package observability

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Helper functions for creating test resources

func createTestNetwork(name string, value string) *configv1.Network {
	network := &configv1.Network{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}

	if value != "" {
		network.Spec.NetworkObservability = configv1.NetworkObservabilitySpec{
			InstallationPolicy: configv1.NetworkObservabilityInstallationPolicy(value),
		}
	}

	return network
}

func createTestOperatorNetwork(name string) *operatorv1.Network {
	return &operatorv1.Network{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
}

func createTestOperatorNetworkWithDeployedCondition(name string) *operatorv1.Network {
	return &operatorv1.Network{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Status: operatorv1.NetworkStatus{
			OperatorStatus: operatorv1.OperatorStatus{
				Conditions: []operatorv1.OperatorCondition{
					{
						Type:               NetworkObservabilityDeployed,
						Status:             operatorv1.ConditionTrue,
						Reason:             "DeploymentComplete",
						Message:            "Network Observability has been deployed",
						LastTransitionTime: metav1.Now(),
					},
				},
			},
		},
	}
}

func createTestFlowCollector(name string) *unstructured.Unstructured {
	fc := &unstructured.Unstructured{}
	fc.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "flows.netobserv.io",
		Version: FlowCollectorVersion,
		Kind:    "FlowCollector",
	})
	fc.SetName(name)
	return fc
}

func createTestNamespace(name string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
}

func createTestInfrastructure(topology configv1.TopologyMode) *configv1.Infrastructure {
	return &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
		Status: configv1.InfrastructureStatus{
			ControlPlaneTopology: topology,
		},
	}
}

func createTestCRD(name string) *unstructured.Unstructured {
	crd := &unstructured.Unstructured{}
	crd.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "apiextensions.k8s.io",
		Version: "v1",
		Kind:    "CustomResourceDefinition",
	})
	crd.SetName(name)
	return crd
}

func createTestClusterExtension(t *testing.T, name string, installed bool) *unstructured.Unstructured {
	t.Helper()
	ce := &unstructured.Unstructured{}
	ce.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "olm.operatorframework.io",
		Version: "v1",
		Kind:    "ClusterExtension",
	})
	ce.SetName(name)

	// Set status conditions
	conditions := []interface{}{
		map[string]interface{}{
			"type": "Installed",
			"status": func() string {
				if installed {
					return "True"
				}
				return "False"
			}(),
			"reason":  "InstallSucceeded",
			"message": "ClusterExtension installed successfully",
		},
	}
	if err := unstructured.SetNestedSlice(ce.Object, conditions, "status", "conditions"); err != nil {
		t.Fatalf("Failed to set status conditions: %v", err)
	}
	return ce
}

func createTestCSV(name string, succeeded bool) *unstructured.Unstructured {
	csv := &unstructured.Unstructured{}
	csv.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "operators.coreos.com",
		Version: "v1alpha1",
		Kind:    "ClusterServiceVersion",
	})
	csv.SetName(name + ".v1.0.0")
	csv.SetNamespace(OperatorNamespace)

	// Set status phase
	phase := "Succeeded"
	if !succeeded {
		phase = "Failed"
	}
	_ = unstructured.SetNestedField(csv.Object, phase, "status", "phase")

	return csv
}

func createTempManifest(t *testing.T, content string) string {
	t.Helper()
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "manifest.yaml")
	err := os.WriteFile(filePath, []byte(content), 0644)
	if err != nil {
		t.Fatalf("Failed to create temp manifest: %v", err)
	}
	return filePath
}

// Helper function to create a feature gate with NetworkObservabilityInstall enabled
func createEnabledFeatureGate() featuregates.FeatureGate {
	return featuregates.NewFeatureGate(
		[]configv1.FeatureGateName{"NetworkObservabilityInstall"},
		[]configv1.FeatureGateName{},
	)
}

// Test shouldInstallNetworkObservability()

func TestShouldInstallNetworkObservability_NilNonSNO(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add configv1 to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add corev1 to scheme: %v", err)
	}
	if err := operatorv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add operatorv1 to scheme: %v", err)
	}

	network := &configv1.Network{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec:       configv1.NetworkSpec{
			// NetworkObservability not set: Default behavior should install on non-SNO
		},
	}
	operatorNetwork := createTestOperatorNetwork("cluster")
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(network, operatorNetwork, infra).Build()
	r := &ReconcileObservability{client: client}

	result, err := r.shouldInstallNetworkObservability(context.TODO())

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(BeTrue())
}

func TestShouldInstallNetworkObservability_NilSNO(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add configv1 to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add corev1 to scheme: %v", err)
	}
	if err := operatorv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add operatorv1 to scheme: %v", err)
	}

	network := &configv1.Network{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec:       configv1.NetworkSpec{
			// NetworkObservability not set: Default behavior should NOT install on SNO
		},
	}
	operatorNetwork := createTestOperatorNetwork("cluster")
	infra := createTestInfrastructure(configv1.SingleReplicaTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(network, operatorNetwork, infra).Build()
	r := &ReconcileObservability{client: client}

	result, err := r.shouldInstallNetworkObservability(context.TODO())

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(BeFalse())
}

