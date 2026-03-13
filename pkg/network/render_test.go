package network

import (
	"fmt"
	"reflect"

	. "github.com/onsi/gomega"
	"github.com/openshift/cluster-network-operator/pkg/client/fake"
	"github.com/openshift/cluster-network-operator/pkg/hypershift"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"github.com/stretchr/testify/assert"
	"k8s.io/client-go/kubernetes/scheme"

	"testing"

	configv1 "github.com/openshift/api/config/v1"
	apifeatures "github.com/openshift/api/features"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var manifestDir = "../../bindata"

// NOTE: IsChangeSafe() requires you to have called Validate() beforehand, so we
// don't have to check that invalid configs are considered unsafe to change to.

func TestDisallowCNAdditionOfSameType(t *testing.T) {
	g, infra, prev, next := setupTestInfraAndBasicRenderConfigs(t, OVNKubernetesConfig, OVNKubernetesConfig)

	next.ClusterNetwork = append(next.ClusterNetwork, operv1.ClusterNetworkEntry{
		CIDR:       "1.2.0.0/16",
		HostPrefix: 24,
	})
	err := IsChangeSafe(prev, next, infra)
	g.Expect(err).To(MatchError(ContainSubstring("adding/removing clusterNetwork entries of the same type is not supported")))
}

func TestDisallowServiceNetworkChange(t *testing.T) {
	g, infra, prev, next := setupTestInfraAndBasicRenderConfigs(t, OVNKubernetesConfig, OVNKubernetesConfig)

	// It is not supported to change the ServiceNetwork.
	next.ServiceNetwork = []string{"1.2.3.0/24"}
	err := IsChangeSafe(prev, next, infra)
	g.Expect(err).To(MatchError(ContainSubstring("unsupported change to ServiceNetwork")))
}

func TestNoErrorOnIdenticalConfigs(t *testing.T) {
	g, infra, prev, next := setupTestInfraAndBasicRenderConfigs(t, OVNKubernetesConfig, OVNKubernetesConfig)

	// No error should occur when prev equals next.
	err := IsChangeSafe(prev, next, infra)
	g.Expect(err).NotTo(HaveOccurred())
}

// Migration validation
// =================================
func TestClusterNetworkNoMigration(t *testing.T) {
	g := NewGomegaWithT(t)
	config := OVNKubernetesConfig.DeepCopy()
	err := Validate(&config.Spec)
	g.Expect(err).NotTo(HaveOccurred())

	config.Spec.Migration = &operv1.NetworkMigration{
		NetworkType: "Whatever",
	}
	err = Validate(&config.Spec)
	g.Expect(err).To(MatchError(ContainSubstring("network type migration is not supported")))
}

// OVNKubernetes DualStack validation
// =================================
func TestSingleToDualStackIsOk(t *testing.T) {
	g, infra, prev, next := setupTestInfraAndBasicRenderConfigs(t, OVNKubernetesConfig, OVNKubernetesConfig)

	infra.PlatformType = configv1.BareMetalPlatformType

	// You can change a single-stack config to dual-stack ...
	next.ServiceNetwork = append(next.ServiceNetwork, "fd02::/112")
	next.ClusterNetwork = append(next.ClusterNetwork, operv1.ClusterNetworkEntry{
		CIDR:       "fd01::/48",
		HostPrefix: 64,
	})
	err := IsChangeSafe(prev, next, infra)
	g.Expect(err).NotTo(HaveOccurred())
	// ... and vice-versa.
	err = IsChangeSafe(next, prev, infra)
	g.Expect(err).NotTo(HaveOccurred())
}

func TestDisallowServiceNetworkChangeV4toV6(t *testing.T) {
	g, infra, prev, next := setupTestInfraAndBasicRenderConfigs(t, OVNKubernetesConfig, OVNKubernetesConfig)

	infra.PlatformType = configv1.BareMetalPlatformType

	// you can't change the ServiceNetwork from single-stack IPv4 to dual-stack IPv6-primary ...
	next.ServiceNetwork = append([]string{"fd02::/112"}, prev.ServiceNetwork...)
	next.ClusterNetwork = append([]operv1.ClusterNetworkEntry{{
		CIDR:       "fd01::/48",
		HostPrefix: 64,
	}}, prev.ClusterNetwork...)
	err := IsChangeSafe(prev, next, infra)
	g.Expect(err).To(MatchError(ContainSubstring("cannot change primary ServiceNetwork when migrating to/from dual-stack")))
	// ... or vice-versa.
	err = IsChangeSafe(next, prev, infra)
	g.Expect(err).To(MatchError(ContainSubstring("cannot change primary ServiceNetwork when migrating to/from dual-stack")))
}

func TestDisallowClusterNetworkChangeV4toV6(t *testing.T) {
	g, infra, prev, next := setupTestInfraAndBasicRenderConfigs(t, OVNKubernetesConfig, OVNKubernetesConfig)

	infra.PlatformType = configv1.BareMetalPlatformType

	// You also cannot change the ClusterNetwork from single-stack IPv4 to dual-stack IPv6-primary ...
	next.ServiceNetwork = append(next.ServiceNetwork, "fd02::/112")
	next.ClusterNetwork = append([]operv1.ClusterNetworkEntry{{
		CIDR:       "fd01::/48",
		HostPrefix: 64,
	},
	}, prev.ClusterNetwork...)
	err := IsChangeSafe(prev, next, infra)
	g.Expect(err).To(MatchError(ContainSubstring("cannot change primary ClusterNetwork when migrating to/from dual-stack")))
	// ... or vice-versa.
	err = IsChangeSafe(next, prev, infra)
	g.Expect(err).To(MatchError(ContainSubstring("cannot change primary ClusterNetwork when migrating to/from dual-stack")))
}

func TestAllowMultipleClusterNetworksOfNewIPFamily(t *testing.T) {
	g, infra, prev, next := setupTestInfraAndBasicRenderConfigs(t, OVNKubernetesConfig, OVNKubernetesConfig)

	infra.PlatformType = configv1.BareMetalPlatformType

	// You can add multiple ClusterNetworks of the new IP family ...
	next.ServiceNetwork = append(next.ServiceNetwork, "fd02::/112")
	next.ClusterNetwork = append(next.ClusterNetwork,
		operv1.ClusterNetworkEntry{
			CIDR:       "fd01::/48",
			HostPrefix: 64,
		},
		operv1.ClusterNetworkEntry{
			CIDR:       "fd02::/48",
			HostPrefix: 64,
		},
	)
	err := IsChangeSafe(prev, next, infra)
	g.Expect(err).NotTo(HaveOccurred())
	// ... and vice-versa.
	err = IsChangeSafe(next, prev, infra)
	g.Expect(err).NotTo(HaveOccurred())
}

func TestDisallowMultipleClusterNetworksOfOldIPFamily(t *testing.T) {
	g, infra, prev, next := setupTestInfraAndBasicRenderConfigs(t, OVNKubernetesConfig, OVNKubernetesConfig)

	infra.PlatformType = configv1.BareMetalPlatformType

	// You can't add any new ClusterNetworks of the old IP family.
	next.ServiceNetwork = append(next.ServiceNetwork, "fd02::/112")
	next.ClusterNetwork = append(next.ClusterNetwork,
		operv1.ClusterNetworkEntry{
			CIDR:       "fd01::/48",
			HostPrefix: 64,
		},
		operv1.ClusterNetworkEntry{
			CIDR:       "1.2.0.0/16",
			HostPrefix: 24,
		},
	)
	err := IsChangeSafe(prev, next, infra)
	g.Expect(err).To(MatchError(ContainSubstring("cannot add additional ClusterNetwork values of original IP family when migrating to dual stack")))
}

func TestAllowMigrationOnlyForSupportedTypes(t *testing.T) {
	g, infra, prev, next := setupTestInfraAndBasicRenderConfigs(t, OVNKubernetesConfig, OVNKubernetesConfig)

	next.ServiceNetwork = append(next.ServiceNetwork, "fd02::/112")
	next.ClusterNetwork = append(next.ClusterNetwork, operv1.ClusterNetworkEntry{
		CIDR:       "fd01::/48",
		HostPrefix: 64,
	},
	)
	// You can't migrate from single-stack to dual-stack if this is anything else but
	// BareMetal, NonePlatformType, and VSphere
	infra.PlatformType = configv1.GCPPlatformType

	err := IsChangeSafe(prev, next, infra)
	g.Expect(err).To(MatchError(ContainSubstring(fmt.Sprintf("%s does not allow conversion to dual-stack cluster", infra.PlatformType))))
	// ... but the migration in the other direction should work
	err = IsChangeSafe(next, prev, infra)
	g.Expect(err).NotTo(HaveOccurred())
}

// ClusterNetwork CIDR tests
func TestAllowExpandingClusterNetworkCIDRMaskForOVN(t *testing.T) {
	g, infra, prev, next := setupTestInfraAndBasicRenderConfigs(t, OVNKubernetesConfig, OVNKubernetesConfig)

	// original is 10.128.0.0/15, so expanding the ip range with a /14 mask should be allowed.
	next.ClusterNetwork[0].CIDR = "10.128.0.0/14"
	err := IsChangeSafe(prev, next, infra)
	g.Expect(err).NotTo(HaveOccurred())
}

func TestDisallowHostPrefixChangeOnOvn(t *testing.T) {
	g, infra, prev, next := setupTestInfraAndBasicRenderConfigs(t, OVNKubernetesConfig, OVNKubernetesConfig)

	// Changes to the cluster network's prefix are not supported.
	next.ClusterNetwork[0].HostPrefix = 31
	err := IsChangeSafe(prev, next, infra)
	g.Expect(err).To(MatchError(ContainSubstring("invalid configuration: [modifying a clusterNetwork's hostPrefix value is unsupported]")))
}

func TestDisallowShrinkingClusterNetworkCIDRMaskForOVN(t *testing.T) {
	g, infra, prev, next := setupTestInfraAndBasicRenderConfigs(t, OVNKubernetesConfig, OVNKubernetesConfig)

	// original is 10.128.0.0/15, but shrinking the ip range with a /16 mask should not be allowed.
	next.ClusterNetwork[0].CIDR = "10.128.0.0/16"
	err := IsChangeSafe(prev, next, infra)
	g.Expect(err).To(MatchError(ContainSubstring("reducing IP range with a larger CIDR mask for clusterNetwork CIDR is unsupported")))
}

func TestDisallowRemovalOfAllClusterNetworkCIDREntries(t *testing.T) {
	g, infra, prev, next := setupTestInfraAndBasicRenderConfigs(t, OVNKubernetesConfig, OVNKubernetesConfig)

	// negative test case to ensure no ill effects when trying to apply a blank CusterNetwork CIDR config
	next.ClusterNetwork[0].CIDR = ""
	err := IsChangeSafe(prev, next, infra)
	g.Expect(err).To(MatchError(ContainSubstring("error parsing CIDR from ClusterNetwork entry : invalid CIDR address: ")))
}

func TestAllowExpandingClusterNetworkCIDRAfterEntriesReordered(t *testing.T) {
	g, infra, prev, next := setupTestInfraAndBasicRenderConfigs(t, OVNKubernetesConfig, OVNKubernetesConfig)

	// multiple clusterNetwork entries can exist and expanding one of them is supported even if the
	// entries have been re-ordered in the change. original 0th element was 10.128.0.0/15 and 1st
	// element is 10.0.0.0/14. Swapping those and modifying the CIDR mask on one of them
	next.ClusterNetwork[0].CIDR = "10.0.0.0/14"
	next.ClusterNetwork[0].HostPrefix = prev.ClusterNetwork[1].HostPrefix
	next.ClusterNetwork[1].CIDR = "10.128.0.0/14"
	next.ClusterNetwork[1].HostPrefix = prev.ClusterNetwork[0].HostPrefix
	err := IsChangeSafe(prev, next, infra)
	g.Expect(err).NotTo(HaveOccurred())
}

func TestDisallowCIDRMaskChangeInDualStackUpdate(t *testing.T) {
	g, infra, prev, next := setupTestInfraAndBasicRenderConfigs(t, OVNKubernetesConfig, OVNKubernetesConfig)

	infra.PlatformType = configv1.BareMetalPlatformType

	// You can add multiple ClusterNetworks of the new IP family, but cannot also change
	// the previously existing clusternetwork CIDR mask at the same time.
	next.ServiceNetwork = append(next.ServiceNetwork, "fd02::/112")
	next.ClusterNetwork = append(next.ClusterNetwork,
		operv1.ClusterNetworkEntry{
			CIDR:       "fd01::/48",
			HostPrefix: 64,
		},
		operv1.ClusterNetworkEntry{
			CIDR:       "fd02::/48",
			HostPrefix: 64,
		},
		operv1.ClusterNetworkEntry{
			CIDR:       "10.128.0.0/14",
			HostPrefix: 23,
		},
	)
	err := IsChangeSafe(prev, next, infra)
	g.Expect(err).To(MatchError(ContainSubstring("cannot add additional ClusterNetwork values of original IP family when migrating to dual stack")))
}

func getDefaultFeatureGatesWithDualStack() featuregates.FeatureGate {
	return featuregates.NewFeatureGate(
		[]configv1.FeatureGateName{apifeatures.FeatureGateDNSNameResolver,
			apifeatures.FeatureGateOVNObservability,
			apifeatures.FeatureGateAWSDualStackInstall,
			apifeatures.FeatureGateAzureDualStackInstall},
		[]configv1.FeatureGateName{},
	)
}

func TestRenderUnknownNetwork(t *testing.T) {
	g := NewGomegaWithT(t)

	config := operv1.Network{
		Spec: operv1.NetworkSpec{
			ServiceNetwork: []string{"172.30.0.0/16"},
			ClusterNetwork: []operv1.ClusterNetworkEntry{
				{
					CIDR:       "10.128.0.0/15",
					HostPrefix: 23,
				},
				{
					CIDR:       "10.0.0.0/14",
					HostPrefix: 24,
				},
			},
			DefaultNetwork: operv1.DefaultNetworkDefinition{
				Type: "MyAwesomeThirdPartyPlugin",
			},
		},
	}

	// Bootstrap a client with an infrastructure object
	if err := configv1.AddToScheme(scheme.Scheme); err != nil {
		t.Fatalf("failed to add configv1 to scheme: %v", err)
	}
	infrastructure := &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Status: configv1.InfrastructureStatus{
			PlatformStatus: &configv1.PlatformStatus{},
		},
	}

	client := fake.NewFakeClient(infrastructure)
	err := createProxy(client)
	g.Expect(err).NotTo(HaveOccurred())

	err = Validate(&config.Spec)
	g.Expect(err).NotTo(HaveOccurred())

	prev := config.Spec.DeepCopy()
	fillDefaults(prev, nil)
	next := config.Spec.DeepCopy()
	fillDefaults(next, nil)

	err = IsChangeSafe(prev, next, &fakeBootstrapResult().Infra)
	g.Expect(err).NotTo(HaveOccurred())

	bootstrapResult, err := Bootstrap(&config, client)
	g.Expect(err).NotTo(HaveOccurred())

	featureGatesCNO := getDefaultFeatureGatesWithDualStack()

	objs, _, err := Render(prev, &configv1.NetworkSpec{}, manifestDir, client, featureGatesCNO, bootstrapResult)
	g.Expect(err).NotTo(HaveOccurred())

	// Validate that ovn-kubernetes isn't rendered
	g.Expect(objs).NotTo(ContainElement(HaveKubernetesID("DaemonSet", "openshift-ovn-kubernetes", "ovnkube-node")))

	// validate that Multus is still rendered
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "multus")))

	// validate that the openshift-network-features namespace and role bindings are still rendered
	g.Expect(objs).To(ContainElement(HaveKubernetesID("Role", "openshift-config-managed", "openshift-network-public-role")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("RoleBinding", "openshift-config-managed", "openshift-network-public-role-binding")))

	// TODO(cdc) validate that kube-proxy is rendered
}

