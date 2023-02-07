package network

import (
	"fmt"
	"strings"
	"testing"

	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"github.com/openshift/cluster-network-operator/pkg/client/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
)

// NOTE: IsChangeSafe() requires you to have called Validate() beforehand, so we
// don't have to check that invalid configs are considered unsafe to change to.

// OpenShiftSDN validation
// =================================
func TestDisallowCNAdditionOfSameType(t *testing.T) {
	g, infra, prev, next := setupTestInfraAndBasicRenderConfigs(t, "sdn", "sdn")

	next.ClusterNetwork = append(next.ClusterNetwork, operv1.ClusterNetworkEntry{
		CIDR:       "1.2.0.0/16",
		HostPrefix: 24,
	})
	err := IsChangeSafe(prev, next, infra)
	g.Expect(err).To(MatchError(ContainSubstring("unsupported change to ClusterNetwork")))
}

func TestDisallowServiceNetworkChange(t *testing.T) {
	g, infra, prev, next := setupTestInfraAndBasicRenderConfigs(t, "sdn", "sdn")

	// It is not supported to change the ServiceNetwork.
	next.ServiceNetwork = []string{"1.2.3.0/24"}
	err := IsChangeSafe(prev, next, infra)
	g.Expect(err).To(MatchError(ContainSubstring("unsupported change to ServiceNetwork")))
}

func TestDisallowHostPrefixChange(t *testing.T) {
	g, infra, prev, next := setupTestInfraAndBasicRenderConfigs(t, "sdn", "sdn")

	// Changes to the cluster network's prefix are not supported.
	next.ClusterNetwork[0].HostPrefix = 31
	err := IsChangeSafe(prev, next, infra)
	g.Expect(err).To(MatchError(ContainSubstring("unsupported change to ClusterNetwork")))
}

func TestNoErrorOnIdenticalConfigs(t *testing.T) {
	g, infra, prev, next := setupTestInfraAndBasicRenderConfigs(t, "sdn", "sdn")

	// No error should occur when prev equals next.
	err := IsChangeSafe(prev, next, infra)
	g.Expect(err).NotTo(HaveOccurred())
}

// Migration from OpenShiftSDN to OVNKubernetes validation
// =================================
func TestClusterNetworkChangeOkOnMigration(t *testing.T) {
	g, infra, prev, next := setupTestInfraAndBasicRenderConfigs(t, "sdn", "ovn")

	prev.Migration = &operv1.NetworkMigration{NetworkType: "OVNKubernetes"}

	// You can change cluster network during migration.
	next.ClusterNetwork = append(next.ClusterNetwork,
		operv1.ClusterNetworkEntry{
			CIDR:       "1.2.0.0/16",
			HostPrefix: 24,
		},
	)
	err := IsChangeSafe(prev, next, infra)
	g.Expect(err).NotTo(HaveOccurred())

}

// Invalid miscellaneous migration validation
// =================================
func TestServiceNetworkChangeNotOkOnMigration(t *testing.T) {
	g, infra, prev, next := setupTestInfraAndBasicRenderConfigs(t, "sdn", "sdn")

	prev.Migration = &operv1.NetworkMigration{NetworkType: "OVNKubernetes"}

	// You can't change service network during migration.
	next.ServiceNetwork = []string{"1.2.3.0/24"}
	err := IsChangeSafe(prev, next, infra)
	g.Expect(err).To(MatchError(ContainSubstring("cannot change ServiceNetwork during migration")))
}

func TestDisallowNetworkTypeChangeWithoutMigration(t *testing.T) {
	g, infra, prev, next := setupTestInfraAndBasicRenderConfigs(t, "sdn", "sdn")

	// You can't change default network type when not doing migration.
	next.DefaultNetwork.Type = "Kuryr"
	err := IsChangeSafe(prev, next, infra)
	g.Expect(err).To(MatchError(ContainSubstring("cannot change default network type when not doing migration")))
}

func TestDisallowNonTargetTypeForMigration(t *testing.T) {
	g, infra, prev, next := setupTestInfraAndBasicRenderConfigs(t, "sdn", "sdn")

	// You can't change default network type to non-target migration network type.
	prev.Migration = &operv1.NetworkMigration{NetworkType: "OVNKubernetes"}
	next.DefaultNetwork.Type = "Kuryr"
	err := IsChangeSafe(prev, next, infra)
	g.Expect(err).To(MatchError(ContainSubstring("can only change default network type to the target migration network type")))
}

