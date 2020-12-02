package network

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"

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

	cc := *ClusterConfig.DeepCopy()
	err := ValidateClusterConfig(cc)
	g.Expect(err).NotTo(HaveOccurred())

	haveError := func(cfg configv1.NetworkSpec, substr string) {
		t.Helper()
		err = ValidateClusterConfig(cc)
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
	res := ValidateClusterConfig(cc)
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
}

func TestStatusFromConfig(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := OpenShiftSDNConfig.DeepCopy()
	FillDefaults(&crd.Spec, nil)

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
	FillDefaults(&crd.Spec, nil)

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