func Test_getMultusAdmissionControllerReplicas(t *testing.T) {
	type args struct {
		bootstrapResult   *bootstrap.BootstrapResult
		hypershiftEnabled bool
	}
	tests := []struct {
		name string
		args args
		want int
	}{
		{
			name: "External control plane, HyperShift,  highly available infra",
			args: args{
				bootstrapResult: &bootstrap.BootstrapResult{
					Infra: bootstrap.InfraStatus{
						ControlPlaneTopology: configv1.ExternalTopologyMode,
						HostedControlPlane: &hypershift.HostedControlPlane{
							ControllerAvailabilityPolicy: hypershift.HighlyAvailable,
						},
					},
				},
				hypershiftEnabled: true,
			},
			want: 2,
		},
		{
			name: "External control plane, HyperShift, single-replica infra",
			args: args{
				bootstrapResult: &bootstrap.BootstrapResult{
					Infra: bootstrap.InfraStatus{
						ControlPlaneTopology: configv1.ExternalTopologyMode,
						HostedControlPlane: &hypershift.HostedControlPlane{
							ControllerAvailabilityPolicy: hypershift.SingleReplica,
						},
					},
				},
				hypershiftEnabled: true,
			},
			want: 1,
		},
		{
			name: "External control plane, highly available infra",
			args: args{
				bootstrapResult: &bootstrap.BootstrapResult{
					Infra: bootstrap.InfraStatus{
						ControlPlaneTopology:   configv1.ExternalTopologyMode,
						InfrastructureTopology: configv1.HighlyAvailableTopologyMode,
					},
				},
			},
			want: 2,
		},
		{
			name: "External control plane, single-replica infra",
			args: args{
				bootstrapResult: &bootstrap.BootstrapResult{
					Infra: bootstrap.InfraStatus{
						ControlPlaneTopology:   configv1.ExternalTopologyMode,
						InfrastructureTopology: configv1.SingleReplicaTopologyMode,
					},
				},
			},
			want: 1,
		},
		{
			name: "Highly available control-plane, highly available infra",
			args: args{
				bootstrapResult: &bootstrap.BootstrapResult{
					Infra: bootstrap.InfraStatus{
						ControlPlaneTopology:   configv1.HighlyAvailableTopologyMode,
						InfrastructureTopology: configv1.HighlyAvailableTopologyMode,
					},
				},
			},
			want: 2,
		},
		{
			name: "Highly available control-plane, single-replica infra",
			args: args{
				bootstrapResult: &bootstrap.BootstrapResult{
					Infra: bootstrap.InfraStatus{
						ControlPlaneTopology:   configv1.HighlyAvailableTopologyMode,
						InfrastructureTopology: configv1.SingleReplicaTopologyMode,
					},
				},
			},
			want: 2,
		},
		{
			name: "Single-replicas control-plane, single-replica infra",
			args: args{
				bootstrapResult: &bootstrap.BootstrapResult{
					Infra: bootstrap.InfraStatus{
						ControlPlaneTopology:   configv1.SingleReplicaTopologyMode,
						InfrastructureTopology: configv1.SingleReplicaTopologyMode,
					},
				},
			},
			want: 1,
		},
		{
			name: "Single-replicas control-plane, highly-available infra",
			args: args{
				bootstrapResult: &bootstrap.BootstrapResult{
					Infra: bootstrap.InfraStatus{
						ControlPlaneTopology:   configv1.SingleReplicaTopologyMode,
						InfrastructureTopology: configv1.HighlyAvailableTopologyMode,
					},
				},
			},
			want: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := getMultusAdmissionControllerReplicas(tt.args.bootstrapResult, tt.args.hypershiftEnabled); got != tt.want {
				t.Errorf("getMultusAdmissionControllerReplicas() = %v, want %v", got, tt.want)
			}
		})
	}
}