func TestShouldInstallNetworkObservability_ExplicitInstallAndEnableNonSNO(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add configv1 to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add corev1 to scheme: %v", err)
	}
	if err := operatorv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add operatorv1 to scheme: %v", err)
	}

	network := &configv1.Network{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: configv1.NetworkSpec{
			NetworkObservability: configv1.NetworkObservabilitySpec{
				InstallationPolicy: configv1.NetworkObservabilityInstallAndEnable,
			},
		},
	}
	operatorNetwork := createTestOperatorNetwork("cluster")
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(network, operatorNetwork, infra).Build()
	r := &ReconcileObservability{client: client}

	result, err := r.shouldInstallNetworkObservability(context.TODO())

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(BeTrue())
}

func TestShouldInstallNetworkObservability_ExplicitInstallAndEnableSNO(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add configv1 to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add corev1 to scheme: %v", err)
	}
	if err := operatorv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add operatorv1 to scheme: %v", err)
	}

	network := &configv1.Network{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: configv1.NetworkSpec{
			NetworkObservability: configv1.NetworkObservabilitySpec{
				InstallationPolicy: configv1.NetworkObservabilityInstallAndEnable, // Explicit InstallAndEnable: install even on SNO
			},
		},
	}
	operatorNetwork := createTestOperatorNetwork("cluster")
	infra := createTestInfrastructure(configv1.SingleReplicaTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(network, operatorNetwork, infra).Build()
	r := &ReconcileObservability{client: client}

	result, err := r.shouldInstallNetworkObservability(context.TODO())

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(BeTrue())
}

func TestShouldInstallNetworkObservability_ExplicitNoAction(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add configv1 to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add corev1 to scheme: %v", err)
	}
	if err := operatorv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add operatorv1 to scheme: %v", err)
	}

	network := &configv1.Network{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: configv1.NetworkSpec{
			NetworkObservability: configv1.NetworkObservabilitySpec{
				InstallationPolicy: configv1.NetworkObservabilityNoAction,
			},
		},
	}
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(network, infra).Build()
	r := &ReconcileObservability{client: client}

	result, err := r.shouldInstallNetworkObservability(context.TODO())

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(BeFalse())
}

func TestShouldInstallNetworkObservability_EmptyStringNonSNO(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add configv1 to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add corev1 to scheme: %v", err)
	}
	if err := operatorv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add operatorv1 to scheme: %v", err)
	}

	network := &configv1.Network{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: configv1.NetworkSpec{
			NetworkObservability: configv1.NetworkObservabilitySpec{
				InstallationPolicy: "", // Empty string: default behavior (install on non-SNO)
			},
		},
	}
	operatorNetwork := createTestOperatorNetwork("cluster")
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(network, operatorNetwork, infra).Build()
	r := &ReconcileObservability{client: client}

	result, err := r.shouldInstallNetworkObservability(context.TODO())

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(BeTrue())
}

// Test isSingleNodeCluster()

func TestIsSingleNodeCluster_SNO(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add configv1 to scheme: %v", err)
	}

	infra := createTestInfrastructure(configv1.SingleReplicaTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(infra).Build()
	r := &ReconcileObservability{client: client}

	isSNO, err := r.isSingleNodeCluster(context.TODO())

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(isSNO).To(BeTrue())
}

func TestIsSingleNodeCluster_HighlyAvailable(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add configv1 to scheme: %v", err)
	}

	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(infra).Build()
	r := &ReconcileObservability{client: client}

	isSNO, err := r.isSingleNodeCluster(context.TODO())

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(isSNO).To(BeFalse())
}

// Test Reconcile() - Main Controller Logic

func TestReconcile_IgnoresNonClusterNetwork(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add configv1 to scheme: %v", err)
	}

	network := createTestNetwork("not-cluster", "InstallAndEnable")
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(network).Build()

	r := &ReconcileObservability{
		client: client,
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "not-cluster"}}
	result, err := r.Reconcile(context.TODO(), req)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(Equal(reconcile.Result{}))
}

func TestReconcile_SkipsWhenDisabled(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add configv1 to scheme: %v", err)
	}

	// Explicitly set to false (opt-out)
	network := createTestNetwork("cluster", "NoAction")
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(network).Build()

	r := &ReconcileObservability{
		client:      client,
		featureGate: createEnabledFeatureGate(),
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}
	result, err := r.Reconcile(context.TODO(), req)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(Equal(reconcile.Result{}))
}

func TestReconcile_InstallsWhenNil(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add configv1 to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add corev1 to scheme: %v", err)
	}
	if err := operatorv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add operatorv1 to scheme: %v", err)
	}

	// Create network with no NetworkObservability field (defaults to enabled on non-SNO)
	network := &configv1.Network{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
		Spec: configv1.NetworkSpec{
			// NetworkObservability not set: defaults to enabled on non-SNO
		},
	}
	operatorNetwork := createTestOperatorNetwork("cluster")
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)
	operatorNs := createTestNamespace(OperatorNamespace)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(network, operatorNetwork, infra, operatorNs).Build()

	r := &ReconcileObservability{
		client:      client,
		featureGate: createEnabledFeatureGate(),
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}
	result, err := r.Reconcile(context.TODO(), req)

	// When nil, controller should try to install (opt-out behavior)
	// This will fail because the manifest doesn't exist, but it requeues instead of erroring
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.RequeueAfter).To(Equal(requeueAfterOLM))
}