func TestDisallowMigrationTypeChangeWhenNotNull(t *testing.T) {
	g, infra, prev, next := setupTestInfraAndBasicRenderConfigs(t, "sdn", "ovn")

	// You can't change the migration network type when it is not null.
	next.Migration = &operv1.NetworkMigration{NetworkType: "OVNKubernetes"}
	prev.Migration = &operv1.NetworkMigration{NetworkType: "Kuryr"}
	err := IsChangeSafe(prev, next, infra)
	g.Expect(err).To(MatchError(ContainSubstring("cannot change migration network type after migration has started")))
}

// OVNKubernetes DualStack validation
// =================================
func TestSingleToDualStackIsOk(t *testing.T) {
	g, infra, prev, next := setupTestInfraAndBasicRenderConfigs(t, "ovn", "ovn")

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
	g, infra, prev, next := setupTestInfraAndBasicRenderConfigs(t, "ovn", "ovn")

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
	g, infra, prev, next := setupTestInfraAndBasicRenderConfigs(t, "ovn", "ovn")

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
	g, infra, prev, next := setupTestInfraAndBasicRenderConfigs(t, "ovn", "ovn")

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
	g, infra, prev, next := setupTestInfraAndBasicRenderConfigs(t, "ovn", "ovn")

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

func TestAllowMigrationOnlyForBareMetalOrNoneType(t *testing.T) {
	g, infra, prev, next := setupTestInfraAndBasicRenderConfigs(t, "ovn", "ovn")

	// You can't migrate from single-stack to dual-stack if this is anything else but
	// BareMetal or NonePlatformType
	infra.PlatformType = configv1.AzurePlatformType

	next.ServiceNetwork = append(next.ServiceNetwork, "fd02::/112")
	next.ClusterNetwork = append(next.ClusterNetwork, operv1.ClusterNetworkEntry{
		CIDR:       "fd01::/48",
		HostPrefix: 64,
	},
	)
	err := IsChangeSafe(prev, next, infra)
	g.Expect(err).To(MatchError(ContainSubstring(fmt.Sprintf("%s is not one of the supported platforms for dual stack (%s)", infra.PlatformType,
		strings.Join(dualStackPlatforms.List(), ", ")))))
	// ... but the migration in the other direction should work
	err = IsChangeSafe(next, prev, infra)
	g.Expect(err).NotTo(HaveOccurred())
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

	objs, _, err := Render(prev, bootstrapResult, manifestDir, client)
	g.Expect(err).NotTo(HaveOccurred())

	// Validate that openshift-sdn isn't rendered
	g.Expect(objs).NotTo(ContainElement(HaveKubernetesID("DaemonSet", "openshift-sdn", "ovs")))

	// validate that Multus is still rendered
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "multus")))

	// validate that the openshift-network-features namespace and role bindings are still rendered
	g.Expect(objs).To(ContainElement(HaveKubernetesID("Role", "openshift-config-managed", "openshift-network-public-role")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("RoleBinding", "openshift-config-managed", "openshift-network-public-role-binding")))

	// TODO(cdc) validate that kube-proxy is rendered
}

func Test_getMultusAdmissionControllerReplicas(t *testing.T) {
	type args struct {
		bootstrapResult *bootstrap.BootstrapResult
	}
	tests := []struct {
		name string
		args args
		want int
	}{
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
			if got := getMultusAdmissionControllerReplicas(tt.args.bootstrapResult); got != tt.want {
				t.Errorf("getMultusAdmissionControllerReplicas() = %v, want %v", got, tt.want)
			}
		})
	}
}

func fillDefaults(conf, previous *operv1.NetworkSpec) {
	FillDefaults(conf, previous, 1400)
}

func setupTestInfraAndBasicRenderConfigs(t *testing.T, prevType, nextType string) (
	*GomegaWithT,
	*bootstrap.InfraStatus,
	*operv1.NetworkSpec,
	*operv1.NetworkSpec) {

	g := NewGomegaWithT(t)
	infra := &fakeBootstrapResult().Infra

	var prev, next *operv1.NetworkSpec
	if prevType == "sdn" {
		prev = OpenShiftSDNConfig.Spec.DeepCopy()
	} else if prevType == "ovn" {
		prev = OVNKubernetesConfig.Spec.DeepCopy()
	}
	if nextType == "sdn" {
		next = OpenShiftSDNConfig.Spec.DeepCopy()
	} else if nextType == "ovn" {
		next = OVNKubernetesConfig.Spec.DeepCopy()
	}

	fillDefaults(prev, nil)
	fillDefaults(next, nil)
	return g, infra, prev, next
}