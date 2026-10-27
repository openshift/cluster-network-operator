package network

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/client/fake"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

// noSecrets is the getter for specs without passwordSecret references: any
// call is a bug.
func noSecrets(t *testing.T) bgpVIPSecretGetter {
	return func(name string) (*corev1.Secret, error) {
		t.Fatalf("unexpected password Secret lookup for %q", name)
		return nil, nil
	}
}

func basicAuthSecret(name, password string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: bgpVIPSourceSecretNamespace},
		Type:       corev1.SecretTypeBasicAuth,
		Data:       map[string][]byte{corev1.BasicAuthPasswordKey: []byte(password)},
	}
}

func frrConfiguration(g *WithT, objs []*uns.Unstructured) *uns.Unstructured {
	for _, obj := range objs {
		if obj.GetKind() == "FRRConfiguration" {
			return obj
		}
	}
	g.Expect(objs).To(ContainElement(HaveField("Object", HaveKeyWithValue("kind", "FRRConfiguration"))))
	return nil
}

func neighborsOf(g *WithT, frr *uns.Unstructured) []any {
	routers, found, err := uns.NestedSlice(frr.Object, "spec", "bgp", "routers")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(routers).To(HaveLen(1))
	// CRD prefixes would render as unconditional `network` statements,
	// bypassing the health gate.
	_, found, err = uns.NestedStringSlice(routers[0].(map[string]any), "prefixes")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeFalse())
	neighbors, found, err := uns.NestedSlice(routers[0].(map[string]any), "neighbors")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	return neighbors
}

func rawConfigOf(g *WithT, frr *uns.Unstructured) string {
	rawConfig, found, err := uns.NestedString(frr.Object, "spec", "raw", "rawConfig")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(rawConfig).To(HavePrefix("router bgp"))
	g.Expect(rawConfig).NotTo(ContainSubstring("import-table"))
	return rawConfig
}

func TestBuildFRRConfigurationObjects(t *testing.T) {
	g := NewGomegaWithT(t)
	spec := mcfgv1alpha1.BGPVIPConfigSpec{
		LocalASN:     64512,
		DefaultPeers: []mcfgv1alpha1.BGPVIPPeer{{PeerAddress: "192.168.111.1", PeerASN: 64513}},
	}

	objs, err := buildFRRConfigurationObjects(spec, []string{"192.168.111.5"}, []string{"192.168.111.4"}, noSecrets(t))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).To(HaveLen(1))
	frr := frrConfiguration(g, objs)
	g.Expect(frr.GetName()).To(Equal("bgp-vip"))
	g.Expect(frr.GetNamespace()).To(Equal("openshift-frr-k8s"))
	// No node selector: the CR applies to all nodes.
	_, found, err := uns.NestedMap(frr.Object, "spec", "nodeSelector")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeFalse())

	neighbors := neighborsOf(g, frr)
	g.Expect(neighbors).To(HaveLen(1))
	neighbor := neighbors[0].(map[string]any)
	_, found, err = uns.NestedMap(neighbor, "toAdvertise")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeFalse())
	g.Expect(neighbor["address"]).To(Equal("192.168.111.1"))
	g.Expect(neighbor["asn"]).To(Equal(int64(64513)))
	// Omitted peer fields stay omitted.
	g.Expect(neighbor).NotTo(HaveKey("password"))
	g.Expect(neighbor).NotTo(HaveKey("passwordSecret"))
	g.Expect(neighbor).NotTo(HaveKey("bfdProfile"))
	g.Expect(neighbor).NotTo(HaveKey("ebgpMultiHop"))
	g.Expect(neighbor).NotTo(HaveKey("port"))
	g.Expect(neighbor).NotTo(HaveKey("holdTime"))
	bfdProfiles, found, err := uns.NestedSlice(frr.Object, "spec", "bgp", "bfdProfiles")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(bfdProfiles).To(BeEmpty())

	rawConfig := rawConfigOf(g, frr)
	g.Expect(rawConfig).To(ContainSubstring("router bgp 64512"))
	g.Expect(rawConfig).To(ContainSubstring("redistribute table-direct 198 route-map BGP-VIP-ROUTES-V4"))
	g.Expect(rawConfig).To(ContainSubstring("route-map BGP-VIP-ROUTES-V4 permit 10"))
	g.Expect(rawConfig).To(ContainSubstring("match ip address prefix-list BGP-VIP-PREFIXES-V4"))
	g.Expect(rawConfig).To(ContainSubstring("route-map BGP-VIP-ROUTES-V4 deny 20"))
	g.Expect(rawConfig).To(ContainSubstring("ip prefix-list BGP-VIP-PREFIXES-V4 seq 10 permit 192.168.111.5/32"))
	g.Expect(rawConfig).To(ContainSubstring("ip prefix-list BGP-VIP-PREFIXES-V4 seq 20 permit 192.168.111.4/32"))
	g.Expect(rawConfig).To(ContainSubstring("route-map 192.168.111.1-out permit 4000\n match ip address prefix-list BGP-VIP-PREFIXES-V4"))
	// No IPv6 VIPs: no v6 blocks at all.
	g.Expect(rawConfig).NotTo(ContainSubstring("-out permit 4001"))
	g.Expect(rawConfig).NotTo(ContainSubstring("address-family ipv6"))
	g.Expect(rawConfig).NotTo(ContainSubstring("BGP-VIP-ROUTES-V6"))
	g.Expect(rawConfig).NotTo(ContainSubstring("BGP-VIP-PREFIXES-V6"))
}