func TestReconcile_SkipsInstallWhenNilOnSNO(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add configv1 to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add corev1 to scheme: %v", err)
	}
	if err := operatorv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add operatorv1 to scheme: %v", err)
	}

	// Create network with no NetworkObservability field on SNO cluster
	network := &configv1.Network{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
		Spec: configv1.NetworkSpec{
			// NetworkObservability not set: defaults to disabled on SNO
		},
	}
	operatorNetwork := createTestOperatorNetwork("cluster")
	infra := createTestInfrastructure(configv1.SingleReplicaTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(network, operatorNetwork, infra).Build()

	r := &ReconcileObservability{
		client:      client,
		featureGate: createEnabledFeatureGate(),
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}
	result, err := r.Reconcile(context.TODO(), req)

	// On SNO with nil, controller should skip installation
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(Equal(reconcile.Result{}))

	// Verify that the operator namespace was NOT created
	ns := &corev1.Namespace{}
	nsErr := client.Get(context.TODO(), types.NamespacedName{Name: OperatorNamespace}, ns)
	g.Expect(nsErr).To(HaveOccurred())
	g.Expect(nsErr.Error()).To(ContainSubstring("not found"))
}

func TestReconcile_IgnoresNotFound(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add configv1 to scheme: %v", err)
	}

	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &ReconcileObservability{
		client: client,
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}
	result, err := r.Reconcile(context.TODO(), req)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(Equal(reconcile.Result{}))
}

// Test isNetObservOperatorInstalled()

func TestIsNetObservOperatorInstalled_True(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()

	crd := createTestCRD("flowcollectors.flows.netobserv.io")
	clusterExtension := createTestClusterExtension(t, "netobserv-operator", true)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(crd, clusterExtension).Build()

	r := &ReconcileObservability{client: client}

	installed, ceExists, err := r.isNetObservOperatorInstalled(context.TODO())

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(installed).To(BeTrue())
	g.Expect(ceExists).To(BeTrue())
}

func TestIsNetObservOperatorInstalled_False(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()

	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &ReconcileObservability{client: client}

	installed, ceExists, err := r.isNetObservOperatorInstalled(context.TODO())

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(installed).To(BeFalse())
	g.Expect(ceExists).To(BeFalse())
}

func TestIsNetObservOperatorInstalled_Multiple(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()

	// Create multiple CRDs, but only the FlowCollector one should matter
	crd1 := createTestCRD("other-crds.example.com")
	crd2 := createTestCRD("flowcollectors.flows.netobserv.io")
	clusterExtension := createTestClusterExtension(t, "netobserv-operator", true)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(crd1, crd2, clusterExtension).Build()

	r := &ReconcileObservability{client: client}

	installed, ceExists, err := r.isNetObservOperatorInstalled(context.TODO())

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(installed).To(BeTrue())
	g.Expect(ceExists).To(BeTrue())
}

func TestIsNetObservOperatorInstalled_CRDExistsButNoOLM(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()

	// Create only CRD, no ClusterExtension or CSV
	crd := createTestCRD("flowcollectors.flows.netobserv.io")

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(crd).Build()

	r := &ReconcileObservability{client: client}

	installed, ceExists, err := r.isNetObservOperatorInstalled(context.TODO())

	// Should return error because CRD exists but no OLM installation found
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("FlowCollector CRD is present but could not identify"))
	g.Expect(installed).To(BeFalse())
	g.Expect(ceExists).To(BeFalse())
}

func TestIsNetObservOperatorInstalled_OLMv1InstallationFailed(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()

	crd := createTestCRD("flowcollectors.flows.netobserv.io")

	// Create ClusterExtension with Installed=False (installation failed)
	ce := &unstructured.Unstructured{}
	ce.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "olm.operatorframework.io",
		Version: "v1",
		Kind:    "ClusterExtension",
	})
	ce.SetName("netobserv-operator")
	conditions := []interface{}{
		map[string]interface{}{
			"type":    "Installed",
			"status":  "False",
			"reason":  "InstallationFailed",
			"message": "Failed to install operator bundle",
		},
	}
	if err := unstructured.SetNestedSlice(ce.Object, conditions, "status", "conditions"); err != nil {
		t.Fatalf("Failed to set status conditions: %v", err)
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(crd, ce).Build()

	r := &ReconcileObservability{client: client}

	installed, ceExists, err := r.isNetObservOperatorInstalled(context.TODO())

	// Should return error because installation failed
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("OLMv1 installation error"))
	g.Expect(err.Error()).To(ContainSubstring("ClusterExtension installation failed"))
	g.Expect(installed).To(BeFalse())
	g.Expect(ceExists).To(BeTrue())
}

func TestIsNetObservOperatorInstalled_OLMv1NotInstalledYet(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()

	crd := createTestCRD("flowcollectors.flows.netobserv.io")

	// Create ClusterExtension with Installed=Unknown (not installed yet)
	ce := &unstructured.Unstructured{}
	ce.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "olm.operatorframework.io",
		Version: "v1",
		Kind:    "ClusterExtension",
	})
	ce.SetName("netobserv-operator")
	conditions := []interface{}{
		map[string]interface{}{
			"type":    "Installed",
			"status":  "Unknown",
			"reason":  "Installing",
			"message": "Installing operator bundle",
		},
	}
	if err := unstructured.SetNestedSlice(ce.Object, conditions, "status", "conditions"); err != nil {
		t.Fatalf("Failed to set status conditions: %v", err)
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(crd, ce).Build()

	r := &ReconcileObservability{client: client}

	installed, ceExists, err := r.isNetObservOperatorInstalled(context.TODO())

	// Should return an error because the controller could not identify how NOO was installed
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("could not identify how Network Observability Operator was installed"))
	g.Expect(installed).To(BeFalse())
	g.Expect(ceExists).To(BeTrue())
}

