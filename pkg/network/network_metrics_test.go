package network

import (
	"strings"
	"testing"

	. "github.com/onsi/gomega"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	appsv1 "k8s.io/api/apps/v1"
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
			Type: operv1.NetworkTypeOVNKubernetes,
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
	fillDefaults(config, nil)

	// disable MultusAdmissionController
	objs, err := renderMultus(config, fakeBootstrapResult(), manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).NotTo(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "network-metrics-daemon")))

	// enable MultusAdmissionController
	enabled := false
	config.DisableMultiNetwork = &enabled
	objs, err = renderMultus(config, fakeBootstrapResult(), manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "network-metrics-daemon")))

	// Check rendered object

	g.Expect(len(objs)).To(Equal(34), "Expected 34 multus related objects")
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "network-metrics-daemon")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("Service", "openshift-multus", "network-metrics-service")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ClusterRole", "", "metrics-daemon-role")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ClusterRoleBinding", "", "metrics-daemon-sa-rolebinding")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ServiceMonitor", "openshift-multus", "monitor-network")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("Role", "openshift-multus", "prometheus-k8s")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("RoleBinding", "openshift-multus", "prometheus-k8s")))

	// Test TLS rendering for kube-rbac-proxy container
	testTLSArgRendering(t, "network-metrics kube-rbac-proxy", "",
		"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256",
		func(t *testing.T, tlsProfile bootstrap.TLSProfile) string {
			testBootstrap := fakeBootstrapResult()
			testBootstrap.TLSProfile = tlsProfile
			objs, err := renderMultus(config, testBootstrap, manifestDir)
			g.Expect(err).NotTo(HaveOccurred())

			daemonSet := mustFindRenderedObj[*appsv1.DaemonSet](t, objs, "DaemonSet", "network-metrics-daemon")
			return strings.Join(mustFindContainer(t, daemonSet.Spec.Template.Spec.Containers, "kube-rbac-proxy").Args, " ")
		})
}
