package network

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	apifeatures "github.com/openshift/api/features"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
)

// BGPVIPConfigGVR identifies the BGPVIPConfig resource: read here and
// watched by the operconfig controller. openshift/client-go has no typed
// clients for it, so both go through the dynamic client.
var BGPVIPConfigGVR = mcfgv1alpha1.GroupVersion.WithResource("bgpvipconfigs")

const (
	// bgpVIPConfigName is the name of the BGPVIPConfig singleton.
	bgpVIPConfigName = "cluster"
	// bgpVIPSourceSecretNamespace is where the BGPVIPConfig API contract
	// places the peer password Secrets; frr-k8s can only resolve
	// passwordSecret references from its own namespace, hence the copy.
	bgpVIPSourceSecretNamespace = "openshift-config"  //nolint:gosec // G101: a namespace name, not a credential
	bgpVIPTargetSecretNamespace = "openshift-frr-k8s" //nolint:gosec // G101: a namespace name, not a credential
	// bgpVIPPasswordMaxBytes is the kernel's TCP MD5 key limit (RFC 2385).
	bgpVIPPasswordMaxBytes = 80
	// bgpVIPFieldManager owns the SessionsConfigured condition on the
	// BGPVIPConfig status; the machine-config-operator owns Rendered.
	bgpVIPFieldManager = "cluster-network-operator"
)

// renderInlinePassword is an INTERIM for metallb/frr-k8s#484: the frr-k8s
// validation webhook compared a neighbor's passwordSecret reference as an
// empty password, so a secret-backed neighbor merged with another producer's
// inline-password neighbor (MetalLB always inlines) was rejected, wedging
// every FRRConfiguration write for the shared neighbor. Until the fix
// (metallb/frr-k8s#511) ships in the frr-k8s image this operator deploys,
// render the resolved password inline, exactly like MetalLB. Flipping this to
// false switches to passwordSecret references plus a Secret copy into
// openshift-frr-k8s; both paths are tested.
const renderInlinePassword = true

// apiCallTimeout bounds direct API calls made while rendering.
const apiCallTimeout = 30 * time.Second

// communityRe matches standard (AA:NN) and large (AA:BB:CC) BGP community
// values; anything else is rejected before being spliced into rawConfig.
var communityRe = regexp.MustCompile(`^\d+:\d+(:\d+)?$`)

// bgpVIPSecretGetter resolves a passwordSecret name to the Secret in
// openshift-config. Injected so rendering stays client-free in tests.
type bgpVIPSecretGetter func(name string) (*corev1.Secret, error)

// validateBGPVIPSpec rejects values that must not reach the FRRConfiguration
// rawConfig. The CRD validates the same at admission; this is the last line
// before text splicing, so it stays.
func validateBGPVIPSpec(spec mcfgv1alpha1.BGPVIPConfigSpec, apiVIPs, ingressVIPs []string) error {
	if spec.LocalASN < 1 || spec.LocalASN > 4294967295 {
		return fmt.Errorf("localASN %d out of range", spec.LocalASN)
	}
	if len(apiVIPs) == 0 || len(ingressVIPs) == 0 {
		return fmt.Errorf("the Infrastructure status carries no API or Ingress VIPs")
	}
	for _, vip := range slices.Concat(apiVIPs, ingressVIPs) {
		if net.ParseIP(vip) == nil {
			return fmt.Errorf("VIP %q is not a valid IP address", vip)
		}
	}
	for _, community := range spec.Communities {
		if !communityRe.MatchString(string(community)) {
			return fmt.Errorf("invalid BGP community %q", community)
		}
	}
	peers := slices.Clone(spec.DefaultPeers)
	for _, override := range spec.HostOverrides {
		peers = append(peers, override.Peers...)
	}
	for _, peer := range peers {
		if net.ParseIP(peer.PeerAddress) == nil {
			return fmt.Errorf("BGP peer address %q is not a valid IP address", peer.PeerAddress)
		}
		if peer.PeerASN < 1 || peer.PeerASN > 4294967295 {
			return fmt.Errorf("BGP peer %s: peerASN %d out of range", peer.PeerAddress, peer.PeerASN)
		}
		if peer.Port != 0 && (peer.Port < 1 || peer.Port > 65535) {
			return fmt.Errorf("BGP peer %s: invalid port %d", peer.PeerAddress, peer.Port)
		}
	}
	return nil
}