func fillDefaults(conf, previous *operv1.NetworkSpec) {
	FillDefaults(conf, previous, 1400)
}

func setupTestInfraAndBasicRenderConfigs(t *testing.T, prevType, nextType operv1.Network) (
	*GomegaWithT,
	*bootstrap.InfraStatus,
	*operv1.NetworkSpec,
	*operv1.NetworkSpec) {

	g := NewGomegaWithT(t)
	infra := &fakeBootstrapResult().Infra

	prev := prevType.Spec.DeepCopy()
	next := nextType.Spec.DeepCopy()

	fillDefaults(prev, nil)
	fillDefaults(next, nil)
	return g, infra, prev, next
}

func Test_renderNetworkDiagnostics(t *testing.T) {
	type args struct {
		operConf    *operv1.NetworkSpec
		clusterConf *configv1.NetworkSpec
	}
	tests := []struct {
		name        string
		args        args
		want        int
		expectedErr error
	}{
		{
			name: "Disabled when networkDiagnostics is empty and DisableNetworkDiagnostics is true",
			args: args{
				operConf:    &operv1.NetworkSpec{DisableNetworkDiagnostics: true},
				clusterConf: &configv1.NetworkSpec{NetworkDiagnostics: configv1.NetworkDiagnostics{}},
			},
			want:        0,
			expectedErr: nil,
		},
		{
			name: "Disabled when networkDiagnostics mode is disabled",
			args: args{
				operConf:    &operv1.NetworkSpec{},
				clusterConf: &configv1.NetworkSpec{NetworkDiagnostics: configv1.NetworkDiagnostics{Mode: configv1.NetworkDiagnosticsDisabled}},
			},
			want:        0,
			expectedErr: nil,
		},
		{
			name: "networkDiagnostics takes precedence over DisableNetworkDiagnostics",
			args: args{
				operConf:    &operv1.NetworkSpec{DisableNetworkDiagnostics: true},
				clusterConf: &configv1.NetworkSpec{NetworkDiagnostics: configv1.NetworkDiagnostics{Mode: configv1.NetworkDiagnosticsAll}},
			},
			want:        17,
			expectedErr: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := renderNetworkDiagnostics(tt.args.operConf, tt.args.clusterConf, manifestDir)
			if !reflect.DeepEqual(tt.expectedErr, err) {
				t.Errorf("Test_renderNetworkDiagnostics() err = %v, want %v", err, tt.expectedErr)
			}
			assert.Equalf(t, tt.want, len(got), "renderNetworkDiagnostics(%v, %v, %v)", tt.args.operConf, tt.args.clusterConf, manifestDir)
		})
	}
}

func Test_renderAdditionalRoutingCapabilities(t *testing.T) {
	type args struct {
		operConf *operv1.NetworkSpec
	}
	tests := []struct {
		name        string
		args        args
		want        int
		expectedErr error
	}{
		{
			name: "No capability",
			args: args{
				operConf: &operv1.NetworkSpec{},
			},
			want:        0,
			expectedErr: nil,
		},
		{
			name: "FRR capability",
			args: args{
				operConf: &operv1.NetworkSpec{
					AdditionalRoutingCapabilities: &operv1.AdditionalRoutingCapabilities{
						Providers: []operv1.RoutingCapabilitiesProvider{
							operv1.RoutingCapabilitiesProviderFRR,
						},
					},
				},
			},
			want:        20,
			expectedErr: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := renderAdditionalRoutingCapabilities(tt.args.operConf, manifestDir)
			if !reflect.DeepEqual(tt.expectedErr, err) {
				t.Errorf("renderAdditionalRoutingCapabilities() err = %v, want %v", err, tt.expectedErr)
			}
			assert.Equalf(t, tt.want, len(got), "renderAdditionalRoutingCapabilities(%v, %v)", tt.args.operConf, manifestDir)
		})
	}
}
