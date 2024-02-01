package network

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"

	"github.com/openshift/cluster-network-operator/pkg/client/fake"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/util"

	. "github.com/onsi/gomega"
)

var ClusterConfig = configv1.NetworkSpec{
	ClusterNetwork: []configv1.ClusterNetworkEntry{
		{
			CIDR:       "10.0.0.0/22",
			HostPrefix: 24,
		},
		{
			CIDR:       "10.2.0.0/22",
			HostPrefix: 23,
		},
	},
	ServiceNetwork: []string{"192.168.0.0/20"},

	NetworkType: "None",
}

func TestValidateClusterConfig(t *testing.T) {
	g := NewGomegaWithT(t)

	// Bootstrap a client of type Baremetal
	if err := configv1.AddToScheme(scheme.Scheme); err != nil {
		t.Fatalf("failed to add configv1 to scheme: %v", err)
	}
	infrastructure := &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Status: configv1.InfrastructureStatus{
			PlatformStatus: &configv1.PlatformStatus{
				Type: configv1.BareMetalPlatformType,
			},
		},
	}
	client := fake.NewFakeClient(infrastructure)
	err := createProxy(client)
	g.Expect(err).NotTo(HaveOccurred())

	cc := *ClusterConfig.DeepCopy()
	err = ValidateClusterConfig(&configv1.Network{Spec: cc}, client)
	g.Expect(err).NotTo(HaveOccurred())

	haveError := func(cfg configv1.NetworkSpec, substr string) {
		t.Helper()
		err = ValidateClusterConfig(&configv1.Network{Spec: cfg}, client)
		g.Expect(err).To(MatchError(ContainSubstring(substr)))
	}

	// invalid service cidr
	cc.ServiceNetwork[0] = "123q"
	haveError(cc, "could not parse spec.serviceNetwork 123q")

	// service cidr overlap with network
	cc.ServiceNetwork[0] = "10.0.2.0/24"
	haveError(cc, "CIDRs 10.0.2.0/24 and 10.0.0.0/22 overlap")

	// no service cidr
	cc.ServiceNetwork = nil
	haveError(cc, "spec.serviceNetwork must have at least 1 entry")

	// valid clustercidr
	cc = *ClusterConfig.DeepCopy()
	cc.ClusterNetwork[0].CIDR = "1234fz"
	haveError(cc, "could not parse spec.clusterNetwork 1234fz")

	cc.ClusterNetwork[0].CIDR = "192.168.2.0/23"
	haveError(cc, "CIDRs 192.168.0.0/20 and 192.168.2.0/23 overlap")

	cc = *ClusterConfig.DeepCopy()
	cc.ClusterNetwork[1].HostPrefix = 0
	res := ValidateClusterConfig(&configv1.Network{Spec: cc}, client)
	// Since the NetworkType is None, and the hostprefix is unset we don't validate it
	g.Expect(res).Should(BeNil())

	cc = *ClusterConfig.DeepCopy()
	cc.ClusterNetwork[1].HostPrefix = 21
	haveError(cc, "hostPrefix 21 is larger than its cidr 10.2.0.0/22")

	cc = *ClusterConfig.DeepCopy()
	cc.NetworkType = "OpenShiftSDN"
	cc.ClusterNetwork[1].HostPrefix = 0
	haveError(cc, "hostPrefix 0 is larger than its cidr 10.2.0.0/22")

	// network type
	cc = *ClusterConfig.DeepCopy()
	cc.NetworkType = ""
	haveError(cc, "spec.networkType is required")
}

func TestValidClusterConfigLiveMigration(t *testing.T) {
	g := NewGomegaWithT(t)
	tests := []struct {
		name              string
		managedCluster    bool
		hypershiftCluster bool
		annotation        bool
		expectErr         bool
	}{
		{
			"no error when standalone managed cluster and migration label applied",
			true,
			false,
			true,
			false,
		},
		{
			"error when self managed cluster and migration label applied",
			false,
			false,
			true,
			true,
		},
		{

			"no error when standalone managed cluster and migration label not applied",
			true,
			false,
			false,
			false,
		},
		{

			"no error when self managed cluster and migration label not applied",
			false,
			false,
			false,
			false,
		},
		{

			"error when HyperShift and migration label applied",
			true,
			true,
			true,
			true,
		},
	}

	// restore env var flag post test if its set
	hcpEnvVarEnabled := os.Getenv("HYPERSHIFT") != ""
	defer func() {
		if hcpEnvVarEnabled {
			os.Setenv("HYPERSHIFT", "")
		}
	}()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cc := *ClusterConfig.DeepCopy()
			net := &configv1.Network{Spec: cc}
			infrastructure := &configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Status: configv1.InfrastructureStatus{
					PlatformStatus: &configv1.PlatformStatus{},
				},
			}
			client := fake.NewFakeClient(infrastructure)
			err := createProxy(client)
			g.Expect(err).NotTo(HaveOccurred())
			if tt.hypershiftCluster {
				// HyperShift is detected if an env var is set
				os.Setenv("HYPERSHIFT", "")
			} else {
				os.Unsetenv("HYPERSHIFT")
			}
			if tt.annotation {
				net.Annotations = map[string]string{names.NetworkTypeMigrationAnnotation: ""}
			}
			if tt.managedCluster && !tt.hypershiftCluster {
				err := client.Default().CRClient().Create(context.TODO(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: util.STANDALONE_MANAGED_CLUSTER_NAMESPACE}})
				if err != nil {
					t.Errorf("failed to create test namespace %q: %v", util.STANDALONE_MANAGED_CLUSTER_NAMESPACE, err)
				}
			}
			err = ValidateClusterConfig(net, client)
			if tt.expectErr && err == nil {
				t.Errorf("expected error but got nil")
			}
			if !tt.expectErr && err != nil {
				t.Errorf("expected no err but got error: %v", err)
			}
		})
	}
}

