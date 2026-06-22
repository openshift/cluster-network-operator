package network

import (
	"strings"
	"testing"

	. "github.com/onsi/gomega"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	cnofake "github.com/openshift/cluster-network-operator/pkg/client/fake"
	"github.com/openshift/cluster-network-operator/pkg/hypershift"
	"github.com/openshift/cluster-network-operator/pkg/names"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
			Type: operv1.NetworkTypeOVNKubernetes,
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

	fakeClient := cnofake.NewFakeClient(
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test1-ignored",
				Labels: map[string]string{
					"openshift.io/cluster-monitoring": "true",
				},
			},
		},
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test2-not-ignored",
			},
		},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: "test3-ignored",
			Labels: map[string]string{
				"openshift.io/cluster-monitoring": "true",
			},
		},
		})
	bootstrapResult := fakeBootstrapResult()

	// disable MultusAdmissionController
	objs, err := renderMultusAdmissionController(config, manifestDir, false, bootstrapResult, fakeClient, getDefaultFeatureGates())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).NotTo(ContainElement(HaveKubernetesID("Deployment", "openshift-multus", "multus-admission-controller")))

	// enable MultusAdmissionController
	enabled := false
	config.DisableMultiNetwork = &enabled
	objs, err = renderMultusAdmissionController(config, manifestDir, false, bootstrapResult, fakeClient, getDefaultFeatureGates())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).To(ContainElement(HaveKubernetesID("Deployment", "openshift-multus", "multus-admission-controller")))

	// Check rendered object
	g.Expect(len(objs)).To(Equal(11))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("Service", "openshift-multus", "multus-admission-controller")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ClusterRole", "", "multus-admission-controller-webhook")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ClusterRoleBinding", "", "multus-admission-controller-webhook")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ValidatingWebhookConfiguration", "", names.MULTUS_VALIDATING_WEBHOOK)))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("Deployment", "openshift-multus", "multus-admission-controller")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("NetworkPolicy", "openshift-multus", "multus-admission-controller")))

	weboookCmd := findMultusWebhookExec(t, objs)
	g.Expect(weboookCmd).To(ContainSubstring("-metrics-listen-address=127.0.0.1:9091"))
	g.Expect(weboookCmd).NotTo(ContainSubstring("-encrypt-metrics"))

	// Test TLS rendering for webhook container
	testTLSArgRendering(t, "multus-admission-controller webhook", "", "", func(t *testing.T, tlsProfile bootstrap.TLSProfile) string {
		testBootstrap := *bootstrapResult
		testBootstrap.TLSProfile = tlsProfile
		objs, err := renderMultusAdmissionController(config, manifestDir, false, &testBootstrap, fakeClient, getDefaultFeatureGates())
		g.Expect(err).NotTo(HaveOccurred())
		return findMultusWebhookExec(t, objs)
	})

	// Test TLS rendering for kube-rbac-proxy container
	// kube-rbac-proxy has hardcoded default ciphers when TLS profile is not honored
	testTLSArgRendering(t, "multus-admission-controller kube-rbac-proxy", "",
		"TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305",
		func(t *testing.T, tlsProfile bootstrap.TLSProfile) string {
			testBootstrap := *bootstrapResult
			testBootstrap.TLSProfile = tlsProfile
			objs, err := renderMultusAdmissionController(config, manifestDir, false, &testBootstrap, fakeClient, getDefaultFeatureGates())
			g.Expect(err).NotTo(HaveOccurred())
			deployment := mustFindRenderedObj[*appsv1.Deployment](t, objs, "Deployment", "multus-admission-controller")
			container := mustFindContainer(t, deployment.Spec.Template.Spec.Containers, "kube-rbac-proxy")
			return strings.Join(container.Args, " ")
		})
}

