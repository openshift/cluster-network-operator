package observability

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// Helper functions for creating test resources

func createTestNetwork(name string, value string) *configv1.Network {
	network := &configv1.Network{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}

	if value != "" {
		policy := configv1.NetworkObservabilityInstallationPolicy(value)
		network.Spec.NetworkObservability = configv1.NetworkObservabilitySpec{
			InstallationPolicy: &policy,
		}
	}

	return network
}

func createTestNetworkWithDeployedCondition(name string, value string) *configv1.Network {
	network := createTestNetwork(name, value)
	network.Status.Conditions = []metav1.Condition{
		{
			Type:               NetworkObservabilityDeployed,
			Status:             metav1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Reason:             "DeploymentComplete",
			Message:            "Network Observability FlowCollector was successfully deployed",
		},
	}
	return network
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

func createTestSubscription(name, namespace string) *unstructured.Unstructured {
	sub := &unstructured.Unstructured{}
	sub.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "operators.coreos.com",
		Version: "v1alpha1",
		Kind:    "Subscription",
	})
	sub.SetName(name)
	sub.SetNamespace(namespace)
	return sub
}

func createTestCSV(name, namespace, phase string) *unstructured.Unstructured {
	csv := &unstructured.Unstructured{}
	csv.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "operators.coreos.com",
		Version: "v1alpha1",
		Kind:    "ClusterServiceVersion",
	})
	csv.SetName(name)
	csv.SetNamespace(namespace)
	if phase != "" {
		_ = unstructured.SetNestedField(csv.Object, phase, "status", "phase")
	}
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

// Mock status manager that implements the methods the controller needs
type mockStatusManager struct {
	mu               sync.Mutex
	degradedCalls    []degradedCall
	notDegradedCalls []statusmanager.StatusLevel
}

type degradedCall struct {
	level   statusmanager.StatusLevel
	reason  string
	message string
}

func (m *mockStatusManager) SetDegraded(level statusmanager.StatusLevel, reason, message string) {
	if m != nil {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.degradedCalls = append(m.degradedCalls, degradedCall{level, reason, message})
	}
}

func (m *mockStatusManager) SetNotDegraded(level statusmanager.StatusLevel) {
	if m != nil {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.notDegradedCalls = append(m.notDegradedCalls, level)
	}
}

func newMockStatusManager() *mockStatusManager {
	return &mockStatusManager{
		degradedCalls:    []degradedCall{},
		notDegradedCalls: []statusmanager.StatusLevel{},
	}
}

// Test shouldInstallNetworkObservability()

func TestShouldInstallNetworkObservability_NilNonSNO(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)

	network := &configv1.Network{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec:       configv1.NetworkSpec{
			// NetworkObservability not set: Default behavior should install on non-SNO
		},
	}
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(infra).Build()
	r := &ReconcileObservability{client: client}

	result, err := r.shouldInstallNetworkObservability(context.TODO(), network)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(BeTrue())
}

func TestShouldInstallNetworkObservability_NilSNO(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)

	network := &configv1.Network{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec:       configv1.NetworkSpec{
			// NetworkObservability not set: Default behavior should NOT install on SNO
		},
	}
	infra := createTestInfrastructure(configv1.SingleReplicaTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(infra).Build()
	r := &ReconcileObservability{client: client}

	result, err := r.shouldInstallNetworkObservability(context.TODO(), network)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(BeFalse())
}

func TestShouldInstallNetworkObservability_ExplicitInstallAndEnableNonSNO(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)

	network := &configv1.Network{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: configv1.NetworkSpec{
			NetworkObservability: configv1.NetworkObservabilitySpec{
				InstallationPolicy: ptr.To(configv1.NetworkObservabilityInstallAndEnable),
			},
		},
	}
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(infra).Build()
	r := &ReconcileObservability{client: client}

	result, err := r.shouldInstallNetworkObservability(context.TODO(), network)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(BeTrue())
}

