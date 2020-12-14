package network

import (
	"testing"

	. "github.com/onsi/gomega"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/apply"
)

var MultiNetworkPolicyConfig = operv1.Network{
	Spec: operv1.NetworkSpec{
		ServiceNetwork: []string{"172.30.0.0/16"},
		ClusterNetwork: []operv1.ClusterNetworkEntry{
			{
				CIDR:       "10.128.0.0/15",
				HostPrefix: 23,
			},
		},
		DefaultNetwork: operv1.DefaultNetworkDefinition{
			Type: operv1.NetworkTypeOpenShiftSDN,
			OpenShiftSDNConfig: &operv1.OpenShiftSDNConfig{
				Mode: operv1.SDNModeNetworkPolicy,
			},
		},
	},
}

// TestRenderMultiNetworkPolicy has some simple rendering tests
func TestRenderMultiNetworkPolicy(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := MultusAdmissionControllerConfig.DeepCopy()
	config := &crd.Spec
	disabled := false
	config.UseMultiNetworkPolicy = &disabled
	FillDefaults(config, nil)

	// disable MultiNetworkPolicy
	objs, err := renderMultiNetworkpolicy(config, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).NotTo(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "multus-networkpolicy")))

	// enable MultiNetworkPolicy
	enabled := true
	config.UseMultiNetworkPolicy = &enabled
	objs, err = renderMultiNetworkpolicy(config, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "multus-networkpolicy")))

	// Check rendered object
	g.Expect(len(objs)).To(Equal(4))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("CustomResourceDefinition", "", "multi-networkpolicies.k8s.cni.cncf.io")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ClusterRole", "", "openshift-multus-networkpolicy")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ClusterRoleBinding", "", "openshift-multus-networkpolicy")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "multus-networkpolicy")))

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