func TestValidateClusterConfigDualStack(t *testing.T) {
	g := NewGomegaWithT(t)

	// Bootstrap a client of type Baremetal
	if err := configv1.AddToScheme(scheme.Scheme); err != nil {
		t.Fatalf("failed to add configv1 to scheme: %v", err)
	}
	infrastructure := &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Status: configv1.InfrastructureStatus{
			PlatformStatus: &configv1.PlatformStatus{
				Type: configv1.BareMetalPlatformType,
			},
		},
	}
	client := fake.NewFakeClient(infrastructure)
	err := createProxy(client)
	g.Expect(err).NotTo(HaveOccurred())

	cc := *ClusterConfig.DeepCopy()
	err = ValidateClusterConfig(&configv1.Network{Spec: cc}, client)
	g.Expect(err).NotTo(HaveOccurred())

	haveError := func(cfg configv1.NetworkSpec, substr string) {
		t.Helper()
		err = ValidateClusterConfig(&configv1.Network{Spec: cc}, client)
		g.Expect(err).To(MatchError(ContainSubstring(substr)))
	}

	// Multiple ServiceNetworks of same family
	cc = *ClusterConfig.DeepCopy()
	cc.ServiceNetwork = append(cc.ServiceNetwork, "192.168.128.0/20")
	haveError(cc, "at most one IPv4 and one IPv6")

	// Too many ServiceNetworks
	cc = *ClusterConfig.DeepCopy()
	cc.ServiceNetwork = append(cc.ServiceNetwork, "192.168.128.0/20")
	cc.ServiceNetwork = append(cc.ServiceNetwork, "fd02::/112")
	haveError(cc, "at most one IPv4 and one IPv6")

	// Dual-Stack Service but Single-Stack Cluster
	cc = *ClusterConfig.DeepCopy()
	cc.ServiceNetwork = append(cc.ServiceNetwork, "fd02::/112")
	haveError(cc, "both be IPv4-only, both be IPv6-only, or both be dual-stack")

	// Dual-Stack Cluster but Single-Stack Service
	cc = *ClusterConfig.DeepCopy()
	cc.ClusterNetwork = append(cc.ClusterNetwork, configv1.ClusterNetworkEntry{
		CIDR:       "fd01::/48",
		HostPrefix: 64,
	})
	haveError(cc, "both be IPv4-only, both be IPv6-only, or both be dual-stack")

	// IPv4 Cluster but IPv6 Service
	cc = *ClusterConfig.DeepCopy()
	cc.ServiceNetwork[0] = "fd02::/112"
	haveError(cc, "both be IPv4-only, both be IPv6-only, or both be dual-stack")

	// Proper dual-stack
	cc = *ClusterConfig.DeepCopy()
	cc.ServiceNetwork = append(cc.ServiceNetwork, "fd02::/112")
	cc.ClusterNetwork = append(cc.ClusterNetwork, configv1.ClusterNetworkEntry{
		CIDR:       "fd01::/48",
		HostPrefix: 64,
	})
	err = ValidateClusterConfig(&configv1.Network{Spec: cc}, client)
	g.Expect(err).NotTo(HaveOccurred())

	// You can't use dual-stack if this is anything else but BareMetal or NonePlatformType
	infrastructure.Status.PlatformStatus.Type = configv1.AzurePlatformType
	client = fake.NewFakeClient(infrastructure)
	err = createProxy(client)
	g.Expect(err).NotTo(HaveOccurred())
	cc = *ClusterConfig.DeepCopy()
	cc.ServiceNetwork = append(cc.ServiceNetwork, "fd02::/112")
	cc.ClusterNetwork = append(cc.ClusterNetwork, configv1.ClusterNetworkEntry{
		CIDR:       "fd01::/48",
		HostPrefix: 64,
	})
	haveError(cc, fmt.Sprintf("%s is not one of the supported platforms for dual stack (%s)",
		infrastructure.Status.PlatformStatus.Type, strings.Join(dualStackPlatforms.List(), ", ")))
}