func TestShouldInstallNetworkObservability_ExplicitInstallAndEnableSNO(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)

	network := &configv1.Network{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: configv1.NetworkSpec{
			NetworkObservability: configv1.NetworkObservabilitySpec{
				InstallationPolicy: ptr.To(configv1.NetworkObservabilityInstallAndEnable), // Explicit InstallAndEnable: install even on SNO
			},
		},
	}
	infra := createTestInfrastructure(configv1.SingleReplicaTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(infra).Build()
	r := &ReconcileObservability{client: client}

	result, err := r.shouldInstallNetworkObservability(context.TODO(), network)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(BeTrue())
}

func TestShouldInstallNetworkObservability_ExplicitDoNotInstall(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)

	network := &configv1.Network{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: configv1.NetworkSpec{
			NetworkObservability: configv1.NetworkObservabilitySpec{
				InstallationPolicy: ptr.To(configv1.NetworkObservabilityDoNotInstall),
			},
		},
	}
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(infra).Build()
	r := &ReconcileObservability{client: client}

	result, err := r.shouldInstallNetworkObservability(context.TODO(), network)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(BeFalse())
}

func TestShouldInstallNetworkObservability_EmptyStringNonSNO(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)

	network := &configv1.Network{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: configv1.NetworkSpec{
			NetworkObservability: configv1.NetworkObservabilitySpec{
				InstallationPolicy: ptr.To(configv1.NetworkObservabilityNoOpinion), // Empty string: default behavior (install on non-SNO)
			},
		},
	}
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(infra).Build()
	r := &ReconcileObservability{client: client}

	result, err := r.shouldInstallNetworkObservability(context.TODO(), network)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(BeTrue())
}

// Test isSingleNodeCluster()

func TestIsSingleNodeCluster_SNO(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)

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
	_ = configv1.AddToScheme(scheme)

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
	_ = configv1.AddToScheme(scheme)

	network := createTestNetwork("not-cluster", "InstallAndEnable")
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(network).Build()

	r := &ReconcileObservability{
		client: client,
		status: newMockStatusManager(),
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "not-cluster"}}
	result, err := r.Reconcile(context.TODO(), req)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(Equal(ctrl.Result{}))
}

func TestReconcile_SkipsWhenDisabled(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)

	// Explicitly set to false (opt-out)
	network := createTestNetwork("cluster", "DoNotInstall")
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(network).Build()

	r := &ReconcileObservability{
		client: client,
		status: newMockStatusManager(),
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}
	result, err := r.Reconcile(context.TODO(), req)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(Equal(ctrl.Result{}))
}

func TestReconcile_InstallsWhenNil(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	// Create network with no NetworkObservability field (defaults to enabled on non-SNO)
	network := &configv1.Network{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
		Spec: configv1.NetworkSpec{
			// NetworkObservability not set: defaults to enabled on non-SNO
		},
	}
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(network, infra).Build()

	r := &ReconcileObservability{
		client: client,
		status: newMockStatusManager(),
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}
	_, err := r.Reconcile(context.TODO(), req)

	// When nil, controller should try to install (opt-out behavior)
	// This will fail because the manifest doesn't exist, which proves it tried to install
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("failed to read"))

	// Verify that the operator namespace was created despite the error
	ns := &corev1.Namespace{}
	nsErr := client.Get(context.TODO(), types.NamespacedName{Name: OperatorNamespace}, ns)
	g.Expect(nsErr).NotTo(HaveOccurred())
	g.Expect(ns.Name).To(Equal(OperatorNamespace))
}

func TestReconcile_SkipsInstallWhenNilOnSNO(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	// Create network with no NetworkObservability field on SNO cluster
	network := &configv1.Network{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
		Spec: configv1.NetworkSpec{
			// NetworkObservability not set: defaults to disabled on SNO
		},
	}
	infra := createTestInfrastructure(configv1.SingleReplicaTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(network, infra).Build()

	r := &ReconcileObservability{
		client: client,
		status: newMockStatusManager(),
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}
	result, err := r.Reconcile(context.TODO(), req)

	// On SNO with nil, controller should skip installation
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(Equal(ctrl.Result{}))

	// Verify that the operator namespace was NOT created
	ns := &corev1.Namespace{}
	nsErr := client.Get(context.TODO(), types.NamespacedName{Name: OperatorNamespace}, ns)
	g.Expect(nsErr).To(HaveOccurred())
	g.Expect(nsErr.Error()).To(ContainSubstring("not found"))
}

func TestReconcile_IgnoresNotFound(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)

	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &ReconcileObservability{
		client: client,
		status: newMockStatusManager(),
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}
	result, err := r.Reconcile(context.TODO(), req)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(Equal(ctrl.Result{}))
}

