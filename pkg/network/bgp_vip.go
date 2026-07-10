package network

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
)

// bgpBasedVIPManagementFeatureGate gates BGP-based VIP management. Defined
// locally until the gate lands in vendored openshift/api (openshift/api#2923).
const bgpBasedVIPManagementFeatureGate = configv1.FeatureGateName("BGPBasedVIPManagement")

// bgpVIPPeer is one BGP peer from the bgp-vip-config ConfigMap. String-typed
// BFDEnabled and EBGPMultiHop match the installer's BGPPeerConfig
// serialization.
type bgpVIPPeer struct {
	PeerAddress   string `json:"peerAddress"`
	PeerASN       int64  `json:"peerASN"`
	Password      string `json:"password,omitempty"`
	BFDEnabled    string `json:"bfdEnabled,omitempty"`
	EBGPMultiHop  string `json:"ebgpMultiHop,omitempty"`
	HoldTime      string `json:"holdTime,omitempty"`
	KeepaliveTime string `json:"keepaliveTime,omitempty"`
}

// bgpVIPConfigData is the parsed config.json of the bgp-vip-config
// ConfigMap; the schema matches baremetal-runtimecfg's FRRPeerMapping.
type bgpVIPConfigData struct {
	LocalASN      int64                   `json:"localASN"`
	DefaultPeers  []bgpVIPPeer            `json:"defaultPeers"`
	Communities   []string                `json:"communities,omitempty"`
	APIVIPs       []string                `json:"apiVIPs"`
	IngressVIPs   []string                `json:"ingressVIPs"`
	HostOverrides map[string][]bgpVIPPeer `json:"hostOverrides,omitempty"`
}

// apiCallTimeout bounds direct API calls made while rendering.
const apiCallTimeout = 30 * time.Second

// communityRe matches standard (AA:NN) and large (AA:BB:CC) BGP community
// values; anything else is rejected before being spliced into rawConfig.
var communityRe = regexp.MustCompile(`^\d+:\d+(:\d+)?$`)

// validateBGPVIPConfig rejects non-IP values before they are spliced into
// the FRRConfiguration rawConfig.
func validateBGPVIPConfig(cfg bgpVIPConfigData) error {
	if cfg.LocalASN < 1 || cfg.LocalASN > 4294967295 {
		return fmt.Errorf("localASN %d out of range", cfg.LocalASN)
	}
	for _, vip := range slices.Concat(cfg.APIVIPs, cfg.IngressVIPs) {
		if net.ParseIP(vip) == nil {
			return fmt.Errorf("VIP %q is not a valid IP address", vip)
		}
	}
	for _, community := range cfg.Communities {
		if !communityRe.MatchString(community) {
			return fmt.Errorf("invalid BGP community %q", community)
		}
	}
	peers := slices.Clone(cfg.DefaultPeers)
	for _, hostPeers := range cfg.HostOverrides {
		peers = append(peers, hostPeers...)
	}
	for _, peer := range peers {
		if net.ParseIP(peer.PeerAddress) == nil {
			return fmt.Errorf("BGP peer address %q is not a valid IP address", peer.PeerAddress)
		}
		if peer.PeerASN < 1 || peer.PeerASN > 4294967295 {
			return fmt.Errorf("BGP peer %s: peerASN %d out of range", peer.PeerAddress, peer.PeerASN)
		}
		for _, d := range []string{peer.HoldTime, peer.KeepaliveTime} {
			if d == "" {
				continue
			}
			if _, err := time.ParseDuration(d); err != nil {
				return fmt.Errorf("BGP peer %s: invalid duration %q", peer.PeerAddress, d)
			}
		}
		for _, v := range []string{peer.BFDEnabled, peer.EBGPMultiHop} {
			if v != "" && v != "true" && v != "false" {
				return fmt.Errorf("BGP peer %s: invalid boolean %q", peer.PeerAddress, v)
			}
		}
	}
	return nil
}

// isBGPVIPManagement reports whether the BGPBasedVIPManagement gate is
// enabled and the BareMetal Infrastructure CR requests vipManagement "BGP".
func isBGPVIPManagement(client cnoclient.Client, bootstrapResult *bootstrap.BootstrapResult, featureGates featuregates.FeatureGate) (bool, error) {
	if bootstrapResult == nil || bootstrapResult.Infra.PlatformStatus == nil ||
		bootstrapResult.Infra.PlatformType != configv1.BareMetalPlatformType ||
		bootstrapResult.Infra.PlatformStatus.BareMetal == nil {
		return false, nil
	}
	// Enabled panics on unknown gates.
	if !slices.Contains(featureGates.KnownFeatures(), bgpBasedVIPManagementFeatureGate) ||
		!featureGates.Enabled(bgpBasedVIPManagementFeatureGate) {
		return false, nil
	}
	// The typed client would silently drop the not-yet-vendored
	// vipManagement field (openshift/api#2923).
	ctx, cancel := context.WithTimeout(context.Background(), apiCallTimeout)
	defer cancel()
	infra, err := client.Default().Dynamic().Resource(schema.GroupVersionResource{
		Group: "config.openshift.io", Version: "v1", Resource: "infrastructures",
	}).Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to get Infrastructure CR: %v", err)
	}
	val, found, err := uns.NestedString(infra.Object, "status", "platformStatus", "baremetal", "vipManagement")
	if err != nil {
		return false, fmt.Errorf("malformed vipManagement in Infrastructure CR: %v", err)
	}
	if !found {
		return false, nil
	}
	return val == "BGP", nil
}