// TestBuildFRRConfigurationObjectsAllOptionalFields locks the mapping of
// every optional CR field, with the password rendered inline (the interim
// for metallb/frr-k8s#484) and hostOverrides left to the static pods.
func TestBuildFRRConfigurationObjectsAllOptionalFields(t *testing.T) {
	g := NewGomegaWithT(t)
	spec := mcfgv1alpha1.BGPVIPConfigSpec{
		LocalASN: 64512,
		DefaultPeers: []mcfgv1alpha1.BGPVIPPeer{{
			PeerAddress:          "192.168.111.1",
			PeerASN:              64513,
			PasswordSecret:       mcfgv1alpha1.BGPVIPPasswordSecret{Name: "tor-password"},
			Port:                 1790,
			FailureDetection:     mcfgv1alpha1.FailureDetectionBFD,
			PeerReachability:     mcfgv1alpha1.PeerReachabilityMultiHop,
			HoldTimeSeconds:      90,
			KeepaliveTimeSeconds: 30,
		}},
		Communities: []mcfgv1alpha1.BGPCommunity{"64512:100"},
		HostOverrides: []mcfgv1alpha1.BGPVIPHostPeers{{
			Hostname: "master-0",
			Peers:    []mcfgv1alpha1.BGPVIPPeer{{PeerAddress: "192.168.1.1", PeerASN: 64513}},
		}},
	}
	lookups := 0
	getSecret := func(name string) (*corev1.Secret, error) {
		lookups++
		g.Expect(name).To(Equal("tor-password"))
		return basicAuthSecret(name, "s3cret"), nil
	}

	objs, err := buildFRRConfigurationObjects(spec, []string{"192.168.111.5"}, []string{"192.168.111.4"}, getSecret)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).To(HaveLen(1), "inline rendering emits no Secret copy")
	g.Expect(lookups).To(Equal(1))
	frr := frrConfiguration(g, objs)

	neighbors := neighborsOf(g, frr)
	g.Expect(neighbors).To(HaveLen(1), "hostOverrides are not part of the cluster-wide CR")
	neighbor := neighbors[0].(map[string]any)
	g.Expect(neighbor["password"]).To(Equal("s3cret"))
	g.Expect(neighbor).NotTo(HaveKey("passwordSecret"))
	g.Expect(neighbor["ebgpMultiHop"]).To(BeTrue())
	g.Expect(neighbor["bfdProfile"]).To(Equal("vip-bfd"))
	g.Expect(neighbor["port"]).To(Equal(int64(1790)))
	// Seconds become the metav1.Duration strings the FRRConfiguration takes.
	g.Expect(neighbor["holdTime"]).To(Equal("90s"))
	g.Expect(neighbor["keepaliveTime"]).To(Equal("30s"))

	bfdProfiles, found, err := uns.NestedSlice(frr.Object, "spec", "bgp", "bfdProfiles")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(bfdProfiles).To(HaveLen(1))

	rawConfig := rawConfigOf(g, frr)
	g.Expect(rawConfig).To(ContainSubstring(" set community 64512:100 additive"))
	g.Expect(rawConfig).To(ContainSubstring("route-map 192.168.111.1-out permit 4000\n match ip address prefix-list BGP-VIP-PREFIXES-V4"))
	g.Expect(rawConfig).NotTo(ContainSubstring("192.168.1.1"), "override peers get no egress permit in the cluster-wide CR")
}