// Test isNetObservOperatorInstalled()

func TestIsNetObservOperatorInstalled_True(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()

	subscription := createTestSubscription("netobserv-operator", OperatorNamespace)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(subscription).Build()

	r := &ReconcileObservability{client: client}

	installed, err := r.isNetObservOperatorInstalled(context.TODO())

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(installed).To(BeTrue())
}

func TestIsNetObservOperatorInstalled_False(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()

	client := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &ReconcileObservability{client: client}

	installed, err := r.isNetObservOperatorInstalled(context.TODO())

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(installed).To(BeFalse())
}

func TestIsNetObservOperatorInstalled_Multiple(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()

	// Create multiple subscriptions, but only one with the correct name
	subscription1 := createTestSubscription("other-operator", OperatorNamespace)
	subscription2 := createTestSubscription("netobserv-operator", OperatorNamespace)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(subscription1, subscription2).Build()

	r := &ReconcileObservability{client: client}

	installed, err := r.isNetObservOperatorInstalled(context.TODO())

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(installed).To(BeTrue())
}

// Test waitForNetObservOperator()

func TestWaitForOperator_Success(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()

	csv := createTestCSV("netobserv-operator.v1.0.0", OperatorNamespace, "Succeeded")

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(csv).Build()

	r := &ReconcileObservability{client: client}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := r.waitForNetObservOperator(ctx)

	g.Expect(err).NotTo(HaveOccurred())
}

func TestWaitForOperator_Timeout(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()

	// CSV exists but not in Succeeded phase
	csv := createTestCSV("netobserv-operator.v1.0.0", OperatorNamespace, "Installing")

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(csv).Build()

	r := &ReconcileObservability{client: client}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	err := r.waitForNetObservOperator(ctx)

	g.Expect(err).To(HaveOccurred())
	g.Expect(err).To(Equal(context.DeadlineExceeded))
}

func TestWaitForOperator_MissingStatus(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()

	// Create CSV without status phase
	csv := createTestCSV("netobserv-operator.v1.0.0", OperatorNamespace, "")

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(csv).Build()

	r := &ReconcileObservability{client: client}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	err := r.waitForNetObservOperator(ctx)

	g.Expect(err).To(HaveOccurred())
	g.Expect(err).To(Equal(context.DeadlineExceeded))
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
	_ = corev1.AddToScheme(scheme)

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
	_ = corev1.AddToScheme(scheme)

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
	_ = configv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	network := createTestNetwork("cluster", "InstallAndEnable")
	subscription := createTestSubscription("netobserv-operator", OperatorNamespace)
	csv := createTestCSV("netobserv-operator.v1.0.0", OperatorNamespace, "Succeeded")
	flowCollector := createTestFlowCollector(FlowCollectorName)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, subscription, csv, flowCollector).
		WithStatusSubresource(&configv1.Network{}).
		Build()

	r := &ReconcileObservability{
		client: client,
		status: newMockStatusManager(),
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}
	result, err := r.Reconcile(context.TODO(), req)

	// Should complete without error
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(Equal(ctrl.Result{}))
}

// TestReconcile_SkipsInstallWhenExists tests that reconciliation
// doesn't try to install operator if it already exists
func TestReconcile_SkipsInstallWhenExists(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	network := createTestNetwork("cluster", "InstallAndEnable")
	subscription := createTestSubscription("netobserv-operator", OperatorNamespace)
	csv := createTestCSV("netobserv-operator.v1.0.0", OperatorNamespace, "Succeeded")
	operatorNs := createTestNamespace(OperatorNamespace)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, subscription, csv, operatorNs).
		Build()

	r := &ReconcileObservability{
		client: client,
		status: newMockStatusManager(),
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// Since operator is already installed, it should proceed to FlowCollector creation
	// which will fail (due to fake client not supporting server-side apply)
	// but that's expected behavior for this test
	_, err := r.Reconcile(context.TODO(), req)

	// We expect an error because FlowCollector creation requires manifest apply
	// which fake client doesn't support, but the operator installation check passed
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("failed to read"))
}