// renderBGPVIPFRRConfiguration builds FRRConfiguration CRs for BGP-managed
// VIPs; a no-op unless BGP VIP management is active.
func renderBGPVIPFRRConfiguration(conf *operv1.NetworkSpec, client cnoclient.Client, isBGP bool) ([]*uns.Unstructured, error) {
	if !isBGP {
		return nil, nil
	}
	// The FRRConfiguration CRD ships with the frr-k8s bundle.
	if conf == nil || conf.AdditionalRoutingCapabilities == nil ||
		!slices.Contains(conf.AdditionalRoutingCapabilities.Providers, operv1.RoutingCapabilitiesProviderFRR) {
		return nil, fmt.Errorf("BGP-based VIP management requires the FRR additional routing capability provider")
	}

	klog.Infof("BGP VIP management is active, rendering FRRConfiguration CRs")

	cm := &corev1.ConfigMap{}
	ctx, cancel := context.WithTimeout(context.Background(), apiCallTimeout)
	defer cancel()
	err := client.Default().CRClient().Get(ctx,
		types.NamespacedName{Name: "bgp-vip-config", Namespace: names.APPLIED_NAMESPACE}, cm)
	if err != nil {
		if apierrors.IsNotFound(err) {
			klog.Infof("bgp-vip-config ConfigMap not found yet, skipping FRRConfiguration rendering")
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read bgp-vip-config ConfigMap: %v", err)
	}

	configJSON := cm.Data["config.json"]
	if configJSON == "" {
		return nil, fmt.Errorf("bgp-vip-config ConfigMap has no config.json data")
	}

	var bgpConfig bgpVIPConfigData
	if err := json.Unmarshal([]byte(configJSON), &bgpConfig); err != nil {
		return nil, fmt.Errorf("failed to parse bgp-vip-config: %v", err)
	}
	if err := validateBGPVIPConfig(bgpConfig); err != nil {
		return nil, fmt.Errorf("invalid bgp-vip-config: %v", err)
	}
	if len(bgpConfig.HostOverrides) > 0 {
		// Overrides reach control plane nodes through the MCO-rendered
		// per-node peers file (runtimecfg keys them by short hostname);
		// the cluster-wide CR intentionally carries only defaultPeers.
		// See docs/bgp_vip_management.md.
		klog.Warningf("bgp-vip-config hostOverrides apply to the static pod peers file only; the cluster-wide FRRConfiguration uses defaultPeers")
	}

	return buildFRRConfigurationObjects(bgpConfig)
}

// buildFRRConfigurationObjects renders the cluster-wide (no node selector)
// FRRConfiguration. The CR spec carries only the BGP sessions; advertisement
// is in rawConfig because the CRD cannot express health-gated redistribution:
// declared prefixes render as unconditional `network` statements and
// toAdvertise only permits declared prefixes (metallb/frr-k8s#469).
// Per-node correctness comes from the health gate: kube-vip installs a
// table-198 route only where the VIP's backend is healthy.
func buildFRRConfigurationObjects(cfg bgpVIPConfigData) ([]*uns.Unstructured, error) {
	neighbors := []interface{}{}
	for _, peer := range cfg.DefaultPeers {
		neighbor := map[string]interface{}{
			"address": peer.PeerAddress,
			"asn":     peer.PeerASN,
		}
		if peer.Password != "" {
			neighbor["password"] = peer.Password
		}
		if peer.BFDEnabled == "true" {
			neighbor["bfdProfile"] = "vip-bfd"
		}
		if peer.EBGPMultiHop == "true" {
			neighbor["ebgpMultiHop"] = true
		}
		if peer.HoldTime != "" {
			neighbor["holdTime"] = peer.HoldTime
		}
		if peer.KeepaliveTime != "" {
			neighbor["keepaliveTime"] = peer.KeepaliveTime
		}
		neighbors = append(neighbors, neighbor)
	}

	bfdProfiles := []interface{}{}
	for _, peer := range cfg.DefaultPeers {
		if peer.BFDEnabled == "true" {
			bfdProfiles = append(bfdProfiles, map[string]interface{}{
				"name":             "vip-bfd",
				"receiveInterval":  int64(300),
				"transmitInterval": int64(300),
			})
			break
		}
	}

	spec := map[string]interface{}{
		"bgp": map[string]interface{}{
			"bfdProfiles": bfdProfiles,
			"routers": []interface{}{
				map[string]interface{}{
					"asn":       cfg.LocalASN,
					"neighbors": neighbors,
				},
			},
		},
		"raw": map[string]interface{}{
			"rawConfig": buildBGPVIPRawConfig(cfg),
		},
	}

	obj := &uns.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "frrk8s.metallb.io/v1beta1",
			"kind":       "FRRConfiguration",
			"metadata": map[string]interface{}{
				"name":      "bgp-vip",
				"namespace": "openshift-frr-k8s",
				"labels": map[string]interface{}{
					"app.kubernetes.io/managed-by": "cluster-network-operator",
				},
			},
			"spec": spec,
		},
	}

	return []*uns.Unstructured{obj}, nil
}

