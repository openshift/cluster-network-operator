package network

import (
	"testing"

	operv1 "github.com/openshift/api/operator/v1"

	. "github.com/onsi/gomega"
)

var MultusConfig = operv1.Network{
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

// TestRenderMultus has some simple rendering tests
func TestRenderMultus(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := MultusConfig.DeepCopy()
	config := &crd.Spec
	disabled := true
	config.DisableMultiNetwork = &disabled
	fillDefaults(config, nil)

	// disable Multus
	objs, err := renderMultus(config, fakeBootstrapResult(), manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).NotTo(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "multus")))

	// enable Multus
	enabled := false
	config.DisableMultiNetwork = &enabled
	objs, err = renderMultus(config, fakeBootstrapResult(), manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "multus")))

	// It's important that the namespace is first
	g.Expect(len(objs)).To(Equal(23), "Expected 23 multus related objects")
	g.Expect(objs[0]).To(HaveKubernetesID("CustomResourceDefinition", "", "network-attachment-definitions.k8s.cni.cncf.io"))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("Namespace", "", "openshift-multus")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ClusterRole", "", "multus")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ServiceAccount", "openshift-multus", "multus")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ClusterRoleBinding", "", "multus")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "multus")))
}
