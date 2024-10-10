package network

import (
	"fmt"
	"strings"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	v1 "github.com/openshift/api/network/v1"
	operv1 "github.com/openshift/api/operator/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"github.com/openshift/cluster-network-operator/pkg/client/fake"
	"github.com/openshift/cluster-network-operator/pkg/hypershift"
	"github.com/openshift/cluster-network-operator/pkg/names"

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
	networkConfig := *ClusterConfig.DeepCopy()
	networkConfig.NetworkType = "OVNKubernetes"

	tests := []struct {
		name             string
		infraRes         *bootstrap.InfraStatus
		config           *configv1.Network
		objects          []crclient.Object
		expectErr        bool
		expectedErrorMsg string
	}{
		{
			"no error when standalone cluster and migration label applied",
			&bootstrap.InfraStatus{},
			&configv1.Network{
				Spec: networkConfig,
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{names.NetworkTypeMigrationAnnotation: ""},
				}},
			[]crclient.Object{&operv1.Network{ObjectMeta: metav1.ObjectMeta{Name: names.OPERATOR_CONFIG}}},
			false,
			"",
		},
		{

			"no error when standalone cluster and migration label not applied",
			&bootstrap.InfraStatus{},
			&configv1.Network{Spec: networkConfig},
			[]crclient.Object{&operv1.Network{ObjectMeta: metav1.ObjectMeta{Name: names.OPERATOR_CONFIG}}},
			false,
			"",
		},
		{
			"error when HyperShift and migration label applied",
			&bootstrap.InfraStatus{HostedControlPlane: &hypershift.HostedControlPlane{}},
			&configv1.Network{
				Spec: networkConfig,
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{names.NetworkTypeMigrationAnnotation: ""},
				},
				Status: configv1.NetworkStatus{NetworkType: string(operv1.NetworkTypeOpenShiftSDN)}},
			[]crclient.Object{&operv1.Network{ObjectMeta: metav1.ObjectMeta{Name: names.OPERATOR_CONFIG}}},
			true,
			"network type live migration is not supported on HyperShift clusters",
		},
		{
			"error when trying to migrate to an unsupported cni",
			&bootstrap.InfraStatus{},
			&configv1.Network{
				Spec: *ClusterConfig.DeepCopy(),
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{names.NetworkTypeMigrationAnnotation: ""},
				}},
			[]crclient.Object{&operv1.Network{ObjectMeta: metav1.ObjectMeta{Name: names.OPERATOR_CONFIG}}},
			true,
			"network type live migration is only supported for OVNKubernetes and OpenShiftSDN CNI",
		},
		{
			"error when trying to migrate from sdn in multinenat mode",
			&bootstrap.InfraStatus{},
			&configv1.Network{
				Spec: networkConfig,
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{names.NetworkTypeMigrationAnnotation: ""},
				},
				Status: configv1.NetworkStatus{NetworkType: string(operv1.NetworkTypeOpenShiftSDN)}},
			[]crclient.Object{&operv1.Network{
				ObjectMeta: metav1.ObjectMeta{Name: names.OPERATOR_CONFIG},
				Spec: operv1.NetworkSpec{
					DefaultNetwork: operv1.DefaultNetworkDefinition{
						Type:               operv1.NetworkTypeOpenShiftSDN,
						OpenShiftSDNConfig: &operv1.OpenShiftSDNConfig{Mode: "Multitenant"}}}}},
			true,
			"network type live migration is not supported on SDN Multitenant clusters",
		},
		{
			"error when cluster network overlaps with ovn-k internal subnet overlap",
			&bootstrap.InfraStatus{},
			&configv1.Network{
				Spec: networkConfig,
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{names.NetworkTypeMigrationAnnotation: ""},
				},
				Status: configv1.NetworkStatus{NetworkType: string(operv1.NetworkTypeOpenShiftSDN)}},
			[]crclient.Object{&operv1.Network{
				ObjectMeta: metav1.ObjectMeta{Name: names.OPERATOR_CONFIG},
				Spec: operv1.NetworkSpec{
					DefaultNetwork: operv1.DefaultNetworkDefinition{
						Type: operv1.NetworkTypeOpenShiftSDN,
						OVNKubernetesConfig: &operv1.OVNKubernetesConfig{
							// 10.2.0.0/22 is the second clusternetwork in networkConfig
							V4InternalSubnet: "10.2.2.0/24",
						}}}}},
			true,
			"network clusterNetwork(10.2.0.0/22) overlaps with network v4InternalSubnet(10.2.2.0/24)",
		},
		{
			"error when service overlaps with ovn-k internal transit switch subnet overlap",
			&bootstrap.InfraStatus{},
			&configv1.Network{
				Spec: networkConfig,
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{names.NetworkTypeMigrationAnnotation: ""},
				},
				Status: configv1.NetworkStatus{NetworkType: string(operv1.NetworkTypeOpenShiftSDN)}},
			[]crclient.Object{&operv1.Network{
				ObjectMeta: metav1.ObjectMeta{Name: names.OPERATOR_CONFIG},
				Spec: operv1.NetworkSpec{
					DefaultNetwork: operv1.DefaultNetworkDefinition{
						Type: operv1.NetworkTypeOpenShiftSDN,
						OVNKubernetesConfig: &operv1.OVNKubernetesConfig{
							// "192.168.0.0/20" is the service network in networkConfig
							IPv4: &operv1.IPv4OVNKubernetesConfig{
								InternalTransitSwitchSubnet: "192.0.0.0/8",
							},
						}}}}},
			true,

			"network serviceNetwork(192.168.0.0/20) overlaps with network v4InternalTransitSwitchSubnet(192.0.0.0/8)",
		},
		{
			"error when service network overlaps with ovn-k internal transit switch subnet overlap",
			&bootstrap.InfraStatus{},
			&configv1.Network{
				Spec: func() configv1.NetworkSpec {
					cfg := networkConfig
					cfg.ClusterNetwork = []configv1.ClusterNetworkEntry{
						{
							CIDR:       "fd00:1234::/48",
							HostPrefix: 64,
						}}
					//fd97::/64 is the default v6 transit switch subnet
					cfg.ServiceNetwork = []string{"fd97::/48"}
					return cfg
				}(),
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{names.NetworkTypeMigrationAnnotation: ""},
				},
				Status: configv1.NetworkStatus{NetworkType: string(operv1.NetworkTypeOpenShiftSDN)}},
			[]crclient.Object{&operv1.Network{
				ObjectMeta: metav1.ObjectMeta{Name: names.OPERATOR_CONFIG},
				Spec: operv1.NetworkSpec{
					DefaultNetwork: operv1.DefaultNetworkDefinition{
						Type: operv1.NetworkTypeOpenShiftSDN,
					}}}},
			true,
			"network serviceNetwork(fd97::/48) overlaps with network v6InternalTransitSwitchSubnet(fd97::/64)",
		},
		{
			"error when pods with 'pod.network.openshift.io/assign-macvlan' annotation are present in the cluster",
			&bootstrap.InfraStatus{},
			&configv1.Network{
				Spec: networkConfig,
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{names.NetworkTypeMigrationAnnotation: ""},
				},
				Status: configv1.NetworkStatus{NetworkType: string(operv1.NetworkTypeOpenShiftSDN)}},
			[]crclient.Object{
				&operv1.Network{
					ObjectMeta: metav1.ObjectMeta{Name: names.OPERATOR_CONFIG},
					Spec: operv1.NetworkSpec{
						DefaultNetwork: operv1.DefaultNetworkDefinition{
							Type: operv1.NetworkTypeOpenShiftSDN,
						},
					},
				},
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{v1.AssignMacvlanAnnotation: ""},
					},
				},
			},
			true,
			"network type live migration is not supported for pods with \"pod.network.openshift.io/assign-macvlan\" annotation. Please remove all egress router pods",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewFakeClient(tt.objects...)
			err := validateClusterConfig(tt.config, tt.infraRes, client)
			if tt.expectErr {
				g.Expect(err).To(HaveOccurred())
				if tt.expectedErrorMsg != "" {
					g.Expect(err).To(MatchError(Equal(tt.expectedErrorMsg)))
				}
			}
			if !tt.expectErr {
				g.Expect(err).NotTo(HaveOccurred())
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
