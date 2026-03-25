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
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
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

func createTestClusterExtension(name string, installed bool) *unstructured.Unstructured {
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
	_ = unstructured.SetNestedSlice(ce.Object, conditions, "status", "conditions")
	return ce
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
	operatorNs := createTestNamespace(OperatorNamespace)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(network, infra, operatorNs).Build()

	r := &ReconcileObservability{
		client:      client,
		status:      newMockStatusManager(),
		featureGate: createEnabledFeatureGate(),
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}
	result, err := r.Reconcile(context.TODO(), req)

	// When nil, controller should try to install (opt-out behavior)
	// This will fail because the manifest doesn't exist, but it requeues instead of erroring
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.RequeueAfter).To(Equal(requeueAfterOLM))
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

	crd := createTestCRD("flowcollectors.flows.netobserv.io")

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(crd).Build()

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

	// Create multiple CRDs, but only the FlowCollector one should matter
	crd1 := createTestCRD("other-crds.example.com")
	crd2 := createTestCRD("flowcollectors.flows.netobserv.io")

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(crd1, crd2).Build()

	r := &ReconcileObservability{client: client}

	installed, err := r.isNetObservOperatorInstalled(context.TODO())

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(installed).To(BeTrue())
}

// Test waitForNetObservOperator()

func TestWaitForOperator_Success(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()

	clusterExtension := createTestClusterExtension("netobserv-operator", true)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(clusterExtension).Build()

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
	clusterExtension := createTestClusterExtension("netobserv-operator", false)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(clusterExtension).Build()

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
	clusterExtension := createTestClusterExtension("netobserv-operator", false)

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(clusterExtension).Build()

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
	crd := createTestCRD("flowcollectors.flows.netobserv.io")
	clusterExtension := createTestClusterExtension("netobserv-operator", true)
	flowCollector := createTestFlowCollector(FlowCollectorName)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, crd, clusterExtension, flowCollector).
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
	crd := createTestCRD("flowcollectors.flows.netobserv.io")
	clusterExtension := createTestClusterExtension("netobserv-operator", true)
	operatorNs := createTestNamespace(OperatorNamespace)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, crd, clusterExtension, operatorNs).
		Build()

	r := &ReconcileObservability{
		client:      client,
		status:      newMockStatusManager(),
		featureGate: createEnabledFeatureGate(),
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

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
	_ = configv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	network := createTestNetwork("cluster", "InstallAndEnable")
	crd := createTestCRD("flowcollectors.flows.netobserv.io")
	clusterExtension := createTestClusterExtension("netobserv-operator", true)
	flowCollector := createTestFlowCollector(FlowCollectorName)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, crd, clusterExtension, flowCollector).
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
	crd := createTestCRD("flowcollectors.flows.netobserv.io")
	// CSV exists but not in Succeeded phase
	clusterExtension := createTestClusterExtension("netobserv-operator", false)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, crd, clusterExtension).
		Build()

	r := &ReconcileObservability{
		client:      client,
		status:      newMockStatusManager(),
		featureGate: createEnabledFeatureGate(),
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result, err := r.Reconcile(ctx, req)

	// Controller returns no error, but should requeue after failing FlowCollector creation
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result.RequeueAfter).To(Equal(requeueAfterStandard))
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
	network := createTestNetwork("cluster", "InstallAndEnable")
	crd := createTestCRD("flowcollectors.flows.netobserv.io")
	clusterExtension := createTestClusterExtension("netobserv-operator", true)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, crd, clusterExtension).
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
	network := createTestNetwork("cluster", "InstallAndEnable")
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
	network := createTestNetwork("cluster", "InstallAndEnable")

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
		WithStatusSubresource(&configv1.Network{}).
		Build()

	r := &ReconcileObservability{
		client:      client,
		status:      newMockStatusManager(),
		featureGate: createEnabledFeatureGate(),
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
	_ = configv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	network := createTestNetwork("cluster", "InstallAndEnable")

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network).
		WithStatusSubresource(&configv1.Network{}).
		Build()

	r := &ReconcileObservability{
		client:      client,
		status:      newMockStatusManager(),
		featureGate: createEnabledFeatureGate(),
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

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
	_ = configv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	network := createTestNetwork("cluster", "InstallAndEnable")
	crd := createTestCRD("flowcollectors.flows.netobserv.io")
	// Start with CSV in Installing phase
	clusterExtension := createTestClusterExtension("netobserv-operator", false)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, crd, clusterExtension).
		Build()

	r := &ReconcileObservability{
		client:      client,
		status:      newMockStatusManager(),
		featureGate: createEnabledFeatureGate(),
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

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
	_ = unstructured.SetNestedSlice(clusterExtension.Object, conditions, "status", "conditions")
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
	_ = configv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	network := createTestNetwork("cluster", "InstallAndEnable")
	crd := createTestCRD("flowcollectors.flows.netobserv.io")
	clusterExtension := createTestClusterExtension("netobserv-operator", true)
	flowCollector := createTestFlowCollector(FlowCollectorName)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, crd, clusterExtension, flowCollector).
		WithStatusSubresource(&configv1.Network{}).
		Build()

	r := &ReconcileObservability{
		client: client,
		status: newMockStatusManager(),
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

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
		client:      client,
		status:      mockStatus,
		featureGate: createEnabledFeatureGate(),
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// Reconciliation should fail trying to install operator (manifest doesn't exist)
	result, err := r.Reconcile(context.TODO(), req)

	// Should not fail or set degraded status (errors are logged and requeued)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.RequeueAfter).To(Equal(requeueAfterOLM))

	// Verify that status degraded was NOT called (optional feature shouldn't degrade operator)
	g.Expect(len(mockStatus.degradedCalls)).To(Equal(0))
}

// TestReconcile_StatusNotDegradedOnSuccess tests that status is cleared on success
func TestReconcile_StatusNotDegradedOnSuccess(t *testing.T) {
	g := NewGomegaWithT(t)

	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	network := createTestNetwork("cluster", "InstallAndEnable")
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)
	crd := createTestCRD("flowcollectors.flows.netobserv.io")
	clusterExtension := createTestClusterExtension("netobserv-operator", true)
	flowCollector := createTestFlowCollector(FlowCollectorName)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, infra, crd, clusterExtension, flowCollector).
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

// TestReconcile_StatusDegradedOnInfrastructureError tests that Infrastructure lookup failures don't cause degraded status
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
		WithStatusSubresource(&configv1.Network{}).
		Build()

	mockStatus := newMockStatusManager()
	r := &ReconcileObservability{
		client:      client,
		status:      mockStatus,
		featureGate: createEnabledFeatureGate(),
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// Reconciliation should requeue when checking Infrastructure fails
	result, err := r.Reconcile(context.TODO(), req)

	// Should not fail or set degraded status (errors are logged and requeued)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(result.RequeueAfter).To(Equal(requeueAfterStandard))

	// Verify that status degraded was NOT called (optional feature shouldn't degrade operator)
	g.Expect(len(mockStatus.degradedCalls)).To(Equal(0))
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

func TestIsFeatureGateEnabled_FeatureGateNotRegistered(t *testing.T) {
	g := NewGomegaWithT(t)

	// Create a feature gate without NetworkObservabilityInstall registered
	fg := featuregates.NewFeatureGate(
		[]configv1.FeatureGateName{"SomeOtherFeature"},
		[]configv1.FeatureGateName{},
	)

	r := &ReconcileObservability{featureGate: fg}

	// Should default to disabled when feature gate is not registered
	result := r.isFeatureGateEnabled()
	g.Expect(result).To(BeFalse())
}