func TestIsNetObservOperatorInstalled_CRDMissingButOLMv1Present(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()

	// No CRD, but ClusterExtension exists
	clusterExtension := createTestClusterExtension(t, "netobserv-operator", true)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(clusterExtension).Build()

	r := &ReconcileObservability{client: client}

	installed, ceExists, err := r.isNetObservOperatorInstalled(context.TODO())

	// Should return error because operator was deployed but CRD is missing
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("network Observability Operator was deployed via OLMv1 but FlowCollector CRD is missing"))
	g.Expect(installed).To(BeFalse())
	g.Expect(ceExists).To(BeTrue())
}

func TestIsNetObservOperatorInstalled_CRDMissingButOLMv0Present(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()

	// No CRD, but CSV exists
	csv := createTestCSV("netobserv-operator", true)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(csv).Build()

	r := &ReconcileObservability{client: client}

	installed, ceExists, err := r.isNetObservOperatorInstalled(context.TODO())

	// Should return error because operator was deployed but CRD is missing
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("network Observability Operator was deployed via OLMv0 but FlowCollector CRD is missing"))
	g.Expect(installed).To(BeFalse())
	g.Expect(ceExists).To(BeFalse())
}

// Test isFlowCollectorExists()

func TestIsFlowCollectorExists_True(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()

	flowCollector := createTestFlowCollector(FlowCollectorName)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(flowCollector).Build()

	r := &ReconcileObservability{client: client}

	exists, err := r.isFlowCollectorExists(context.TODO())

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(exists).To(BeTrue())
}

func TestIsFlowCollectorExists_False(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()

	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &ReconcileObservability{client: client}

	exists, err := r.isFlowCollectorExists(context.TODO())

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(exists).To(BeFalse())
}

func TestIsFlowCollectorExists_OnlyChecksCluster(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()

	// Create a FlowCollector with a different name
	fcOther := createTestFlowCollector("other")

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(fcOther).Build()

	r := &ReconcileObservability{client: client}

	exists, err := r.isFlowCollectorExists(context.TODO())

	// Should return false because we only check for "cluster"
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(exists).To(BeFalse())
}

// Test createFlowCollector() - Note: Full testing requires real manifest files

func TestCreateFlowCollector_ManifestNotFound(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add corev1 to scheme: %v", err)
	}

	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &ReconcileObservability{client: client}

	// Test with non-existent manifest by calling applyManifest directly
	err := r.applyManifest(context.TODO(), "/non/existent/path.yaml", "test")

	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("failed to read"))
}

// Test installNetObservOperator()

func TestInstallNetObservOperator_ManifestNotFound(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()

	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &ReconcileObservability{client: client}

	// Test applyManifest with non-existent path directly
	err := r.applyManifest(context.TODO(), "/non/existent/operator.yaml", "Network Observability Operator")

	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("failed to read"))
}

// Test applyManifest()

func TestApplyManifest_SingleResource(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add corev1 to scheme: %v", err)
	}

	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &ReconcileObservability{client: client}

	manifestContent := `
apiVersion: v1
kind: Namespace
metadata:
  name: test-namespace
`
	manifestPath := createTempManifest(t, manifestContent)

	err := r.applyManifest(context.TODO(), manifestPath, "test resource")

	g.Expect(err).NotTo(HaveOccurred())
}

func TestApplyManifest_MultipleResources(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()

	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &ReconcileObservability{client: client}

	manifestContent := `
apiVersion: v1
kind: Namespace
metadata:
  name: test-namespace-1
---
apiVersion: v1
kind: Namespace
metadata:
  name: test-namespace-2
`
	manifestPath := createTempManifest(t, manifestContent)

	err := r.applyManifest(context.TODO(), manifestPath, "test resources")

	g.Expect(err).NotTo(HaveOccurred())
}

func TestApplyManifest_EmptyDocuments(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()

	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &ReconcileObservability{client: client}

	manifestContent := `---
---
`
	manifestPath := createTempManifest(t, manifestContent)

	err := r.applyManifest(context.TODO(), manifestPath, "empty resources")

	// Should not error on empty documents
	g.Expect(err).NotTo(HaveOccurred())
}

func TestApplyManifest_InvalidYAML(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()

	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &ReconcileObservability{client: client}

	manifestContent := `
invalid: yaml: content:
  - broken
    indentation
`
	manifestPath := createTempManifest(t, manifestContent)

	err := r.applyManifest(context.TODO(), manifestPath, "invalid resource")

	g.Expect(err).To(HaveOccurred())
}

// Integration Tests

// TestReconcile_SkipsFlowCollectorWhenExists tests that reconciliation
// doesn't try to create FlowCollector if it already exists
func TestReconcile_SkipsFlowCollectorWhenExists(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add configv1 to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add corev1 to scheme: %v", err)
	}
	if err := operatorv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add operatorv1 to scheme: %v", err)
	}

	network := createTestNetwork("cluster", "InstallAndEnable")
	operatorNetwork := createTestOperatorNetwork("cluster")
	crd := createTestCRD("flowcollectors.flows.netobserv.io")
	clusterExtension := createTestClusterExtension(t, "netobserv-operator", true)
	flowCollector := createTestFlowCollector(FlowCollectorName)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, operatorNetwork, crd, clusterExtension, flowCollector).
		WithStatusSubresource(&operatorv1.Network{}).
		Build()

	r := &ReconcileObservability{
		client:      client,
		featureGate: createEnabledFeatureGate(),
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}
	result, err := r.Reconcile(context.TODO(), req)

	// Should complete without error
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(Equal(reconcile.Result{}))
}