// HoldTimer and DirectlyConnected are the FRR defaults: no bfdProfile, no
// ebgpMultiHop, and no BFD profile object.
func TestBuildFRRConfigurationObjectsDefaultEnums(t *testing.T) {
	g := NewGomegaWithT(t)
	spec := mcfgv1alpha1.BGPVIPConfigSpec{
		LocalASN: 64512,
		DefaultPeers: []mcfgv1alpha1.BGPVIPPeer{{
			PeerAddress:      "192.168.111.1",
			PeerASN:          64513,
			FailureDetection: mcfgv1alpha1.FailureDetectionHoldTimer,
			PeerReachability: mcfgv1alpha1.PeerReachabilityDirectlyConnected,
		}},
	}
	objs, err := buildFRRConfigurationObjects(spec, []string{"192.168.111.5"}, []string{"192.168.111.4"}, noSecrets(t))
	g.Expect(err).NotTo(HaveOccurred())
	neighbor := neighborsOf(g, frrConfiguration(g, objs))[0].(map[string]any)
	g.Expect(neighbor).NotTo(HaveKey("bfdProfile"))
	g.Expect(neighbor).NotTo(HaveKey("ebgpMultiHop"))
	bfdProfiles, _, err := uns.NestedSlice(frrConfiguration(g, objs).Object, "spec", "bgp", "bfdProfiles")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(bfdProfiles).To(BeEmpty())
}

// TestBuildFRRConfigurationObjectsDualStack locks the per-family rendering.
func TestBuildFRRConfigurationObjectsDualStack(t *testing.T) {
	g := NewGomegaWithT(t)
	spec := mcfgv1alpha1.BGPVIPConfigSpec{
		LocalASN:     64512,
		DefaultPeers: []mcfgv1alpha1.BGPVIPPeer{{PeerAddress: "192.168.111.1", PeerASN: 64513}},
	}

	objs, err := buildFRRConfigurationObjects(spec,
		[]string{"192.168.111.5", "fd2e:6f44:5dd8::5"}, []string{"192.168.111.4", "fd2e:6f44:5dd8::4"}, noSecrets(t))
	g.Expect(err).NotTo(HaveOccurred())
	rawConfig := rawConfigOf(g, frrConfiguration(g, objs))
	g.Expect(rawConfig).To(ContainSubstring("redistribute table-direct 198 route-map BGP-VIP-ROUTES-V4"))
	g.Expect(rawConfig).To(ContainSubstring("redistribute table-direct 198 route-map BGP-VIP-ROUTES-V6"))
	g.Expect(rawConfig).To(ContainSubstring("route-map BGP-VIP-ROUTES-V6 permit 10"))
	g.Expect(rawConfig).To(ContainSubstring("match ipv6 address prefix-list BGP-VIP-PREFIXES-V6"))
	g.Expect(rawConfig).To(ContainSubstring("route-map BGP-VIP-ROUTES-V6 deny 20"))
	g.Expect(rawConfig).To(ContainSubstring("ip prefix-list BGP-VIP-PREFIXES-V4 seq 10 permit 192.168.111.5/32"))
	g.Expect(rawConfig).To(ContainSubstring("ip prefix-list BGP-VIP-PREFIXES-V4 seq 20 permit 192.168.111.4/32"))
	g.Expect(rawConfig).To(ContainSubstring("ipv6 prefix-list BGP-VIP-PREFIXES-V6 seq 10 permit fd2e:6f44:5dd8::5/128"))
	g.Expect(rawConfig).To(ContainSubstring("ipv6 prefix-list BGP-VIP-PREFIXES-V6 seq 20 permit fd2e:6f44:5dd8::4/128"))
	g.Expect(rawConfig).To(ContainSubstring("route-map 192.168.111.1-out permit 4000\n match ip address prefix-list BGP-VIP-PREFIXES-V4"))
	g.Expect(rawConfig).To(ContainSubstring("route-map 192.168.111.1-out permit 4001\n match ipv6 address prefix-list BGP-VIP-PREFIXES-V6"))
}