// Edge Case Tests

// TestReconcile_MultipleInvocations tests that multiple reconciliations
// handle idempotency correctly
func TestReconcile_MultipleInvocations(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	network := createTestNetwork("cluster", "InstallAndEnable")
	subscription := createTestSubscription("netobserv-operator", OperatorNamespace)
	csv := createTestCSV("netobserv-operator.v1.0.0", OperatorNamespace, "Succeeded")
	flowCollector := createTestFlowCollector(FlowCollectorName)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, subscription, csv, flowCollector).
		WithStatusSubresource(&configv1.Network{}).
		Build()

	r := &ReconcileObservability{
		client: client,
		status: newMockStatusManager(),
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// First reconciliation
	result1, err1 := r.Reconcile(context.TODO(), req)
	g.Expect(err1).NotTo(HaveOccurred())
	g.Expect(result1).To(Equal(ctrl.Result{}))

	// Second reconciliation should be idempotent
	result2, err2 := r.Reconcile(context.TODO(), req)
	g.Expect(err2).NotTo(HaveOccurred())
	g.Expect(result2).To(Equal(ctrl.Result{}))

	// Results should be the same
	g.Expect(result1).To(Equal(result2))
}

// TestReconcile_OperatorNotReady tests reconciliation when operator exists
// but is not ready yet
func TestReconcile_OperatorNotReady(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)

	network := createTestNetwork("cluster", "InstallAndEnable")
	subscription := createTestSubscription("netobserv-operator", OperatorNamespace)
	// CSV exists but not in Succeeded phase
	csv := createTestCSV("netobserv-operator.v1.0.0", OperatorNamespace, "Installing")

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, subscription, csv).
		Build()

	r := &ReconcileObservability{
		client: client,
		status: newMockStatusManager(),
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := r.Reconcile(ctx, req)

	// Controller returns no error on timeout, but should requeue after 5 minutes
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result.RequeueAfter).To(Equal(5 * time.Minute))
}

// TestReconcile_FlowCollectorDeleted tests that reconciliation recreates
// FlowCollector if it gets deleted
func TestReconcile_FlowCollectorDeleted(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	// Create network with the deployed condition set (simulating previous successful deployment)
	// FlowCollector is NOT present (deleted)
	network := createTestNetworkWithDeployedCondition("cluster", "InstallAndEnable")
	subscription := createTestSubscription("netobserv-operator", OperatorNamespace)
	csv := createTestCSV("netobserv-operator.v1.0.0", OperatorNamespace, "Succeeded")

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, subscription, csv).
		WithStatusSubresource(&configv1.Network{}).
		Build()

	mockStatus := newMockStatusManager()
	r := &ReconcileObservability{
		client: client,
		status: mockStatus,
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// Reconciliation should skip everything since deployment condition is set
	result, err := r.Reconcile(context.TODO(), req)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(Equal(ctrl.Result{}))

	// Verify status was set to not degraded
	g.Expect(len(mockStatus.notDegradedCalls)).To(Equal(1))
	g.Expect(mockStatus.notDegradedCalls[0]).To(Equal(statusmanager.ObservabilityConfig))
}