// TestReconcile_SkipsInstallWhenExists tests that reconciliation
// doesn't try to install operator if it already exists
func TestReconcile_SkipsInstallWhenExists(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add configv1 to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add corev1 to scheme: %v", err)
	}

	network := createTestNetwork("cluster", "InstallAndEnable")
	crd := createTestCRD("flowcollectors.flows.netobserv.io")
	clusterExtension := createTestClusterExtension(t, "netobserv-operator", true)
	operatorNs := createTestNamespace(OperatorNamespace)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, crd, clusterExtension, operatorNs).
		Build()

	r := &ReconcileObservability{
		client:      client,
		featureGate: createEnabledFeatureGate(),
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// Since operator is already installed, it should proceed to FlowCollector creation
	// which will fail (manifest doesn't exist) but will requeue instead of erroring
	result, err := r.Reconcile(context.TODO(), req)

	// We expect no error, just a requeue
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.RequeueAfter).To(Equal(requeueAfterStandard))
}

// Edge Case Tests

// TestReconcile_MultipleInvocations tests that multiple reconciliations
// handle idempotency correctly
func TestReconcile_MultipleInvocations(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add configv1 to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add corev1 to scheme: %v", err)
	}
	if err := operatorv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add operatorv1 to scheme: %v", err)
	}

	network := createTestNetwork("cluster", "InstallAndEnable")
	operatorNetwork := createTestOperatorNetwork("cluster")
	crd := createTestCRD("flowcollectors.flows.netobserv.io")
	clusterExtension := createTestClusterExtension(t, "netobserv-operator", true)
	flowCollector := createTestFlowCollector(FlowCollectorName)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, operatorNetwork, crd, clusterExtension, flowCollector).
		WithStatusSubresource(&operatorv1.Network{}).
		Build()

	r := &ReconcileObservability{
		client:      client,
		featureGate: createEnabledFeatureGate(),
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// First reconciliation
	result1, err1 := r.Reconcile(context.TODO(), req)
	g.Expect(err1).NotTo(HaveOccurred())
	g.Expect(result1).To(Equal(reconcile.Result{}))

	// Second reconciliation should be idempotent
	result2, err2 := r.Reconcile(context.TODO(), req)
	g.Expect(err2).NotTo(HaveOccurred())
	g.Expect(result2).To(Equal(reconcile.Result{}))

	// Results should be the same
	g.Expect(result1).To(Equal(result2))
}

// TestReconcile_OperatorNotReady tests reconciliation when operator exists
// but is not ready yet
func TestReconcile_OperatorNotReady(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add configv1 to scheme: %v", err)
	}

	network := createTestNetwork("cluster", "InstallAndEnable")
	crd := createTestCRD("flowcollectors.flows.netobserv.io")
	// CSV exists but not in Succeeded phase
	clusterExtension := createTestClusterExtension(t, "netobserv-operator", false)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, crd, clusterExtension).
		Build()

	r := &ReconcileObservability{
		client:      client,
		featureGate: createEnabledFeatureGate(),
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := r.Reconcile(ctx, req)

	// Controller returns no error, but should requeue after failing FlowCollector creation
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result.RequeueAfter).To(Equal(requeueAfterStandard))
}

// TestReconcile_FlowCollectorDeleted tests that reconciliation does not recreate
// FlowCollector if it was previously deployed and then deleted (default policy)
func TestReconcile_FlowCollectorDeleted(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add configv1 to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add corev1 to scheme: %v", err)
	}
	if err := operatorv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add operatorv1 to scheme: %v", err)
	}

	// Create network with the deployed condition set (simulating previous successful deployment)
	// FlowCollector is NOT present (deleted)
	// Use default policy (empty string) - which should NOT reinstall after deployment
	network := createTestNetwork("cluster", "")
	operatorNetwork := createTestOperatorNetworkWithDeployedCondition("cluster")
	crd := createTestCRD("flowcollectors.flows.netobserv.io")
	clusterExtension := createTestClusterExtension(t, "netobserv-operator", true)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, operatorNetwork, crd, clusterExtension).
		WithStatusSubresource(&operatorv1.Network{}).
		Build()

	r := &ReconcileObservability{
		client:      client,
		featureGate: createEnabledFeatureGate(),
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// Reconciliation should skip everything since deployment condition is set
	result, err := r.Reconcile(context.TODO(), req)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(Equal(reconcile.Result{}))
}

// TestReconcile_OperatorDeleted tests that operator is not reinstalled after deletion if previously deployed (default policy)
func TestReconcile_OperatorDeleted(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add configv1 to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add corev1 to scheme: %v", err)
	}
	if err := operatorv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add operatorv1 to scheme: %v", err)
	}

	// Create network with the deployed condition set (simulating previous successful deployment)
	// Operator subscription and CSV are NOT present (simulating deletion)
	// Use default policy (empty string) - which should NOT reinstall after deployment
	network := createTestNetwork("cluster", "")
	operatorNetwork := createTestOperatorNetworkWithDeployedCondition("cluster")
	flowCollector := createTestFlowCollector(FlowCollectorName)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, operatorNetwork, flowCollector).
		WithStatusSubresource(&operatorv1.Network{}).
		Build()

	r := &ReconcileObservability{
		client:      client,
		featureGate: createEnabledFeatureGate(),
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// Reconciliation should skip everything since deployment condition is set
	result, err := r.Reconcile(context.TODO(), req)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(Equal(reconcile.Result{}))
}