// With passwordSecret references (renderInlinePassword = false, the state
// after metallb/frr-k8s#511 ships) the neighbor points at a copy of the
// Secret in openshift-frr-k8s, rendered once per Secret.
func TestBuildFRRConfigurationObjectsPasswordSecretReferences(t *testing.T) {
	g := NewGomegaWithT(t)
	spec := mcfgv1alpha1.BGPVIPConfigSpec{
		LocalASN: 64512,
		DefaultPeers: []mcfgv1alpha1.BGPVIPPeer{
			{PeerAddress: "192.168.111.1", PeerASN: 64513, PasswordSecret: mcfgv1alpha1.BGPVIPPasswordSecret{Name: "tor-password"}},
			{PeerAddress: "fd2e:6f44:5dd8::1", PeerASN: 64513, PasswordSecret: mcfgv1alpha1.BGPVIPPasswordSecret{Name: "tor-password"}},
		},
	}
	getSecret := func(name string) (*corev1.Secret, error) { return basicAuthSecret(name, "s3cret"), nil }

	objs, err := buildFRRConfigurationObjectsWithPasswords(spec, []string{"192.168.111.5"}, []string{"192.168.111.4"}, getSecret, false)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).To(HaveLen(2), "one Secret copy for the two references, plus the FRRConfiguration")

	secretCopy := objs[0]
	g.Expect(secretCopy.GetKind()).To(Equal("Secret"))
	g.Expect(secretCopy.GetName()).To(Equal("tor-password"))
	g.Expect(secretCopy.GetNamespace()).To(Equal(bgpVIPTargetSecretNamespace))
	g.Expect(secretCopy.GetLabels()).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "cluster-network-operator"))
	secretType, _, err := uns.NestedString(secretCopy.Object, "type")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(secretType).To(Equal(string(corev1.SecretTypeBasicAuth)))

	for _, n := range neighborsOf(g, frrConfiguration(g, objs)) {
		neighbor := n.(map[string]any)
		g.Expect(neighbor).NotTo(HaveKey("password"))
		g.Expect(neighbor["passwordSecret"]).To(Equal(map[string]any{"name": "tor-password", "namespace": bgpVIPTargetSecretNamespace}))
	}
}

