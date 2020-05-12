package network

import (
	"testing"

	. "github.com/onsi/gomega"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/apply"
)

var NetworkMetricsDaemonConfig = operv1.Network{
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

// TestRenderNetworkMetricsDaemon has some simple rendering tests
func TestRenderNetworkMetricsDaemon(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := NetworkMetricsDaemonConfig.DeepCopy()
	config := &crd.Spec
	disabled := true
	config.DisableMultiNetwork = &disabled
	FillDefaults(config, nil)

	// disable MultusAdmissionController
	objs, err := RenderMultus(config, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).NotTo(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "network-metrics-daemon")))

	// enable MultusAdmissionController
	enabled := false
	config.DisableMultiNetwork = &enabled
	objs, err = RenderMultus(config, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "network-metrics-daemon")))

	// Check rendered object

	g.Expect(len(objs)).To(Equal(18))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "network-metrics-daemon")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("Service", "openshift-multus", "network-metrics-service")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ClusterRole", "", "metrics-daemon-role")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ClusterRoleBinding", "", "metrics-daemon-sa-rolebinding")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ServiceMonitor", "openshift-multus", "monitor-network")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("Role", "openshift-multus", "prometheus-k8s")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("RoleBinding", "openshift-multus", "prometheus-k8s")))

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