func TestMergeClusterConfig(t *testing.T) {
	g := NewGomegaWithT(t)

	cc := *ClusterConfig.DeepCopy()

	oc := operv1.NetworkSpec{}

	MergeClusterConfig(&oc, cc)
	g.Expect(oc).To(Equal(operv1.NetworkSpec{
		OperatorSpec:   operv1.OperatorSpec{ManagementState: "Managed"},
		ServiceNetwork: []string{"192.168.0.0/20"},
		ClusterNetwork: []operv1.ClusterNetworkEntry{
			{
				CIDR:       "10.0.0.0/22",
				HostPrefix: 24,
			},
			{
				CIDR:       "10.2.0.0/22",
				HostPrefix: 23,
			},
		},
		DefaultNetwork: operv1.DefaultNetworkDefinition{
			Type: "None",
		},
	}))

	cc.ServiceNetwork = append(cc.ServiceNetwork, "2001:db8::2/64")
	MergeClusterConfig(&oc, cc)
	g.Expect(oc).To(Equal(operv1.NetworkSpec{
		OperatorSpec:   operv1.OperatorSpec{ManagementState: "Managed"},
		ServiceNetwork: []string{"192.168.0.0/20", "2001:db8::2/64"},
		ClusterNetwork: []operv1.ClusterNetworkEntry{
			{
				CIDR:       "10.0.0.0/22",
				HostPrefix: 24,
			},
			{
				CIDR:       "10.2.0.0/22",
				HostPrefix: 23,
			},
		},
		DefaultNetwork: operv1.DefaultNetworkDefinition{
			Type: "None",
		},
	}))
}

func TestStatusFromConfig(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := OpenShiftSDNConfig.DeepCopy()
	fillDefaults(&crd.Spec, nil)

	var mtu uint32 = 1300
	crd.Spec.DefaultNetwork.OpenShiftSDNConfig.MTU = &mtu

	status := StatusFromOperatorConfig(&crd.Spec, &configv1.NetworkStatus{})
	g.Expect(status).To(Equal(&configv1.NetworkStatus{
		ClusterNetwork: []configv1.ClusterNetworkEntry{
			{
				CIDR:       "10.128.0.0/15",
				HostPrefix: 23,
			},
			{
				CIDR:       "10.0.0.0/14",
				HostPrefix: 24,
			},
		},
		ServiceNetwork:    []string{"172.30.0.0/16"},
		ClusterNetworkMTU: 1300,

		NetworkType: "OpenShiftSDN",
	}))

	*crd.Spec.DefaultNetwork.OpenShiftSDNConfig.MTU = 1500
	status = StatusFromOperatorConfig(&crd.Spec, status)
	g.Expect(status).To(Equal(&configv1.NetworkStatus{
		ClusterNetwork: []configv1.ClusterNetworkEntry{
			{
				CIDR:       "10.128.0.0/15",
				HostPrefix: 23,
			},
			{
				CIDR:       "10.0.0.0/14",
				HostPrefix: 24,
			},
		},
		ServiceNetwork:    []string{"172.30.0.0/16"},
		ClusterNetworkMTU: 1500,

		NetworkType: "OpenShiftSDN",
	}))

	// If someone manually edits the status we will overwrite them
	status.ClusterNetwork = status.ClusterNetwork[:1]
	status.ServiceNetwork = []string{"172.30.0.0/17"}
	status.ClusterNetworkMTU = 1450

	status = StatusFromOperatorConfig(&crd.Spec, status)
	g.Expect(status).To(Equal(&configv1.NetworkStatus{
		ClusterNetwork: []configv1.ClusterNetworkEntry{
			{
				CIDR:       "10.128.0.0/15",
				HostPrefix: 23,
			},
			{
				CIDR:       "10.0.0.0/14",
				HostPrefix: 24,
			},
		},
		ServiceNetwork:    []string{"172.30.0.0/16"},
		ClusterNetworkMTU: 1500,

		NetworkType: "OpenShiftSDN",
	}))
}

func TestStatusFromConfigUnknown(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := OpenShiftSDNConfig.DeepCopy()
	fillDefaults(&crd.Spec, nil)

	crd.Spec.DefaultNetwork.Type = "None"

	status := StatusFromOperatorConfig(&crd.Spec, &configv1.NetworkStatus{})
	g.Expect(status).To(Equal(&configv1.NetworkStatus{
		ClusterNetwork: []configv1.ClusterNetworkEntry{
			{
				CIDR:       "10.128.0.0/15",
				HostPrefix: 23,
			},
			{
				CIDR:       "10.0.0.0/14",
				HostPrefix: 24,
			},
		},
		ServiceNetwork:    []string{"172.30.0.0/16"},
		ClusterNetworkMTU: 0,

		NetworkType: "None",
	}))

	// The external network plugin updates the status itself...
	status.ClusterNetwork = status.ClusterNetwork[:1]
	status.ServiceNetwork = []string{"172.30.0.0/17"}
	status.ClusterNetworkMTU = 1450

	// The external changes should be preserved
	status = StatusFromOperatorConfig(&crd.Spec, status)
	g.Expect(status).To(Equal(&configv1.NetworkStatus{
		ClusterNetwork: []configv1.ClusterNetworkEntry{
			{
				CIDR:       "10.128.0.0/15",
				HostPrefix: 23,
			},
		},
		ServiceNetwork:    []string{"172.30.0.0/17"},
		ClusterNetworkMTU: 1450,

		NetworkType: "None",
	}))
}