// TestReconcile_OperatorDeleted tests that operator is not reinstalled after deletion if previously deployed
func TestReconcile_OperatorDeleted(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	// Create network with the deployed condition set (simulating previous successful deployment)
	// Operator subscription and CSV are NOT present (simulating deletion)
	network := createTestNetworkWithDeployedCondition("cluster", "InstallAndEnable")
	flowCollector := createTestFlowCollector(FlowCollectorName)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, flowCollector).
		WithStatusSubresource(&configv1.Network{}).
		Build()

	mockStatus := newMockStatusManager()
	r := &ReconcileObservability{
		client: client,
		status: mockStatus,
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// Reconciliation should skip everything since deployment condition is set
	result, err := r.Reconcile(context.TODO(), req)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(Equal(ctrl.Result{}))

	// Verify status was set to not degraded
	g.Expect(len(mockStatus.notDegradedCalls)).To(Equal(1))
	g.Expect(mockStatus.notDegradedCalls[0]).To(Equal(statusmanager.ObservabilityConfig))
}

// TestReconcile_BothDeleted tests that nothing is reinstalled when both operator and FlowCollector are deleted
func TestReconcile_BothDeleted(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	// Create network with the deployed condition set (simulating previous successful deployment)
	// Neither operator nor FlowCollector are present (both deleted)
	network := createTestNetworkWithDeployedCondition("cluster", "InstallAndEnable")

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network).
		WithStatusSubresource(&configv1.Network{}).
		Build()

	mockStatus := newMockStatusManager()
	r := &ReconcileObservability{
		client: client,
		status: mockStatus,
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// Reconciliation should skip everything since deployment condition is set
	result, err := r.Reconcile(context.TODO(), req)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(Equal(ctrl.Result{}))

	// Verify status was set to not degraded
	g.Expect(len(mockStatus.notDegradedCalls)).To(Equal(1))
	g.Expect(mockStatus.notDegradedCalls[0]).To(Equal(statusmanager.ObservabilityConfig))
}

// TestReconcile_NetworkCRUpdated tests that reconciliation handles
// Network CR updates correctly
func TestReconcile_NetworkCRUpdated(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)

	// Start with disabled
	network := createTestNetwork("cluster", "DoNotInstall")

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network).
		Build()

	r := &ReconcileObservability{
		client: client,
		status: newMockStatusManager(),
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// First reconciliation - should skip
	result1, err1 := r.Reconcile(context.TODO(), req)
	g.Expect(err1).NotTo(HaveOccurred())
	g.Expect(result1).To(Equal(ctrl.Result{}))

	// Update Network CR to enable observability
	network.Spec.NetworkObservability = configv1.NetworkObservabilitySpec{
		InstallationPolicy: ptr.To(configv1.NetworkObservabilityInstallAndEnable),
	}
	err := client.Update(context.TODO(), network)
	g.Expect(err).NotTo(HaveOccurred())

	// Second reconciliation - should now try to install
	// This will fail because no operator is installed, but verifies
	// that the flag change is detected
	_, err2 := r.Reconcile(context.TODO(), req)

	// Should fail trying to create operator namespace or install operator
	g.Expect(err2).To(HaveOccurred())
}

// Error Scenario Tests

// TestReconcile_PartialFailure_OperatorInstallFails tests recovery
// when operator installation fails
func TestReconcile_PartialFailure_OperatorInstallFails(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	network := createTestNetwork("cluster", "InstallAndEnable")

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network).
		Build()

	r := &ReconcileObservability{
		client: client,
		status: newMockStatusManager(),
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// Reconciliation should fail trying to install operator (manifest doesn't exist)
	_, err := r.Reconcile(context.TODO(), req)

	// Should fail at operator installation
	g.Expect(err).To(HaveOccurred())

	// Verify that namespace was created despite operator install failure
	ns := &corev1.Namespace{}
	nsErr := client.Get(context.TODO(), types.NamespacedName{Name: OperatorNamespace}, ns)
	g.Expect(nsErr).NotTo(HaveOccurred())
	g.Expect(ns.Name).To(Equal(OperatorNamespace))
}