// isBGPVIPManagement reports whether the BGPBasedVIPManagement gate is
// enabled and the BareMetal Infrastructure CR requests vipManagement "BGP".
func isBGPVIPManagement(bootstrapResult *bootstrap.BootstrapResult, featureGates featuregates.FeatureGate) bool {
	if bootstrapResult == nil || bootstrapResult.Infra.PlatformStatus == nil ||
		bootstrapResult.Infra.PlatformType != configv1.BareMetalPlatformType ||
		bootstrapResult.Infra.PlatformStatus.BareMetal == nil {
		return false
	}
	// Enabled panics on unknown gates.
	if !slices.Contains(featureGates.KnownFeatures(), apifeatures.FeatureGateBGPBasedVIPManagement) ||
		!featureGates.Enabled(apifeatures.FeatureGateBGPBasedVIPManagement) {
		return false
	}
	return bootstrapResult.Infra.PlatformStatus.BareMetal.VIPManagement == configv1.VIPManagementTypeBGP
}

// bgpVIPInfraVIPs returns the API and Ingress VIPs from the BareMetal
// Infrastructure status; isBGPVIPManagement guarantees it is present.
func bgpVIPInfraVIPs(bootstrapResult *bootstrap.BootstrapResult) ([]string, []string) {
	bm := bootstrapResult.Infra.PlatformStatus.BareMetal
	return slices.Clone(bm.APIServerInternalIPs), slices.Clone(bm.IngressIPs)
}

// renderBGPVIPFRRConfiguration builds the FRRConfiguration for BGP-managed
// VIPs from the BGPVIPConfig "cluster" CR and reports the outcome on its
// SessionsConfigured condition; a no-op unless BGP VIP management is active.
func renderBGPVIPFRRConfiguration(ctx context.Context, conf *operv1.NetworkSpec, client cnoclient.Client, bootstrapResult *bootstrap.BootstrapResult, isBGP bool) ([]*uns.Unstructured, error) {
	if !isBGP {
		return nil, nil
	}
	// The FRRConfiguration CRD ships with the frr-k8s bundle. Checked before
	// the CR is read: without the provider there is nothing to render into,
	// and no condition is written for a CR that was never consulted.
	if conf == nil || conf.AdditionalRoutingCapabilities == nil ||
		!slices.Contains(conf.AdditionalRoutingCapabilities.Providers, operv1.RoutingCapabilitiesProviderFRR) {
		return nil, fmt.Errorf("BGP-based VIP management requires the FRR additional routing capability provider")
	}

	klog.Infof("BGP VIP management is active, rendering the bgp-vip FRRConfiguration")

	getCtx, cancel := context.WithTimeout(ctx, apiCallTimeout)
	defer cancel()
	obj, err := client.Default().Dynamic().Resource(BGPVIPConfigGVR).Get(getCtx, bgpVIPConfigName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			klog.Infof("BGPVIPConfig %q not found yet, skipping FRRConfiguration rendering", bgpVIPConfigName)
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read BGPVIPConfig %q: %w", bgpVIPConfigName, err)
	}
	bgpConfig := &mcfgv1alpha1.BGPVIPConfig{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, bgpConfig); err != nil {
		return nil, setBGPVIPSessionsCondition(ctx, client, bgpConfig, fmt.Errorf("failed to decode BGPVIPConfig %q: %w", bgpVIPConfigName, err))
	}

	apiVIPs, ingressVIPs := bgpVIPInfraVIPs(bootstrapResult)
	if err := validateBGPVIPSpec(bgpConfig.Spec, apiVIPs, ingressVIPs); err != nil {
		return nil, setBGPVIPSessionsCondition(ctx, client, bgpConfig, fmt.Errorf("invalid BGPVIPConfig: %w", err))
	}
	if len(bgpConfig.Spec.HostOverrides) > 0 {
		// Overrides reach control plane nodes through the MCO-rendered
		// per-node peers file (runtimecfg keys them by short hostname);
		// the cluster-wide CR intentionally carries only defaultPeers.
		// See docs/bgp_vip_management.md.
		klog.Warningf("BGPVIPConfig hostOverrides apply to the static pod peers file only; the cluster-wide FRRConfiguration uses defaultPeers")
	}

	objs, err := buildFRRConfigurationObjects(bgpConfig.Spec, apiVIPs, ingressVIPs, func(name string) (*corev1.Secret, error) {
		secretCtx, cancel := context.WithTimeout(ctx, apiCallTimeout)
		defer cancel()
		return client.Default().Kubernetes().CoreV1().Secrets(bgpVIPSourceSecretNamespace).Get(secretCtx, name, metav1.GetOptions{})
	})
	if err := setBGPVIPSessionsCondition(ctx, client, bgpConfig, err); err != nil {
		return nil, err
	}
	return objs, nil
}