// TestReconcile_BothDeleted tests that nothing is reinstalled when both operator and FlowCollector are deleted (default policy)
func TestReconcile_BothDeleted(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add configv1 to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add corev1 to scheme: %v", err)
	}
	if err := operatorv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add operatorv1 to scheme: %v", err)
	}

	// Create network with the deployed condition set (simulating previous successful deployment)
	// Neither operator nor FlowCollector are present (both deleted)
	// Use default policy (empty string) - which should NOT reinstall after deployment
	network := createTestNetwork("cluster", "")
	operatorNetwork := createTestOperatorNetworkWithDeployedCondition("cluster")

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, operatorNetwork).
		WithStatusSubresource(&operatorv1.Network{}).
		Build()

	r := &ReconcileObservability{
		client:      client,
		featureGate: createEnabledFeatureGate(),
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// Reconciliation should skip everything since deployment condition is set
	result, err := r.Reconcile(context.TODO(), req)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(Equal(reconcile.Result{}))
}

// TestReconcile_InstallAndEnable_DoesNotReinstallWhenDeployed tests that InstallAndEnable
// policy does NOT reinstall when already deployed (same behavior as default policy)
func TestReconcile_InstallAndEnable_DoesNotReinstallWhenDeployed(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add configv1 to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add corev1 to scheme: %v", err)
	}
	if err := operatorv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add operatorv1 to scheme: %v", err)
	}

	// Create network with InstallAndEnable policy and deployed condition set
	// FlowCollector is NOT present (deleted)
	// InstallAndEnable should NOT reinstall after deployment
	network := createTestNetwork("cluster", string(configv1.NetworkObservabilityInstallAndEnable))
	operatorNetwork := createTestOperatorNetworkWithDeployedCondition("cluster")
	crd := createTestCRD("flowcollectors.flows.netobserv.io")
	clusterExtension := createTestClusterExtension(t, "netobserv-operator", true)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, operatorNetwork, crd, clusterExtension).
		WithStatusSubresource(&operatorv1.Network{}).
		Build()

	r := &ReconcileObservability{
		client:      client,
		featureGate: createEnabledFeatureGate(),
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// Reconciliation should skip everything since deployment condition is set
	result, err := r.Reconcile(context.TODO(), req)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(Equal(reconcile.Result{}))
}

// TestShouldInstallNetworkObservability_InstallAndEnable_AlreadyDeployed tests that
// InstallAndEnable returns false when already deployed
func TestShouldInstallNetworkObservability_InstallAndEnable_AlreadyDeployed(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add configv1 to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add corev1 to scheme: %v", err)
	}
	if err := operatorv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add operatorv1 to scheme: %v", err)
	}

	network := &configv1.Network{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: configv1.NetworkSpec{
			NetworkObservability: configv1.NetworkObservabilitySpec{
				InstallationPolicy: configv1.NetworkObservabilityInstallAndEnable,
			},
		},
	}
	operatorNetwork := createTestOperatorNetworkWithDeployedCondition("cluster")
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(network, operatorNetwork, infra).Build()
	r := &ReconcileObservability{client: client}

	result, err := r.shouldInstallNetworkObservability(context.TODO())

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(BeFalse()) // Should NOT install when already deployed
}

// TestReconcile_NetworkCRUpdated tests that reconciliation handles
// Network CR updates correctly
func TestReconcile_NetworkCRUpdated(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add configv1 to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add corev1 to scheme: %v", err)
	}
	if err := operatorv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add operatorv1 to scheme: %v", err)
	}

	// Start with disabled
	network := createTestNetwork("cluster", "NoAction")
	operatorNetwork := createTestOperatorNetwork("cluster")
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, operatorNetwork, infra).
		WithStatusSubresource(&configv1.Network{}, &operatorv1.Network{}).
		Build()

	r := &ReconcileObservability{
		client:      client,
		featureGate: createEnabledFeatureGate(),
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// First reconciliation - should skip
	result1, err1 := r.Reconcile(context.TODO(), req)
	g.Expect(err1).NotTo(HaveOccurred())
	g.Expect(result1).To(Equal(reconcile.Result{}))

	// Update Network CR to enable observability
	network.Spec.NetworkObservability = configv1.NetworkObservabilitySpec{
		InstallationPolicy: configv1.NetworkObservabilityInstallAndEnable,
	}
	err := client.Update(context.TODO(), network)
	g.Expect(err).NotTo(HaveOccurred())

	// Second reconciliation - should now try to install
	// This will fail because manifest doesn't exist, but will requeue instead of erroring
	result2, err2 := r.Reconcile(context.TODO(), req)

	// Should requeue, not error
	g.Expect(err2).ToNot(HaveOccurred())
	g.Expect(result2.RequeueAfter).To(Equal(requeueAfterOLM))
}

// Error Scenario Tests