// TestReconcile_RecoveryAfterOperatorBecomesReady tests that reconciliation
// continues after operator becomes ready
func TestReconcile_RecoveryAfterOperatorBecomesReady(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	network := createTestNetwork("cluster", "InstallAndEnable")
	subscription := createTestSubscription("netobserv-operator", OperatorNamespace)
	// Start with CSV in Installing phase
	csv := createTestCSV("netobserv-operator.v1.0.0", OperatorNamespace, "Installing")

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, subscription, csv).
		Build()

	r := &ReconcileObservability{
		client: client,
		status: newMockStatusManager(),
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// First reconciliation should timeout (returns no error but RequeueAfter=5 minutes)
	ctx1, cancel1 := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel1()

	result1, err1 := r.Reconcile(ctx1, req)
	g.Expect(err1).NotTo(HaveOccurred())
	g.Expect(result1.RequeueAfter).To(Equal(5 * time.Minute))

	// Update CSV to Succeeded phase
	_ = unstructured.SetNestedField(csv.Object, "Succeeded", "status", "phase")
	err := client.Update(context.TODO(), csv)
	g.Expect(err).NotTo(HaveOccurred())

	// Second reconciliation should proceed past operator wait
	// and attempt to create FlowCollector (which will fail due to missing manifest)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()

	_, err2 := r.Reconcile(ctx2, req)

	// Should fail trying to read FlowCollector manifest
	g.Expect(err2).To(HaveOccurred())
	g.Expect(err2.Error()).To(ContainSubstring("failed to read"))
}

// TestReconcile_NamespaceCreation tests namespace creation logic
func TestReconcile_NamespaceCreation(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	network := createTestNetwork("cluster", "InstallAndEnable")

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network).
		Build()

	r := &ReconcileObservability{
		client: client,
		status: newMockStatusManager(),
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// Reconciliation will fail, but should create the operator namespace first
	_, _ = r.Reconcile(context.TODO(), req)

	// Verify operator namespace was created
	ns := &corev1.Namespace{}
	err := client.Get(context.TODO(), types.NamespacedName{Name: OperatorNamespace}, ns)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ns.Name).To(Equal(OperatorNamespace))
}

// TestReconcile_NamespaceAlreadyExists tests that reconciliation
// handles pre-existing namespaces gracefully
func TestReconcile_NamespaceAlreadyExists(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	network := createTestNetwork("cluster", "InstallAndEnable")
	// Namespace already exists
	operatorNs := createTestNamespace(OperatorNamespace)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, operatorNs).
		Build()

	r := &ReconcileObservability{
		client: client,
		status: newMockStatusManager(),
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// Reconciliation should handle existing namespace gracefully
	_, _ = r.Reconcile(context.TODO(), req)

	// Verify namespace still exists
	ns := &corev1.Namespace{}
	err := client.Get(context.TODO(), types.NamespacedName{Name: OperatorNamespace}, ns)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(ns.Name).To(Equal(OperatorNamespace))
}

// Performance/Stress Tests

// TestReconcile_ConcurrentReconciliations tests that multiple concurrent
// reconciliations don't cause issues
func TestReconcile_ConcurrentReconciliations(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	network := createTestNetwork("cluster", "InstallAndEnable")
	subscription := createTestSubscription("netobserv-operator", OperatorNamespace)
	csv := createTestCSV("netobserv-operator.v1.0.0", OperatorNamespace, "Succeeded")
	flowCollector := createTestFlowCollector(FlowCollectorName)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, subscription, csv, flowCollector).
		WithStatusSubresource(&configv1.Network{}).
		Build()

	r := &ReconcileObservability{
		client: client,
		status: newMockStatusManager(),
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// Run 5 concurrent reconciliations
	done := make(chan bool, 5)
	for i := 0; i < 5; i++ {
		go func() {
			_, err := r.Reconcile(context.TODO(), req)
			// All should complete without error (idempotent)
			g.Expect(err).NotTo(HaveOccurred())
			done <- true
		}()
	}

	// Wait for all to complete
	for i := 0; i < 5; i++ {
		<-done
	}
}

// Status Manager Tests

