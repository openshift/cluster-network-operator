package network

import (
	"testing"

	netv1 "github.com/openshift/cluster-network-operator/pkg/apis/networkoperator/v1"
	"github.com/openshift/cluster-network-operator/pkg/apply"

	. "github.com/onsi/gomega"
)

var MultusConfig = netv1.NetworkConfig{
	Spec: netv1.NetworkConfigSpec{
		ServiceNetwork: "172.30.0.0/16",
		ClusterNetworks: []netv1.ClusterNetwork{
			{
				CIDR:             "10.128.0.0/15",
				HostSubnetLength: 9,
			},
		},
		DefaultNetwork: netv1.DefaultNetworkDefinition{
			Type: netv1.NetworkTypeOpenShiftSDN,
			OpenShiftSDNConfig: &netv1.OpenShiftSDNConfig{
				Mode: netv1.SDNModeNetworkPolicy,
			},
		},
	},
}

// TestRenderMultus has some simple rendering tests
func TestRenderMultus(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := MultusConfig.DeepCopy()
	config := &crd.Spec
	disabled := true
	config.DisableMultiNetwork = &disabled
	FillDefaults(config, nil)

	// disable Multus
	objs, err := RenderMultus(config, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).NotTo(ContainElement(HaveKubernetesID("DaemonSet", "multus", "multus")))

	// enable Multus
	enabled := false
	config.DisableMultiNetwork = &enabled
	objs, err = RenderMultus(config, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "multus", "multus")))

	// It's important that the namespace is first
	g.Expect(len(objs)).To(Equal(6))
	g.Expect(objs[0]).To(HaveKubernetesID("CustomResourceDefinition", "", "network-attachment-definitions.k8s.cni.cncf.io"))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("Namespace", "", "multus")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ClusterRole", "", "multus")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ServiceAccount", "multus", "multus")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ClusterRoleBinding", "", "multus")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "multus", "multus")))

	// Make sure every obj is reasonable:
	// - it is supported
	// - it reconciles to itself (steady state)
	for _, obj := range objs {
		g.Expect(apply.IsObjectSupported(obj)).NotTo(HaveOccurred())
		cur := obj.DeepCopy()
		upd := obj.DeepCopy()

		err = apply.MergeObjectForUpdate(cur, upd)
		g.Expect(err).NotTo(HaveOccurred())

		tweakMetaForCompare(cur)
		g.Expect(cur).To(Equal(upd))
	}
}