// TestRenderMultusAdmissionController has some simple rendering tests
func TestRenderMultusAdmissonControllerConfigForHyperShift(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := MultusAdmissionControllerConfig.DeepCopy()
	config := &crd.Spec
	disabled := true
	config.DisableMultiNetwork = &disabled
	fillDefaults(config, nil)

	fakeClient := cnofake.NewFakeClient(
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test1-ignored",
				Labels: map[string]string{
					"openshift.io/cluster-monitoring": "true",
				},
			},
		},
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test2-not-ignored",
			},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "MyCM",
				Namespace: "test1-ignored",
			},
			Data: map[string]string{
				"MyCMKey": "key",
			},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "openshift-service-ca.crt",
				Namespace: "test1-ignored",
			},
			Data: map[string]string{
				"MyCMKey": "key",
			},
		},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: "test3-ignored",
			Labels: map[string]string{
				"openshift.io/cluster-monitoring": "true",
			},
		},
		})
	bootstrapResult := fakeBootstrapResultWithHyperShift()

	hsc := hypershift.NewHyperShiftConfig()
	hsc.Enabled = true
	hsc.CAConfigMap = "MyCM"
	hsc.CAConfigMapKey = "MyCMKey"
	hsc.Name = "MyCluster"
	hsc.Namespace = "test1-ignored"
	hsc.RunAsUser = "1001"
	hsc.ReleaseImage = "MyImage"
	hsc.ControlPlaneImage = "MyCPOImage"

	objs, err := renderMultusAdmissonControllerConfig(manifestDir, false, bootstrapResult, fakeClient, hsc, "", getDefaultFeatureGates())
	g.Expect(err).NotTo(HaveOccurred())

	// Check rendered object
	for _, obj := range objs {
		if obj.GetKind() == "Service" && obj.GetName() == "multus-admission-controller" {
			labels := obj.GetLabels()
			g.Expect(len(labels)).To(Equal(2))
			g.Expect(labels["hypershift.openshift.io/allow-guest-webhooks"]).To(Equal("true"))

			annotations := obj.GetAnnotations()
			g.Expect(len(annotations)).To(Equal(1))
			g.Expect(annotations["network.operator.openshift.io/cluster-name"]).To(Equal("management"))
		}
	}

	weboookCmd := findMultusWebhookExec(t, objs)
	g.Expect(weboookCmd).To(ContainSubstring("-metrics-listen-address=:9091"))
	g.Expect(weboookCmd).To(ContainSubstring("-encrypt-metrics=true"))

	deployment := mustFindMultusAdmissionDeployment(t, objs)
	_, ok := findContainer(deployment.Spec.Template.Spec.Containers, "kube-rbac-proxy")
	g.Expect(ok).To(BeFalse(), "Found unexpected container \"kube-rbac-proxy\"")

	// Test TLS rendering for webhook container in HyperShift mode
	testTLSArgRendering(t, "multus-admission-controller webhook (HyperShift)", "", "", func(t *testing.T, tlsProfile bootstrap.TLSProfile) string {
		testBootstrap := *bootstrapResult
		testBootstrap.TLSProfile = tlsProfile
		objs, err := renderMultusAdmissonControllerConfig(manifestDir, false, &testBootstrap, fakeClient, hsc, "", getDefaultFeatureGates())
		g.Expect(err).NotTo(HaveOccurred())
		return findMultusWebhookExec(t, objs)
	})
}

// TestRenderMultusAdmissionControllerGetNamespace tests getOpenshiftNamespaces()
func TestRenderMultusAdmissionControllerGetNamespace(t *testing.T) {
	g := NewGomegaWithT(t)

	fakeClient := cnofake.NewFakeClient(
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test1-ignored",
				Labels: map[string]string{
					"openshift.io/cluster-monitoring": "true",
				},
				Annotations: map[string]string{
					"workload.openshift.io/allowed": "management",
				},
			},
		},
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test2-not-ignored",
			},
		},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: "test3-ignored",
			Labels: map[string]string{
				"openshift.io/cluster-monitoring": "true",
			},
			Annotations: map[string]string{
				"workload.openshift.io/allowed": "management",
			},
		},
		})
	namespaces, err := getOpenshiftNamespaces(fakeClient)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(namespaces).To(Equal("test1-ignored,test3-ignored"))
}

func mustFindMultusAdmissionDeployment(t *testing.T, objs []*unstructured.Unstructured) *appsv1.Deployment {
	return mustFindRenderedObj[*appsv1.Deployment](t, objs, "Deployment", "multus-admission-controller")
}

func findMultusWebhookExec(t *testing.T, objs []*unstructured.Unstructured) string {
	t.Helper()

	deployment := mustFindMultusAdmissionDeployment(t, objs)
	cmdArgs := mustFindContainer(t, deployment.Spec.Template.Spec.Containers, "multus-admission-controller").Command
	return findExecCommand(t, cmdArgs, "webhook")
}