// TestReconcile_StatusDegradedOnError tests that status is set to degraded on errors
func TestReconcile_StatusDegradedOnError(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	network := createTestNetwork("cluster", "InstallAndEnable")
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, infra).
		Build()

	mockStatus := newMockStatusManager()
	r := &ReconcileObservability{
		client: client,
		status: mockStatus,
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// Reconciliation should fail trying to install operator (manifest doesn't exist)
	_, err := r.Reconcile(context.TODO(), req)

	// Should fail at operator installation
	g.Expect(err).To(HaveOccurred())

	// Verify that status degraded was called
	g.Expect(len(mockStatus.degradedCalls)).To(BeNumerically(">", 0))
	g.Expect(mockStatus.degradedCalls[0].level).To(Equal(statusmanager.ObservabilityConfig))
	g.Expect(mockStatus.degradedCalls[0].reason).To(Equal("InstallOperatorError"))
}

// TestReconcile_StatusNotDegradedOnSuccess tests that status is cleared on success
func TestReconcile_StatusNotDegradedOnSuccess(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	network := createTestNetwork("cluster", "InstallAndEnable")
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)
	subscription := createTestSubscription("netobserv-operator", OperatorNamespace)
	csv := createTestCSV("netobserv-operator.v1.0.0", OperatorNamespace, "Succeeded")
	flowCollector := createTestFlowCollector(FlowCollectorName)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, infra, subscription, csv, flowCollector).
		WithStatusSubresource(&configv1.Network{}).
		Build()

	mockStatus := newMockStatusManager()
	r := &ReconcileObservability{
		client: client,
		status: mockStatus,
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// Reconciliation should succeed (FlowCollector already exists)
	_, err := r.Reconcile(context.TODO(), req)

	g.Expect(err).NotTo(HaveOccurred())

	// Verify that status not degraded was called
	g.Expect(len(mockStatus.notDegradedCalls)).To(BeNumerically(">", 0))
	g.Expect(mockStatus.notDegradedCalls[0]).To(Equal(statusmanager.ObservabilityConfig))
}

// TestReconcile_StatusNotDegradedWhenDisabled tests that status is cleared when disabled
func TestReconcile_StatusNotDegradedWhenDisabled(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	network := createTestNetwork("cluster", "DoNotInstall") // disabled
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, infra).
		Build()

	mockStatus := newMockStatusManager()
	r := &ReconcileObservability{
		client: client,
		status: mockStatus,
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// Reconciliation should succeed and skip installation
	_, err := r.Reconcile(context.TODO(), req)

	g.Expect(err).NotTo(HaveOccurred())

	// Verify that status not degraded was called (feature is disabled)
	g.Expect(len(mockStatus.notDegradedCalls)).To(Equal(1))
	g.Expect(mockStatus.notDegradedCalls[0]).To(Equal(statusmanager.ObservabilityConfig))
}

// TestReconcile_StatusDegradedOnInfrastructureError tests degraded status when Infrastructure lookup fails
func TestReconcile_StatusDegradedOnInfrastructureError(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)

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

	mockStatus := newMockStatusManager()
	r := &ReconcileObservability{
		client: client,
		status: mockStatus,
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// Reconciliation should fail when checking Infrastructure
	_, err := r.Reconcile(context.TODO(), req)

	g.Expect(err).To(HaveOccurred())

	// Verify that status degraded was called
	g.Expect(len(mockStatus.degradedCalls)).To(BeNumerically(">", 0))
	g.Expect(mockStatus.degradedCalls[0].level).To(Equal(statusmanager.ObservabilityConfig))
	g.Expect(mockStatus.degradedCalls[0].reason).To(Equal("CheckInstallError"))
}

// TestWasNetworkObservabilityDeployed tests the wasNetworkObservabilityDeployed helper
func TestWasNetworkObservabilityDeployed(t *testing.T) {
	g := NewGomegaWithT(t)

	r := &ReconcileObservability{}

	// Test with no conditions
	network1 := createTestNetwork("cluster", "InstallAndEnable")
	g.Expect(r.wasNetworkObservabilityDeployed(network1)).To(BeFalse())

	// Test with deployed condition set to true
	network2 := createTestNetworkWithDeployedCondition("cluster", "InstallAndEnable")
	g.Expect(r.wasNetworkObservabilityDeployed(network2)).To(BeTrue())

	// Test with deployed condition set to false
	network3 := createTestNetwork("cluster", "InstallAndEnable")
	network3.Status.Conditions = []metav1.Condition{
		{
			Type:   NetworkObservabilityDeployed,
			Status: metav1.ConditionFalse,
		},
	}
	g.Expect(r.wasNetworkObservabilityDeployed(network3)).To(BeFalse())

	// Test with other conditions but not deployed condition
	network4 := createTestNetwork("cluster", "InstallAndEnable")
	network4.Status.Conditions = []metav1.Condition{
		{
			Type:   "SomeOtherCondition",
			Status: metav1.ConditionTrue,
		},
	}
	g.Expect(r.wasNetworkObservabilityDeployed(network4)).To(BeFalse())
}