// setBGPVIPSessionsCondition reports the render outcome on the BGPVIPConfig
// SessionsConfigured condition through server-side apply on the status
// subresource, owning only that condition (conditions is a map list keyed
// by type, so the machine-config-operator's Rendered condition is left
// alone). It returns renderErr, wrapped with the status-write failure if
// that fails too, so callers pass their render error straight through.
func setBGPVIPSessionsCondition(ctx context.Context, client cnoclient.Client, bgpConfig *mcfgv1alpha1.BGPVIPConfig, renderErr error) error {
	condition := metav1.Condition{
		Type:               mcfgv1alpha1.BGPVIPConfigSessionsConfigured,
		Status:             metav1.ConditionTrue,
		Reason:             "AsExpected",
		Message:            "FRR session configuration rendered",
		ObservedGeneration: bgpConfig.Generation,
		LastTransitionTime: metav1.Now(),
	}
	if renderErr != nil {
		condition.Status = metav1.ConditionFalse
		condition.Reason = "RenderFailed"
		condition.Message = renderErr.Error()
	}
	// Keep lastTransitionTime when the status did not change so the
	// periodic resubmission does not churn the object.
	if existing := meta.FindStatusCondition(bgpConfig.Status.Conditions, condition.Type); existing != nil &&
		existing.Status == condition.Status {
		condition.LastTransitionTime = existing.LastTransitionTime
	}

	patch := &uns.Unstructured{Object: map[string]any{
		"apiVersion": mcfgv1alpha1.GroupVersion.String(),
		"kind":       "BGPVIPConfig",
		"metadata":   map[string]any{"name": bgpVIPConfigName},
		"status": map[string]any{
			"conditions": []any{map[string]any{
				"type":               condition.Type,
				"status":             string(condition.Status),
				"reason":             condition.Reason,
				"message":            condition.Message,
				"observedGeneration": condition.ObservedGeneration,
				"lastTransitionTime": condition.LastTransitionTime.UTC().Format(time.RFC3339),
			}},
		},
	}}
	applyCtx, cancel := context.WithTimeout(ctx, apiCallTimeout)
	defer cancel()
	_, err := client.Default().Dynamic().Resource(BGPVIPConfigGVR).ApplyStatus(applyCtx, bgpVIPConfigName, patch,
		metav1.ApplyOptions{FieldManager: bgpVIPFieldManager, Force: true})
	if err != nil {
		statusErr := fmt.Errorf("failed to update the BGPVIPConfig status: %w", err)
		if renderErr != nil {
			return errors.Join(renderErr, statusErr)
		}
		return statusErr
	}
	return renderErr
}

