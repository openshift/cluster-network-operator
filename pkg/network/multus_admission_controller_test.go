package network

import (
	"testing"

	. "github.com/onsi/gomega"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/names"
)

var MultusAdmissionControllerConfig = operv1.Network{
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

// TestRenderMultusAdmissionController has some simple rendering tests
func TestRenderMultusAdmissionController(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := MultusAdmissionControllerConfig.DeepCopy()
	config := &crd.Spec
	disabled := true
	config.DisableMultiNetwork = &disabled
	fillDefaults(config, nil)

	// disable MultusAdmissionController
	objs, err := renderMultusAdmissionController(config, manifestDir, false)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).NotTo(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "multus-admission-controller")))

	// enable MultusAdmissionController
	enabled := false
	config.DisableMultiNetwork = &enabled
	objs, err = renderMultusAdmissionController(config, manifestDir, false)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "multus-admission-controller")))

	// Check rendered object
	g.Expect(len(objs)).To(Equal(9))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("Service", "openshift-multus", "multus-admission-controller")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ClusterRole", "", "multus-admission-controller-webhook")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ClusterRoleBinding", "", "multus-admission-controller-webhook")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ValidatingWebhookConfiguration", "", names.MULTUS_VALIDATING_WEBHOOK)))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "multus-admission-controller")))
}