// A password Secret that cannot be resolved or does not meet the contract
// fails the render; the error names the Secret, never its contents.
func TestBuildFRRConfigurationObjectsPasswordSecretErrors(t *testing.T) {
	spec := mcfgv1alpha1.BGPVIPConfigSpec{
		LocalASN:     64512,
		DefaultPeers: []mcfgv1alpha1.BGPVIPPeer{{PeerAddress: "192.168.111.1", PeerASN: 64513, PasswordSecret: mcfgv1alpha1.BGPVIPPasswordSecret{Name: "tor-password"}}},
	}
	cases := map[string]struct {
		secret *corev1.Secret
		err    error
		want   string
	}{
		"lookup error":      {err: errors.New("boom"), want: "failed to read the BGP VIP password Secret openshift-config/tor-password: boom"},
		"wrong type":        {secret: &corev1.Secret{Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"password": []byte("x")}}, want: `has type "Opaque", expected "kubernetes.io/basic-auth"`},
		"missing key":       {secret: &corev1.Secret{Type: corev1.SecretTypeBasicAuth, Data: map[string][]byte{"username": []byte("x")}}, want: `has no "password" key`},
		"password too long": {secret: basicAuthSecret("tor-password", strings.Repeat("x", bgpVIPPasswordMaxBytes+1)), want: "exceeds the 80-byte TCP MD5 limit"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			g := NewGomegaWithT(t)
			getSecret := func(string) (*corev1.Secret, error) { return tc.secret, tc.err }
			for _, inline := range []bool{true, false} {
				objs, err := buildFRRConfigurationObjectsWithPasswords(spec, []string{"192.168.111.5"}, []string{"192.168.111.4"}, getSecret, inline)
				g.Expect(err).To(MatchError(ContainSubstring(tc.want)))
				g.Expect(err.Error()).NotTo(ContainSubstring("xxxx"), "the password must not leak into the error")
				g.Expect(objs).To(BeNil())
			}
		})
	}
}

func TestValidateBGPVIPSpec(t *testing.T) {
	g := NewGomegaWithT(t)

	valid := mcfgv1alpha1.BGPVIPConfigSpec{
		LocalASN:     64512,
		DefaultPeers: []mcfgv1alpha1.BGPVIPPeer{{PeerAddress: "192.168.111.1", PeerASN: 64513}},
		HostOverrides: []mcfgv1alpha1.BGPVIPHostPeers{{
			Hostname: "master-0",
			Peers:    []mcfgv1alpha1.BGPVIPPeer{{PeerAddress: "192.168.111.2", PeerASN: 64513}},
		}},
	}
	apiVIPs, ingressVIPs := []string{"192.168.111.5"}, []string{"fd2e:6f44:5dd8::4"}
	g.Expect(validateBGPVIPSpec(valid, apiVIPs, ingressVIPs)).To(Succeed())

	g.Expect(validateBGPVIPSpec(valid, []string{"192.168.111.5\n router bgp 1"}, ingressVIPs)).NotTo(Succeed())
	g.Expect(validateBGPVIPSpec(valid, nil, ingressVIPs)).NotTo(Succeed())
	g.Expect(validateBGPVIPSpec(valid, apiVIPs, nil)).NotTo(Succeed())

	badPeer := valid
	badPeer.DefaultPeers = []mcfgv1alpha1.BGPVIPPeer{{PeerAddress: "not-an-ip", PeerASN: 64513}}
	g.Expect(validateBGPVIPSpec(badPeer, apiVIPs, ingressVIPs)).NotTo(Succeed())

	badOverride := valid
	badOverride.HostOverrides = []mcfgv1alpha1.BGPVIPHostPeers{{Hostname: "master-0", Peers: []mcfgv1alpha1.BGPVIPPeer{{PeerAddress: "", PeerASN: 1}}}}
	g.Expect(validateBGPVIPSpec(badOverride, apiVIPs, ingressVIPs)).NotTo(Succeed())

	badLocalASN := valid
	badLocalASN.LocalASN = 0
	g.Expect(validateBGPVIPSpec(badLocalASN, apiVIPs, ingressVIPs)).NotTo(Succeed())

	badPeerASN := valid
	badPeerASN.DefaultPeers = []mcfgv1alpha1.BGPVIPPeer{{PeerAddress: "192.168.111.1", PeerASN: 4294967296}}
	g.Expect(validateBGPVIPSpec(badPeerASN, apiVIPs, ingressVIPs)).NotTo(Succeed())

	badPort := valid
	badPort.DefaultPeers = []mcfgv1alpha1.BGPVIPPeer{{PeerAddress: "192.168.111.1", PeerASN: 64513, Port: 70000}}
	g.Expect(validateBGPVIPSpec(badPort, apiVIPs, ingressVIPs)).NotTo(Succeed())

	goodCommunity := valid
	goodCommunity.Communities = []mcfgv1alpha1.BGPCommunity{"64512:100", "64512:100:200"}
	g.Expect(validateBGPVIPSpec(goodCommunity, apiVIPs, ingressVIPs)).To(Succeed())

	badCommunity := valid
	badCommunity.Communities = []mcfgv1alpha1.BGPCommunity{"no-export\n router bgp 1"}
	g.Expect(validateBGPVIPSpec(badCommunity, apiVIPs, ingressVIPs)).NotTo(Succeed())
}