// buildFRRConfigurationObjects renders the cluster-wide (no node selector)
// FRRConfiguration, plus the password Secret copies when passwords are
// rendered as references. The CR spec carries only the BGP sessions;
// advertisement is in rawConfig because the CRD cannot express health-gated
// redistribution: declared prefixes render as unconditional `network`
// statements and toAdvertise only permits declared prefixes
// (metallb/frr-k8s#469). Per-node correctness comes from the health gate:
// kube-vip installs a table-198 route only where the VIP's backend is
// healthy.
func buildFRRConfigurationObjects(spec mcfgv1alpha1.BGPVIPConfigSpec, apiVIPs, ingressVIPs []string, getSecret bgpVIPSecretGetter) ([]*uns.Unstructured, error) {
	return buildFRRConfigurationObjectsWithPasswords(spec, apiVIPs, ingressVIPs, getSecret, renderInlinePassword)
}

// buildFRRConfigurationObjectsWithPasswords takes the password rendering
// mode as a parameter so the passwordSecret path stays tested while
// renderInlinePassword selects the inline one.
func buildFRRConfigurationObjectsWithPasswords(spec mcfgv1alpha1.BGPVIPConfigSpec, apiVIPs, ingressVIPs []string, getSecret bgpVIPSecretGetter, inlinePassword bool) ([]*uns.Unstructured, error) {
	neighbors := []any{}
	secretCopies := []*uns.Unstructured{}
	copied := map[string]bool{}
	for _, peer := range spec.DefaultPeers {
		neighbor := map[string]any{
			"address": peer.PeerAddress,
			"asn":     peer.PeerASN,
		}
		if name := peer.PasswordSecret.Name; name != "" {
			source, err := bgpVIPPasswordSecret(name, getSecret)
			if err != nil {
				return nil, err
			}
			if inlinePassword {
				neighbor["password"] = string(source.Data[corev1.BasicAuthPasswordKey])
			} else {
				// frr-k8s resolves the reference from its own namespace, so
				// point it at the copy rendered alongside.
				neighbor["passwordSecret"] = map[string]any{
					"name":      name,
					"namespace": bgpVIPTargetSecretNamespace,
				}
				if !copied[name] {
					secretCopy, err := bgpVIPSecretCopy(source)
					if err != nil {
						return nil, err
					}
					secretCopies = append(secretCopies, secretCopy)
					copied[name] = true
				}
			}
		}
		if peer.FailureDetection == mcfgv1alpha1.FailureDetectionBFD {
			neighbor["bfdProfile"] = "vip-bfd"
		}
		if peer.PeerReachability == mcfgv1alpha1.PeerReachabilityMultiHop {
			neighbor["ebgpMultiHop"] = true
		}
		if peer.Port != 0 {
			neighbor["port"] = int64(peer.Port)
		}
		// The FRRConfiguration timer fields are metav1.Duration strings.
		if peer.HoldTimeSeconds != 0 {
			neighbor["holdTime"] = fmt.Sprintf("%ds", peer.HoldTimeSeconds)
		}
		if peer.KeepaliveTimeSeconds != 0 {
			neighbor["keepaliveTime"] = fmt.Sprintf("%ds", peer.KeepaliveTimeSeconds)
		}
		neighbors = append(neighbors, neighbor)
	}

	bfdProfiles := []any{}
	if slices.ContainsFunc(spec.DefaultPeers, func(peer mcfgv1alpha1.BGPVIPPeer) bool {
		return peer.FailureDetection == mcfgv1alpha1.FailureDetectionBFD
	}) {
		bfdProfiles = append(bfdProfiles, map[string]any{
			"name":             "vip-bfd",
			"receiveInterval":  int64(300),
			"transmitInterval": int64(300),
		})
	}

	frrSpec := map[string]any{
		"bgp": map[string]any{
			"bfdProfiles": bfdProfiles,
			"routers": []any{
				map[string]any{
					"asn":       spec.LocalASN,
					"neighbors": neighbors,
				},
			},
		},
		"raw": map[string]any{
			"rawConfig": buildBGPVIPRawConfig(spec, apiVIPs, ingressVIPs),
		},
	}

	obj := &uns.Unstructured{
		Object: map[string]any{
			"apiVersion": "frrk8s.metallb.io/v1beta1",
			"kind":       "FRRConfiguration",
			"metadata": map[string]any{
				"name":      "bgp-vip",
				"namespace": bgpVIPTargetSecretNamespace,
				"labels": map[string]any{
					"app.kubernetes.io/managed-by": bgpVIPFieldManager,
				},
			},
			"spec": frrSpec,
		},
	}

	return append(secretCopies, obj), nil
}