// TestReconcile_PartialFailure_OperatorInstallFails tests recovery
// when operator installation fails
func TestReconcile_PartialFailure_OperatorInstallFails(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add configv1 to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add corev1 to scheme: %v", err)
	}
	if err := operatorv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add operatorv1 to scheme: %v", err)
	}

	network := createTestNetwork("cluster", "InstallAndEnable")
	operatorNetwork := createTestOperatorNetwork("cluster")
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, operatorNetwork, infra).
		WithStatusSubresource(&configv1.Network{}, &operatorv1.Network{}).
		Build()

	r := &ReconcileObservability{
		client:      client,
		featureGate: createEnabledFeatureGate(),
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// Reconciliation should requeue when install fails (manifest doesn't exist)
	result, err := r.Reconcile(context.TODO(), req)

	// Should requeue, not error
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.RequeueAfter).To(Equal(requeueAfterOLM))
}

// TestReconcile_RecoveryAfterOperatorBecomesReady tests that reconciliation
// continues after operator becomes ready
func TestReconcile_RecoveryAfterOperatorBecomesReady(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add configv1 to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add corev1 to scheme: %v", err)
	}

	network := createTestNetwork("cluster", "InstallAndEnable")
	crd := createTestCRD("flowcollectors.flows.netobserv.io")
	// Start with CSV in Installing phase
	clusterExtension := createTestClusterExtension(t, "netobserv-operator", false)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, crd, clusterExtension).
		Build()

	r := &ReconcileObservability{
		client:      client,
		featureGate: createEnabledFeatureGate(),
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// First reconciliation will fail creating FlowCollector (returns no error but RequeueAfter=30s)
	ctx1, cancel1 := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel1()

	result1, err1 := r.Reconcile(ctx1, req)
	g.Expect(err1).NotTo(HaveOccurred())
	g.Expect(result1.RequeueAfter).To(Equal(requeueAfterStandard))

	// Update ClusterExtension to Installed status
	conditions := []interface{}{
		map[string]interface{}{
			"type":    "Installed",
			"status":  "True",
			"reason":  "InstallSucceeded",
			"message": "ClusterExtension installed successfully",
		},
	}
	if err := unstructured.SetNestedSlice(clusterExtension.Object, conditions, "status", "conditions"); err != nil {
		t.Fatalf("Failed to set status conditions: %v", err)
	}
	err := client.Update(context.TODO(), clusterExtension)
	g.Expect(err).NotTo(HaveOccurred())

	// Second reconciliation should proceed past operator wait
	// and attempt to create FlowCollector (which will fail due to missing manifest)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()

	result2, err2 := r.Reconcile(ctx2, req)

	// Should requeue after failing to read FlowCollector manifest
	g.Expect(err2).ToNot(HaveOccurred())
	g.Expect(result2.RequeueAfter).To(Equal(requeueAfterStandard))
}

// Performance/Stress Tests

// TestReconcile_ConcurrentReconciliations tests that multiple concurrent
// reconciliations don't cause issues
func TestReconcile_ConcurrentReconciliations(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add configv1 to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add corev1 to scheme: %v", err)
	}

	network := createTestNetwork("cluster", "InstallAndEnable")
	crd := createTestCRD("flowcollectors.flows.netobserv.io")
	clusterExtension := createTestClusterExtension(t, "netobserv-operator", true)
	flowCollector := createTestFlowCollector(FlowCollectorName)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, crd, clusterExtension, flowCollector).
		WithStatusSubresource(&configv1.Network{}).
		Build()

	r := &ReconcileObservability{
		client: client,
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// Run 5 concurrent reconciliations
	errChan := make(chan error, 5)
	for i := 0; i < 5; i++ {
		go func() {
			_, err := r.Reconcile(context.TODO(), req)
			errChan <- err
		}()
	}

	// Wait for all to complete and collect errors
	var unexpectedErrors []error
	for i := 0; i < 5; i++ {
		if err := <-errChan; err != nil {
			// Filter out 409 conflict errors which are expected when multiple
			// goroutines try to update the same resource status concurrently
			if !errors.IsConflict(err) {
				unexpectedErrors = append(unexpectedErrors, err)
			}
		}
	}

	// Assert no unexpected errors occurred (safe to do in main test goroutine)
	g.Expect(unexpectedErrors).To(BeEmpty(), "All concurrent reconciliations should complete without unexpected errors")
}

// Status Manager Tests

// TestReconcile_SetsConditionFalseOnError tests that NetworkObservabilityDeployed condition is set to False on errors
func TestReconcile_SetsConditionFalseOnError(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add configv1 to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add corev1 to scheme: %v", err)
	}
	if err := operatorv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add operatorv1 to scheme: %v", err)
	}

	network := createTestNetwork("cluster", "InstallAndEnable")
	operatorNetwork := createTestOperatorNetwork("cluster")
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, operatorNetwork, infra).
		WithStatusSubresource(&operatorv1.Network{}).
		Build()

	r := &ReconcileObservability{
		client:      client,
		featureGate: createEnabledFeatureGate(),
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// Reconciliation should fail trying to install operator (manifest doesn't exist)
	result, err := r.Reconcile(context.TODO(), req)

	// Should requeue without error
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.RequeueAfter).To(Equal(requeueAfterOLM))

	// Verify that NetworkObservabilityDeployed condition is set to False
	updatedNetwork := &operatorv1.Network{}
	err = client.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, updatedNetwork)
	g.Expect(err).NotTo(HaveOccurred())

	conditionFound := false
	for _, condition := range updatedNetwork.Status.Conditions {
		if condition.Type == NetworkObservabilityDeployed {
			conditionFound = true
			g.Expect(condition.Status).To(Equal(operatorv1.ConditionFalse))
			g.Expect(condition.Reason).To(Equal("DeploymentFailed"))
			g.Expect(condition.Message).To(ContainSubstring("Failed to install Network Observability Operator"))
			break
		}
	}
	g.Expect(conditionFound).To(BeTrue(), "NetworkObservabilityDeployed condition should be set")
}