func bgpVIPBootstrapResult(apiVIPs, ingressVIPs []string) *bootstrap.BootstrapResult {
	return &bootstrap.BootstrapResult{Infra: bootstrap.InfraStatus{
		PlatformType: configv1.BareMetalPlatformType,
		PlatformStatus: &configv1.PlatformStatus{
			Type: configv1.BareMetalPlatformType,
			BareMetal: &configv1.BareMetalPlatformStatus{
				VIPManagement:        configv1.VIPManagementTypeBGP,
				APIServerInternalIPs: apiVIPs,
				IngressIPs:           ingressVIPs,
			},
		},
	}}
}

func bgpVIPConfigObject(generation int64, peers ...map[string]any) *uns.Unstructured {
	defaultPeers := make([]any, 0, len(peers))
	for _, p := range peers {
		defaultPeers = append(defaultPeers, p)
	}
	return &uns.Unstructured{Object: map[string]any{
		"apiVersion": mcfgv1alpha1.GroupVersion.String(),
		"kind":       "BGPVIPConfig",
		"metadata":   map[string]any{"name": "cluster", "generation": generation},
		"spec":       map[string]any{"localASN": int64(64512), "defaultPeers": defaultPeers},
	}}
}

// captureStatusApply records the server-side apply of the BGPVIPConfig
// status (the fake tracker cannot field-manage unstructured applies) and
// returns the applied SessionsConfigured condition.
func captureStatusApply(g *WithT, client cnoclient.Client) func() map[string]any {
	var applied []byte
	var fieldManager string
	client.Default().Dynamic().(*fakedynamic.FakeDynamicClient).PrependReactor("patch", "bgpvipconfigs",
		func(action clienttesting.Action) (bool, runtime.Object, error) {
			patch := action.(clienttesting.PatchAction)
			g.Expect(patch.GetSubresource()).To(Equal("status"))
			g.Expect(patch.GetPatchType()).To(Equal(types.ApplyPatchType))
			applied = patch.GetPatch()
			fieldManager = patch.(clienttesting.PatchActionImpl).PatchOptions.FieldManager
			return true, &uns.Unstructured{}, nil
		})
	return func() map[string]any {
		g.Expect(applied).NotTo(BeNil(), "a SessionsConfigured status apply is expected")
		g.Expect(fieldManager).To(Equal("cluster-network-operator"))
		var obj map[string]any
		g.Expect(json.Unmarshal(applied, &obj)).To(Succeed())
		status := obj["status"].(map[string]any)
		g.Expect(status).NotTo(HaveKey("observedGeneration"), "the API has no status-level observedGeneration")
		conditions := status["conditions"].([]any)
		g.Expect(conditions).To(HaveLen(1), "only the SessionsConfigured condition is owned")
		return conditions[0].(map[string]any)
	}
}

var bgpVIPOperConf = &operv1.NetworkSpec{AdditionalRoutingCapabilities: &operv1.AdditionalRoutingCapabilities{
	Providers: []operv1.RoutingCapabilitiesProvider{operv1.RoutingCapabilitiesProviderFRR},
}}