// bgpVIPPasswordSecret resolves and validates a peer password Secret: type
// kubernetes.io/basic-auth with a non-empty "password" key of at most 80
// bytes, the TCP MD5 limit. Errors name the Secret, never its contents.
func bgpVIPPasswordSecret(name string, getSecret bgpVIPSecretGetter) (*corev1.Secret, error) {
	source, err := getSecret(name)
	if err != nil {
		return nil, fmt.Errorf("failed to read the BGP VIP password Secret %s/%s: %w", bgpVIPSourceSecretNamespace, name, err)
	}
	if source.Type != corev1.SecretTypeBasicAuth {
		return nil, fmt.Errorf("BGP VIP password Secret %s/%s has type %q, expected %q", bgpVIPSourceSecretNamespace, name, source.Type, corev1.SecretTypeBasicAuth)
	}
	password := source.Data[corev1.BasicAuthPasswordKey]
	if len(password) == 0 {
		return nil, fmt.Errorf("BGP VIP password Secret %s/%s has no %q key", bgpVIPSourceSecretNamespace, name, corev1.BasicAuthPasswordKey)
	}
	if len(password) > bgpVIPPasswordMaxBytes {
		return nil, fmt.Errorf("BGP VIP password Secret %s/%s: %q exceeds the %d-byte TCP MD5 limit", bgpVIPSourceSecretNamespace, name, corev1.BasicAuthPasswordKey, bgpVIPPasswordMaxBytes)
	}
	return source, nil
}

// bgpVIPSecretCopy renders the openshift-frr-k8s copy of a password Secret,
// emitted through the same apply pipeline as the FRRConfiguration so
// ownership and pruning stay uniform.
func bgpVIPSecretCopy(source *corev1.Secret) (*uns.Unstructured, error) {
	secretCopy := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      source.Name,
			Namespace: bgpVIPTargetSecretNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": bgpVIPFieldManager,
			},
		},
		Type: source.Type,
		Data: source.Data,
	}
	out, err := runtime.DefaultUnstructuredConverter.ToUnstructured(secretCopy)
	if err != nil {
		return nil, fmt.Errorf("failed to convert the BGP VIP password Secret %s: %w", source.Name, err)
	}
	return &uns.Unstructured{Object: out}, nil
}

// buildBGPVIPRawConfig mirrors MCO's bootstrap frr.conf.tmpl: redistribute
// kernel table 198 filtered to exactly the VIP prefixes, plus egress permits
// for them. Per-family blocks render only when that family has VIPs.
func buildBGPVIPRawConfig(spec mcfgv1alpha1.BGPVIPConfigSpec, apiVIPs, ingressVIPs []string) string {
	var v4Prefixes, v6Prefixes []string
	for _, vip := range slices.Concat(apiVIPs, ingressVIPs) {
		if strings.Contains(vip, ":") {
			v6Prefixes = append(v6Prefixes, vip+"/128")
		} else {
			v4Prefixes = append(v4Prefixes, vip+"/32")
		}
	}

	var b strings.Builder
	// table-direct reads the kernel table directly; no `ip import-table`.
	fmt.Fprintf(&b, "router bgp %d\n", spec.LocalASN)
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
		for _, community := range spec.Communities {
			fmt.Fprintf(&b, " set community %s additive\n", community)
		}
		b.WriteString("route-map BGP-VIP-ROUTES-V4 deny 20\n")
	}
	if len(v6Prefixes) > 0 {
		b.WriteString("route-map BGP-VIP-ROUTES-V6 permit 10\n")
		b.WriteString(" match ipv6 address prefix-list BGP-VIP-PREFIXES-V6\n")
		for _, community := range spec.Communities {
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
	for _, peer := range spec.DefaultPeers {
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
