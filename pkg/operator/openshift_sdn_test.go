package operator

import (
	"testing"

	netv1 "github.com/openshift/cluster-network-operator/pkg/apis/networkoperator/v1"

	. "github.com/onsi/gomega"
)

var OpenshiftSDNConfig = netv1.NetworkConfig{
	Spec: netv1.NetworkConfigSpec{
		ServiceNetwork: "172.30.0.0/16",
		ClusterNetworks: []netv1.ClusterNetwork{
			{
				CIDR:             "10.128.0.0/15",
				HostSubnetLength: 9,
			},
			{
				CIDR:             "10.0.0.0/14",
				HostSubnetLength: 8,
			},
		},
		DefaultNetwork: netv1.DefaultNetworkDefinition{
			Type: netv1.NetworkTypeOpenshiftSDN,
			OpenshiftSDNConfig: &netv1.OpenshiftSDNConfig{
				Mode: netv1.SDNModePolicy,
			},
		},
	},
}

var manifestDir = "../../bindata"

// TestRenderOpenshiftSDN has some simple rendering tests
func TestRenderOpenshiftSDN(t *testing.T) {
	g := NewGomegaWithT(t)

	h := Handler{
		config:      OpenshiftSDNConfig.DeepCopy(),
		ManifestDir: manifestDir,
	}
	config := h.config.Spec
	sdnConfig := config.DefaultNetwork.OpenshiftSDNConfig

	errs := h.validateOpenshiftSDN()
	g.Expect(errs).To(HaveLen(0))

	// Make sure the OVS daemonset isn't created
	truth := true
	sdnConfig.UseExternalOpenvswitch = &truth
	objs, err := h.renderOpenshiftSDN(sdnConfig)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).NotTo(ContainElement(HaveKubernetesID("DaemonSet", "openshift-sdn", "ovs")))

	// enable openvswitch
	sdnConfig.UseExternalOpenvswitch = nil
	objs, err = h.renderOpenshiftSDN(sdnConfig)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-sdn", "ovs")))

	// It's important that the namespace is before any namespaced types;
	// for now, just test that it's the second item in the list, after
	// the ClusterNetwork.
	g.Expect(objs[0]).To(HaveKubernetesID("ClusterNetwork", "", "default"))
	g.Expect(objs[1]).To(HaveKubernetesID("Namespace", "", "openshift-sdn"))

	g.Expect(objs).To(ContainElement(HaveKubernetesID("ClusterRole", "", "openshift-sdn")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ServiceAccount", "openshift-sdn", "sdn")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ClusterRoleBinding", "", "sdn")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-sdn", "sdn")))

}

func TestValidateOpenshiftSDN(t *testing.T) {
	g := NewGomegaWithT(t)

	h := Handler{
		config:      OpenshiftSDNConfig.DeepCopy(),
		ManifestDir: manifestDir,
	}
	config := &h.config.Spec
	sdnconfig := config.DefaultNetwork.OpenshiftSDNConfig

	err := h.validateOpenshiftSDN()
	g.Expect(err).To(BeEmpty())

	errExpect := func(substr string) {
		t.Helper()
		g.Expect(h.validateOpenshiftSDN()).To(
			ContainElement(MatchError(
				ContainSubstring(substr))))
	}

	// set mtu to insanity
	mtu := uint32(70000)
	sdnconfig.MTU = &mtu
	errExpect("invalid MTU 70000")

	sdnconfig.Mode = "broken"
	errExpect("invalid openshift-sdn mode \"broken\"")

	port := uint32(66666)
	sdnconfig.VXLANPort = &port
	errExpect("invalid VXLANPort 66666")

	config.ClusterNetworks = nil
	errExpect("ClusterNetworks cannot be empty")
}