// TestReconcile_SetsConditionTrueOnSuccess tests that NetworkObservabilityDeployed condition is set to True on success
func TestReconcile_SetsConditionTrueOnSuccess(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add configv1 to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add corev1 to scheme: %v", err)
	}
	if err := operatorv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add operatorv1 to scheme: %v", err)
	}

	network := createTestNetwork("cluster", "InstallAndEnable")
	operatorNetwork := createTestOperatorNetwork("cluster")
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)
	crd := createTestCRD("flowcollectors.flows.netobserv.io")
	clusterExtension := createTestClusterExtension(t, "netobserv-operator", true)
	flowCollector := createTestFlowCollector(FlowCollectorName)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, operatorNetwork, infra, crd, clusterExtension, flowCollector).
		WithStatusSubresource(&operatorv1.Network{}).
		Build()

	r := &ReconcileObservability{
		client:      client,
		featureGate: createEnabledFeatureGate(),
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// Reconciliation should succeed (FlowCollector already exists)
	_, err := r.Reconcile(context.TODO(), req)

	g.Expect(err).NotTo(HaveOccurred())

	// Verify that NetworkObservabilityDeployed condition is set to True
	updatedNetwork := &operatorv1.Network{}
	err = client.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, updatedNetwork)
	g.Expect(err).NotTo(HaveOccurred())

	conditionFound := false
	for _, condition := range updatedNetwork.Status.Conditions {
		if condition.Type == NetworkObservabilityDeployed {
			conditionFound = true
			g.Expect(condition.Status).To(Equal(operatorv1.ConditionTrue))
			g.Expect(condition.Reason).To(Equal("DeploymentComplete"))
			g.Expect(condition.Message).To(Equal("Network Observability has been deployed"))
			break
		}
	}
	g.Expect(conditionFound).To(BeTrue(), "NetworkObservabilityDeployed condition should be set")
}

// TestReconcile_SkipsWhenNoActionPolicy tests that reconciliation skips when NoAction policy is set
func TestReconcile_SkipsWhenNoActionPolicy(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add configv1 to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add corev1 to scheme: %v", err)
	}
	if err := operatorv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add operatorv1 to scheme: %v", err)
	}

	network := createTestNetwork("cluster", "NoAction") // disabled
	operatorNetwork := createTestOperatorNetwork("cluster")
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, operatorNetwork, infra).
		Build()

	r := &ReconcileObservability{
		client:      client,
		featureGate: createEnabledFeatureGate(),
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// Reconciliation should succeed and skip installation
	result, err := r.Reconcile(context.TODO(), req)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(Equal(reconcile.Result{}))
}

// TestReconcile_RequeuesOnInfrastructureError tests that reconciliation requeues when Infrastructure lookup fails
func TestReconcile_RequeuesOnInfrastructureError(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	if err := configv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed to add configv1 to scheme: %v", err)
	}

	// Create network with no NetworkObservability field (will trigger SNO check which needs Infrastructure)
	network := &configv1.Network{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec:       configv1.NetworkSpec{
			// NetworkObservability not set: will trigger SNO check
		},
	}

	// Don't add Infrastructure object - this will cause Get to fail
	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network).
		Build()

	r := &ReconcileObservability{
		client:      client,
		featureGate: createEnabledFeatureGate(),
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// Reconciliation should requeue when checking Infrastructure fails
	result, err := r.Reconcile(context.TODO(), req)

	// Should requeue without error (errors are logged and requeued)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.RequeueAfter).To(Equal(requeueAfterStandard))
}

func TestIsFeatureGateEnabled_NilFeatureGate(t *testing.T) {
	g := NewGomegaWithT(t)

	r := &ReconcileObservability{featureGate: nil}

	// Should default to disabled when featureGate is nil
	result := r.isFeatureGateEnabled()
	g.Expect(result).To(BeFalse())
}

func TestIsFeatureGateEnabled_FeatureGateEnabled(t *testing.T) {
	g := NewGomegaWithT(t)

	// Create a feature gate with NetworkObservabilityInstall enabled
	fg := featuregates.NewFeatureGate(
		[]configv1.FeatureGateName{"NetworkObservabilityInstall"},
		[]configv1.FeatureGateName{},
	)

	r := &ReconcileObservability{featureGate: fg}

	result := r.isFeatureGateEnabled()
	g.Expect(result).To(BeTrue())
}

func TestIsFeatureGateEnabled_FeatureGateDisabled(t *testing.T) {
	g := NewGomegaWithT(t)

	// Create a feature gate with NetworkObservabilityInstall disabled
	fg := featuregates.NewFeatureGate(
		[]configv1.FeatureGateName{},
		[]configv1.FeatureGateName{"NetworkObservabilityInstall"},
	)

	r := &ReconcileObservability{featureGate: fg}

	result := r.isFeatureGateEnabled()
	g.Expect(result).To(BeFalse())
}