// TestRenderBGPVIPFRRConfigurationFromCR covers the entry point: the CR is
// read through the dynamic client, the password Secret through the typed
// one, and the outcome lands on the CR's SessionsConfigured condition.
func TestRenderBGPVIPFRRConfigurationFromCR(t *testing.T) {
	g := NewGomegaWithT(t)
	client := fake.NewFakeClient(basicAuthSecret("tor-password", "s3cret"))
	cr := bgpVIPConfigObject(3, map[string]any{
		"peerAddress": "192.168.111.1", "peerASN": int64(64513),
		"passwordSecret": map[string]any{"name": "tor-password"},
	})
	_, err := client.Default().Dynamic().Resource(BGPVIPConfigGVR).Create(t.Context(), cr, metav1.CreateOptions{})
	g.Expect(err).NotTo(HaveOccurred())
	applied := captureStatusApply(g, client)

	objs, err := renderBGPVIPFRRConfiguration(t.Context(), bgpVIPOperConf, client,
		bgpVIPBootstrapResult([]string{"192.168.111.5"}, []string{"192.168.111.4"}), true)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).To(HaveLen(1))
	neighbor := neighborsOf(g, frrConfiguration(g, objs))[0].(map[string]any)
	g.Expect(neighbor["password"]).To(Equal("s3cret"))
	g.Expect(rawConfigOf(g, frrConfiguration(g, objs))).To(ContainSubstring("ip prefix-list BGP-VIP-PREFIXES-V4 seq 10 permit 192.168.111.5/32"))

	cond := applied()
	g.Expect(cond["type"]).To(Equal(mcfgv1alpha1.BGPVIPConfigSessionsConfigured))
	g.Expect(cond["status"]).To(Equal(string(metav1.ConditionTrue)))
	g.Expect(cond["reason"]).To(Equal("AsExpected"))
	g.Expect(cond["observedGeneration"]).To(Equal(float64(3)))
}

// A missing CR is not an error: nothing is rendered and no status is
// written (there is no object to write it to).
func TestRenderBGPVIPFRRConfigurationNoCR(t *testing.T) {
	g := NewGomegaWithT(t)
	objs, err := renderBGPVIPFRRConfiguration(t.Context(), bgpVIPOperConf, fake.NewFakeClient(),
		bgpVIPBootstrapResult([]string{"192.168.111.5"}, []string{"192.168.111.4"}), true)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).To(BeEmpty())
}

// A render failure (here: the referenced password Secret does not exist)
// is returned and reported as SessionsConfigured=False/RenderFailed.
func TestRenderBGPVIPFRRConfigurationRenderFailed(t *testing.T) {
	g := NewGomegaWithT(t)
	client := fake.NewFakeClient()
	cr := bgpVIPConfigObject(7, map[string]any{
		"peerAddress": "192.168.111.1", "peerASN": int64(64513),
		"passwordSecret": map[string]any{"name": "missing"},
	})
	_, err := client.Default().Dynamic().Resource(BGPVIPConfigGVR).Create(t.Context(), cr, metav1.CreateOptions{})
	g.Expect(err).NotTo(HaveOccurred())
	applied := captureStatusApply(g, client)

	objs, err := renderBGPVIPFRRConfiguration(t.Context(), bgpVIPOperConf, client,
		bgpVIPBootstrapResult([]string{"192.168.111.5"}, []string{"192.168.111.4"}), true)
	g.Expect(err).To(MatchError(ContainSubstring("openshift-config/missing")))
	g.Expect(objs).To(BeNil())

	cond := applied()
	g.Expect(cond["status"]).To(Equal(string(metav1.ConditionFalse)))
	g.Expect(cond["reason"]).To(Equal("RenderFailed"))
	g.Expect(cond["message"]).To(ContainSubstring("openshift-config/missing"))
	g.Expect(cond["observedGeneration"]).To(Equal(float64(7)))
}