// buildBGPVIPRawConfig mirrors MCO's bootstrap frr.conf.tmpl: redistribute
// kernel table 198 filtered to exactly the VIP prefixes, plus egress permits
// for them. Per-family blocks render only when that family has VIPs.
func buildBGPVIPRawConfig(cfg bgpVIPConfigData) string {
	var v4Prefixes, v6Prefixes []string
	for _, vip := range slices.Concat(cfg.APIVIPs, cfg.IngressVIPs) {
		if strings.Contains(vip, ":") {
			v6Prefixes = append(v6Prefixes, vip+"/128")
		} else {
			v4Prefixes = append(v4Prefixes, vip+"/32")
		}
	}

	var b strings.Builder
	// table-direct reads the kernel table directly; no `ip import-table`.
	fmt.Fprintf(&b, "router bgp %d\n", cfg.LocalASN)
	if len(v4Prefixes) > 0 {
		b.WriteString(" address-family ipv4 unicast\n")
		b.WriteString("  redistribute table-direct 198 route-map BGP-VIP-ROUTES-V4\n")
		b.WriteString(" exit-address-family\n")
	}
	if len(v6Prefixes) > 0 {
		b.WriteString(" address-family ipv6 unicast\n")
		b.WriteString("  redistribute table-direct 198 route-map BGP-VIP-ROUTES-V6\n")
		b.WriteString(" exit-address-family\n")
	}
	// Communities attach where the VIP routes enter the BGP table, so
	// every egress path carries them (parity with the static pod
	// template's VIP-COMMUNITY route-map).
	if len(v4Prefixes) > 0 {
		b.WriteString("route-map BGP-VIP-ROUTES-V4 permit 10\n")
		b.WriteString(" match ip address prefix-list BGP-VIP-PREFIXES-V4\n")
		for _, community := range cfg.Communities {
			fmt.Fprintf(&b, " set community %s additive\n", community)
		}
		b.WriteString("route-map BGP-VIP-ROUTES-V4 deny 20\n")
	}
	if len(v6Prefixes) > 0 {
		b.WriteString("route-map BGP-VIP-ROUTES-V6 permit 10\n")
		b.WriteString(" match ipv6 address prefix-list BGP-VIP-PREFIXES-V6\n")
		for _, community := range cfg.Communities {
			fmt.Fprintf(&b, " set community %s additive\n", community)
		}
		b.WriteString("route-map BGP-VIP-ROUTES-V6 deny 20\n")
	}
	for i, prefix := range v4Prefixes {
		fmt.Fprintf(&b, "ip prefix-list BGP-VIP-PREFIXES-V4 seq %d permit %s\n", 10*(i+1), prefix)
	}
	for i, prefix := range v6Prefixes {
		fmt.Fprintf(&b, "ipv6 prefix-list BGP-VIP-PREFIXES-V6 seq %d permit %s\n", 10*(i+1), prefix)
	}
	// Egress: frr-k8s renders per-neighbor `<address>-out` route-maps whose
	// generated entries deny everything without toAdvertise. A no-match
	// falls through, so these high-sequence permits open egress for exactly
	// the VIP prefixes. Couples to frr-k8s's route-map naming; native CRD
	// support is proposed in metallb/frr-k8s#469.
	for _, peer := range cfg.DefaultPeers {
		if len(v4Prefixes) > 0 {
			fmt.Fprintf(&b, "route-map %s-out permit 4000\n", peer.PeerAddress)
			b.WriteString(" match ip address prefix-list BGP-VIP-PREFIXES-V4\n")
		}
		if len(v6Prefixes) > 0 {
			fmt.Fprintf(&b, "route-map %s-out permit 4001\n", peer.PeerAddress)
			b.WriteString(" match ipv6 address prefix-list BGP-VIP-PREFIXES-V6\n")
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}