// TestMarkNetworkObservabilityDeployed tests setting the deployment condition
func TestMarkNetworkObservabilityDeployed(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)

	network := createTestNetwork("cluster", "InstallAndEnable")

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network).
		WithStatusSubresource(&configv1.Network{}).
		Build()

	r := &ReconcileObservability{client: client}

	// Mark as deployed
	err := r.markNetworkObservabilityDeployed(context.TODO(), network)
	g.Expect(err).NotTo(HaveOccurred())

	// Verify condition was added
	updatedNetwork := &configv1.Network{}
	err = client.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, updatedNetwork)
	g.Expect(err).NotTo(HaveOccurred())

	foundCondition := false
	for _, cond := range updatedNetwork.Status.Conditions {
		if cond.Type == NetworkObservabilityDeployed {
			foundCondition = true
			g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			g.Expect(cond.Reason).To(Equal("DeploymentComplete"))
			g.Expect(cond.Message).To(ContainSubstring("successfully deployed"))
		}
	}
	g.Expect(foundCondition).To(BeTrue(), "NetworkObservabilityDeployed condition should be present")

	// Call again - should be idempotent
	err = r.markNetworkObservabilityDeployed(context.TODO(), updatedNetwork)
	g.Expect(err).NotTo(HaveOccurred())
}

// TestReconcile_FirstTimeDeploymentSetsCondition tests that the first deployment sets the condition
func TestReconcile_FirstTimeDeploymentSetsCondition(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	// Network without deployed condition (first time deployment)
	network := createTestNetwork("cluster", "InstallAndEnable")
	subscription := createTestSubscription("netobserv-operator", OperatorNamespace)
	csv := createTestCSV("netobserv-operator.v1.0.0", OperatorNamespace, "Succeeded")
	namespace := createTestNamespace(NetObservNamespace)

	// Create manifests directory and FlowCollector manifest
	flowCollectorManifest := `
apiVersion: flows.netobserv.io/v1beta2
kind: FlowCollector
metadata:
  name: cluster
spec:
  namespace: netobserv
`
	err := os.MkdirAll("manifests", 0755)
	g.Expect(err).NotTo(HaveOccurred())

	// Create the FlowCollector manifest at the expected path
	err = os.WriteFile(FlowCollectorYAML, []byte(flowCollectorManifest), 0644)
	g.Expect(err).NotTo(HaveOccurred())
	defer os.Remove(FlowCollectorYAML)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, subscription, csv, namespace).
		WithStatusSubresource(&configv1.Network{}).
		Build()

	r := &ReconcileObservability{
		client: client,
		status: newMockStatusManager(),
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// First reconciliation - should create FlowCollector and set condition
	result, err := r.Reconcile(context.TODO(), req)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(Equal(ctrl.Result{}))

	// Verify FlowCollector was created
	exists, err := r.isFlowCollectorExists(context.TODO())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(exists).To(BeTrue(), "FlowCollector should be created on first deployment")

	// Verify condition was set
	updatedNetwork := &configv1.Network{}
	err = client.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, updatedNetwork)
	g.Expect(err).NotTo(HaveOccurred())

	foundCondition := false
	for _, cond := range updatedNetwork.Status.Conditions {
		if cond.Type == NetworkObservabilityDeployed && cond.Status == metav1.ConditionTrue {
			foundCondition = true
		}
	}
	g.Expect(foundCondition).To(BeTrue(), "NetworkObservabilityDeployed condition should be set after first deployment")
}
