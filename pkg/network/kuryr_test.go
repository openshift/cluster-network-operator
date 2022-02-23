package network

import (
	"testing"

	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"

	"github.com/gophercloud/utils/openstack/clientconfig"
	. "github.com/onsi/gomega"
)

var KuryrConfig = operv1.Network{
	Spec: operv1.NetworkSpec{
		ServiceNetwork: []string{"172.30.0.0/16"},
		ClusterNetwork: []operv1.ClusterNetworkEntry{
			{
				CIDR:       "10.128.0.0/15",
				HostPrefix: 24,
			},
		},
		DefaultNetwork: operv1.DefaultNetworkDefinition{
			Type:        operv1.NetworkTypeKuryr,
			KuryrConfig: &operv1.KuryrConfig{},
		},
	},
}

var FakeBootstrapResult = bootstrap.BootstrapResult{
	Kuryr: bootstrap.KuryrBootstrapResult{
		PodSubnetpool:     "pod-subnetpool-id",
		ServiceSubnet:     "svc-subnet-id",
		WorkerNodesRouter: "worker-nodes-router",
		OpenStackCloud: clientconfig.Cloud{
			AuthType: "password",
			AuthInfo: &clientconfig.AuthInfo{
				AuthURL: "https://foo.bar:8080",
			},
		},
	},
}

// TestRenderKuryr has some simple rendering tests
func TestRenderKuryr(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := KuryrConfig.DeepCopy()
	config := &crd.Spec

	errs := validateKuryr(config)
	g.Expect(errs).To(HaveLen(0))

	fillDefaults(config, nil)

	objs, err := renderKuryr(config, &FakeBootstrapResult, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-kuryr", "kuryr-cni")))

	// It's important that the namespace is before any namespaced types;
	// for now, just test that it's the second item in the list, after
	// the ClusterNetwork.
	g.Expect(objs[0]).To(HaveKubernetesID("Namespace", "", "openshift-kuryr"))

	g.Expect(objs).To(ContainElement(HaveKubernetesID("ClusterRole", "", "kuryr")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ServiceAccount", "openshift-kuryr", "kuryr")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ClusterRoleBinding", "", "kuryr")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("Deployment", "openshift-kuryr", "kuryr-controller")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-kuryr", "kuryr-cni")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ConfigMap", "openshift-kuryr", "kuryr-config")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("CustomResourceDefinition", "", "kuryrnetworks.openstack.org")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("CustomResourceDefinition", "", "kuryrports.openstack.org")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("CustomResourceDefinition", "", "kuryrnetworkpolicies.openstack.org")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("CustomResourceDefinition", "", "kuryrloadbalancers.openstack.org")))
}

func TestValidateKuryr(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := KuryrConfig.DeepCopy()
	config := &crd.Spec

	err := validateKuryr(config)
	g.Expect(err).To(BeEmpty())

	errExpect := func(substr string) {
		t.Helper()
		g.Expect(validateKuryr(config)).To(
			ContainElement(MatchError(
				ContainSubstring(substr))))
	}

	config.ServiceNetwork = []string{"172.30.0.0/16", "172.31.0.0/16"}
	errExpect("serviceNetwork must have exactly 1 entry")

	config.ClusterNetwork = []operv1.ClusterNetworkEntry{
		{
			CIDR:       "10.128.0.0/15",
			HostPrefix: 24,
		},
		{
			CIDR:       "10.129.0.0/15",
			HostPrefix: 24,
		},
	}
	errExpect("clusterNetwork must have exactly 1 entry")

	config.ServiceNetwork = []string{"172.30.0.0/16"}
	config.ClusterNetwork = []operv1.ClusterNetworkEntry{
		{
			CIDR:       "172.31.0.0/16",
			HostPrefix: 16,
		},
	}
	errExpect("will overlap with cluster network")

	config.ServiceNetwork = []string{"172.31.0.0/16"}
	config.ClusterNetwork = []operv1.ClusterNetworkEntry{
		{
			CIDR:       "172.30.0.0/16",
			HostPrefix: 16,
		},
	}
	errExpect("will overlap with cluster network")

	config.ClusterNetwork = []operv1.ClusterNetworkEntry{
		{
			CIDR:       "10.128.0.0/15",
			HostPrefix: 24,
		},
	}
	config.ServiceNetwork = []string{"172.30.0.0/16"}
	config.DefaultNetwork.KuryrConfig.OpenStackServiceNetwork = "172.31.0.0/16"
	errExpect("does not include")

	config.DefaultNetwork.KuryrConfig.OpenStackServiceNetwork = "172.30.0.0/16"
	errExpect("is too small")

	config.DefaultNetwork.KuryrConfig.OpenStackServiceNetwork = "172.30.0.0/15"
	err = validateKuryr(config)
	g.Expect(err).To(BeEmpty())

	mtu := uint32(70000)
	config.DefaultNetwork.KuryrConfig.MTU = &mtu
	errExpect("invalid MTU 70000")
}

func TestFillKuryrDefaults(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := KuryrConfig.DeepCopy()
	conf := &crd.Spec

	c := uint32(8091)
	d := uint32(8090)
	batch := uint(3)
	expected := operv1.NetworkSpec{
		ServiceNetwork: []string{"172.30.0.0/16"},
		ClusterNetwork: []operv1.ClusterNetworkEntry{
			{
				CIDR:       "10.128.0.0/15",
				HostPrefix: 24,
			},
		},
		DefaultNetwork: operv1.DefaultNetworkDefinition{
			Type: operv1.NetworkTypeKuryr,
			KuryrConfig: &operv1.KuryrConfig{
				DaemonProbesPort:             &d,
				ControllerProbesPort:         &c,
				OpenStackServiceNetwork:      "172.30.0.0/15",
				EnablePortPoolsPrepopulation: false,
				PoolMaxPorts:                 0,
				PoolMinPorts:                 1,
				PoolBatchPorts:               &batch,
			},
		},
	}

	fillKuryrDefaults(conf, nil)

	g.Expect(conf).To(Equal(&expected))

}
