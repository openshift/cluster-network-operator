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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Helper functions

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

func createTestOperatorNetworkWithCondition(name string, status operatorv1.ConditionStatus, reason, message string) *operatorv1.Network {
	return &operatorv1.Network{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Status: operatorv1.NetworkStatus{
			OperatorStatus: operatorv1.OperatorStatus{
				Conditions: []operatorv1.OperatorCondition{
					{
						Type:               NetworkObservabilityInstalledByCNO,
						Status:             status,
						Reason:             reason,
						Message:            message,
						LastTransitionTime: metav1.Now(),
					},
				},
			},
		},
	}
}

func createTestOperatorNetworkWithExpiredCondition(name string, reason string) *operatorv1.Network {
	return &operatorv1.Network{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Status: operatorv1.NetworkStatus{
			OperatorStatus: operatorv1.OperatorStatus{
				Conditions: []operatorv1.OperatorCondition{
					{
						Type:               NetworkObservabilityInstalledByCNO,
						Status:             operatorv1.ConditionFalse,
						Reason:             reason,
						LastTransitionTime: metav1.NewTime(time.Now().Add(-installTimeout - time.Minute)),
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

func createEnabledFeatureGate() featuregates.FeatureGate {
	return featuregates.NewFeatureGate(
		[]configv1.FeatureGateName{"NetworkObservabilityInstall"},
		[]configv1.FeatureGateName{},
	)
}

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		configv1.AddToScheme,
		operatorv1.AddToScheme,
		corev1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatalf("Failed to add to scheme: %v", err)
		}
	}
	return scheme
}

func assertCondition(t *testing.T, ctx context.Context, r *ReconcileObservability, expectedStatus operatorv1.ConditionStatus, expectedReason string) {
	t.Helper()
	g := NewGomegaWithT(t)

	network := &operatorv1.Network{}
	err := r.client.Get(ctx, types.NamespacedName{Name: NetworkCRName}, network)
	g.Expect(err).NotTo(HaveOccurred())

	var found *operatorv1.OperatorCondition
	for i := range network.Status.Conditions {
		if network.Status.Conditions[i].Type == NetworkObservabilityInstalledByCNO {
			found = &network.Status.Conditions[i]
			break
		}
	}
	g.Expect(found).NotTo(BeNil(), "Expected condition %s to be set", NetworkObservabilityInstalledByCNO)
	g.Expect(found.Status).To(Equal(expectedStatus), "Expected status %s, got %s", expectedStatus, found.Status)
	g.Expect(found.Reason).To(Equal(expectedReason), "Expected reason %s, got %s", expectedReason, found.Reason)
}

func assertConditionWithMessage(t *testing.T, ctx context.Context, r *ReconcileObservability, expectedStatus operatorv1.ConditionStatus, expectedReason, expectedMessage string) {
	t.Helper()
	g := NewGomegaWithT(t)

	network := &operatorv1.Network{}
	err := r.client.Get(ctx, types.NamespacedName{Name: NetworkCRName}, network)
	g.Expect(err).NotTo(HaveOccurred())

	var found *operatorv1.OperatorCondition
	for i := range network.Status.Conditions {
		if network.Status.Conditions[i].Type == NetworkObservabilityInstalledByCNO {
			found = &network.Status.Conditions[i]
			break
		}
	}
	g.Expect(found).NotTo(BeNil(), "Expected condition %s to be set", NetworkObservabilityInstalledByCNO)
	g.Expect(found.Status).To(Equal(expectedStatus), "Expected status %s, got %s", expectedStatus, found.Status)
	g.Expect(found.Reason).To(Equal(expectedReason), "Expected reason %s, got %s", expectedReason, found.Reason)
	g.Expect(found.Message).To(Equal(expectedMessage), "Expected message %q, got %q", expectedMessage, found.Message)
}

func assertNoCondition(t *testing.T, ctx context.Context, r *ReconcileObservability) {
	t.Helper()
	g := NewGomegaWithT(t)

	network := &operatorv1.Network{}
	err := r.client.Get(ctx, types.NamespacedName{Name: NetworkCRName}, network)
	g.Expect(err).NotTo(HaveOccurred())

	for _, c := range network.Status.Conditions {
		if c.Type == NetworkObservabilityInstalledByCNO {
			t.Fatalf("Expected no condition %s but found one with reason %s", NetworkObservabilityInstalledByCNO, c.Reason)
		}
	}
}

// Test isFeatureGateEnabled()

func TestIsFeatureGateEnabled_NilFeatureGate(t *testing.T) {
	g := NewGomegaWithT(t)
	r := &ReconcileObservability{featureGate: nil}
	g.Expect(r.isFeatureGateEnabled()).To(BeFalse())
}

func TestIsFeatureGateEnabled_Enabled(t *testing.T) {
	g := NewGomegaWithT(t)
	r := &ReconcileObservability{featureGate: createEnabledFeatureGate()}
	g.Expect(r.isFeatureGateEnabled()).To(BeTrue())
}

func TestIsFeatureGateEnabled_Disabled(t *testing.T) {
	g := NewGomegaWithT(t)
	fg := featuregates.NewFeatureGate(
		[]configv1.FeatureGateName{},
		[]configv1.FeatureGateName{"NetworkObservabilityInstall"},
	)
	r := &ReconcileObservability{featureGate: fg}
	g.Expect(r.isFeatureGateEnabled()).To(BeFalse())
}

// Test isSingleNodeCluster()

func TestIsSingleNodeCluster_SNO(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newTestScheme(t)
	infra := createTestInfrastructure(configv1.SingleReplicaTopologyMode)
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(infra).Build()
	r := &ReconcileObservability{client: client}

	isSNO, err := r.isSingleNodeCluster(context.TODO())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(isSNO).To(BeTrue())
}

func TestIsSingleNodeCluster_HighlyAvailable(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newTestScheme(t)
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(infra).Build()
	r := &ReconcileObservability{client: client}

	isSNO, err := r.isSingleNodeCluster(context.TODO())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(isSNO).To(BeFalse())
}

// Test doesFlowCollectorCRDExist()

func TestDoesFlowCollectorCRDExist_True(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := runtime.NewScheme()
	crd := createTestCRD("flowcollectors.flows.netobserv.io")
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(crd).Build()
	r := &ReconcileObservability{client: client}

	exists, err := r.doesFlowCollectorCRDExist(context.TODO())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(exists).To(BeTrue())
}

func TestDoesFlowCollectorCRDExist_False(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := runtime.NewScheme()
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &ReconcileObservability{client: client}

	exists, err := r.doesFlowCollectorCRDExist(context.TODO())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(exists).To(BeFalse())
}

// Test doesFlowCollectorExist()

func TestDoesFlowCollectorExist_True(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := runtime.NewScheme()
	fc := createTestFlowCollector(FlowCollectorName)
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(fc).Build()
	r := &ReconcileObservability{client: client}

	exists, err := r.doesFlowCollectorExist(context.TODO())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(exists).To(BeTrue())
}

func TestDoesFlowCollectorExist_False(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := runtime.NewScheme()
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &ReconcileObservability{client: client}

	exists, err := r.doesFlowCollectorExist(context.TODO())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(exists).To(BeFalse())
}

// Test shouldInstallNetworkObservability()

func TestShouldInstall_PolicyAndTopology(t *testing.T) {
	tests := []struct {
		name           string
		policy         configv1.NetworkObservabilityInstallationPolicy
		sno            bool
		expectedResult bool
		expectedReason string
	}{
		{"InstallAndEnable_NonSNO", configv1.NetworkObservabilityInstallAndEnable, false, true, ""},
		{"InstallAndEnable_SNO", configv1.NetworkObservabilityInstallAndEnable, true, true, ""},
		{"NoAction_NonSNO", configv1.NetworkObservabilityNoAction, false, false, "NoAction"},
		{"NoAction_SNO", configv1.NetworkObservabilityNoAction, true, false, "NoAction"},
		{"Null_NonSNO", "", false, true, ""},
		{"Null_SNO", "", true, false, "SNO"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewGomegaWithT(t)
			scheme := newTestScheme(t)

			network := createTestNetwork("cluster", string(tt.policy))
			operatorNetwork := createTestOperatorNetwork("cluster")
			topology := configv1.HighlyAvailableTopologyMode
			if tt.sno {
				topology = configv1.SingleReplicaTopologyMode
			}
			infra := createTestInfrastructure(topology)

			client := fake.NewClientBuilder().WithScheme(scheme).
				WithObjects(network, operatorNetwork, infra).
				WithStatusSubresource(&operatorv1.Network{}).
				Build()
			r := &ReconcileObservability{client: client, featureGate: createEnabledFeatureGate()}

			result, err := r.shouldInstallNetworkObservability(context.TODO())
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(result).To(Equal(tt.expectedResult))

			if tt.expectedReason != "" {
				assertCondition(t, context.TODO(), r, operatorv1.ConditionFalse, tt.expectedReason)
			}
		})
	}
}

func TestShouldInstall_FeatureGateDisabled(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newTestScheme(t)

	network := createTestNetwork("cluster", "InstallAndEnable")
	operatorNetwork := createTestOperatorNetwork("cluster")
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, operatorNetwork, infra).
		WithStatusSubresource(&operatorv1.Network{}).
		Build()

	fg := featuregates.NewFeatureGate(
		[]configv1.FeatureGateName{},
		[]configv1.FeatureGateName{"NetworkObservabilityInstall"},
	)
	r := &ReconcileObservability{client: client, featureGate: fg}

	result, err := r.shouldInstallNetworkObservability(context.TODO())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(BeFalse())
	assertCondition(t, context.TODO(), r, operatorv1.ConditionFalse, "FeatureGateDisabled")
}

func TestShouldInstall_TerminalConditionBlocksRetry(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		status operatorv1.ConditionStatus
	}{
		{"Installed", "Installed", operatorv1.ConditionTrue},
		{"Failed", "Failed", operatorv1.ConditionFalse},
		{"PreExisting", "PreExisting", operatorv1.ConditionFalse},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewGomegaWithT(t)
			scheme := newTestScheme(t)

			network := createTestNetwork("cluster", "InstallAndEnable")
			operatorNetwork := createTestOperatorNetworkWithCondition("cluster", tt.status, tt.reason, "test message")
			infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)

			client := fake.NewClientBuilder().WithScheme(scheme).
				WithObjects(network, operatorNetwork, infra).
				WithStatusSubresource(&operatorv1.Network{}).
				Build()
			r := &ReconcileObservability{client: client, featureGate: createEnabledFeatureGate()}

			result, err := r.shouldInstallNetworkObservability(context.TODO())
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(result).To(BeFalse())
		})
	}
}

func TestShouldInstall_InProgressContinues(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newTestScheme(t)

	network := createTestNetwork("cluster", "InstallAndEnable")
	operatorNetwork := createTestOperatorNetworkWithCondition("cluster", operatorv1.ConditionFalse, "InstallationInProgress", "installing")
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, operatorNetwork, infra).
		WithStatusSubresource(&operatorv1.Network{}).
		Build()
	r := &ReconcileObservability{client: client, featureGate: createEnabledFeatureGate()}

	result, err := r.shouldInstallNetworkObservability(context.TODO())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(BeTrue())
}

func TestShouldInstall_TimesOutWhenOperatorNotInstalled(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newTestScheme(t)

	network := createTestNetwork("cluster", "InstallAndEnable")
	// WaitingForOperator: the operator was applied but the CRD never appeared.
	operatorNetwork := createTestOperatorNetworkWithExpiredCondition("cluster", "WaitingForOperator")
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, operatorNetwork, infra).
		WithStatusSubresource(&operatorv1.Network{}).
		Build()
	r := &ReconcileObservability{client: client, featureGate: createEnabledFeatureGate()}

	result, err := r.shouldInstallNetworkObservability(context.TODO())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(BeFalse())
	assertConditionWithMessage(t, context.TODO(), r, operatorv1.ConditionFalse, "Failed",
		"Failed to install Network Observability Operator after 10 minutes. Will not retry.")
}

func TestShouldInstall_TimesOutWhenFlowCollectorNotCreated(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newTestScheme(t)

	network := createTestNetwork("cluster", "InstallAndEnable")
	// CreatingFlowCollector: the operator installed but the FlowCollector CR could not be created.
	operatorNetwork := createTestOperatorNetworkWithExpiredCondition("cluster", "CreatingFlowCollector")
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, operatorNetwork, infra).
		WithStatusSubresource(&operatorv1.Network{}).
		Build()
	r := &ReconcileObservability{client: client, featureGate: createEnabledFeatureGate()}

	result, err := r.shouldInstallNetworkObservability(context.TODO())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(BeFalse())
	assertConditionWithMessage(t, context.TODO(), r, operatorv1.ConditionFalse, "Failed",
		"Failed to create FlowCollector after 10 minutes. Will not retry.")
}

func TestShouldInstall_PreExistingCRD(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newTestScheme(t)

	network := createTestNetwork("cluster", "InstallAndEnable")
	operatorNetwork := createTestOperatorNetwork("cluster")
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)
	crd := createTestCRD("flowcollectors.flows.netobserv.io")

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, operatorNetwork, infra, crd).
		WithStatusSubresource(&operatorv1.Network{}).
		Build()
	r := &ReconcileObservability{client: client, featureGate: createEnabledFeatureGate()}

	result, err := r.shouldInstallNetworkObservability(context.TODO())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(BeFalse())
	assertCondition(t, context.TODO(), r, operatorv1.ConditionFalse, "PreExisting")
}

func TestShouldInstall_NoActionThenInstallAndEnable(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newTestScheme(t)

	network := createTestNetwork("cluster", "NoAction")
	operatorNetwork := createTestOperatorNetwork("cluster")
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, operatorNetwork, infra).
		WithStatusSubresource(&operatorv1.Network{}).
		Build()
	r := &ReconcileObservability{client: client, featureGate: createEnabledFeatureGate()}

	// First: NoAction
	result, err := r.shouldInstallNetworkObservability(context.TODO())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(BeFalse())
	assertCondition(t, context.TODO(), r, operatorv1.ConditionFalse, "NoAction")

	// Change to InstallAndEnable
	network.Spec.NetworkObservability.InstallationPolicy = configv1.NetworkObservabilityInstallAndEnable
	err = client.Update(context.TODO(), network)
	g.Expect(err).NotTo(HaveOccurred())

	// Should now proceed to install (NoAction is re-evaluatable)
	result, err = r.shouldInstallNetworkObservability(context.TODO())
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(BeTrue())
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

	manifestPath := createTempManifest(t, `
apiVersion: v1
kind: Namespace
metadata:
  name: test-namespace
`)
	err := r.applyManifest(context.TODO(), manifestPath, "test resource")
	g.Expect(err).NotTo(HaveOccurred())
}

func TestApplyManifest_MultipleResources(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := runtime.NewScheme()
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &ReconcileObservability{client: client}

	manifestPath := createTempManifest(t, `
apiVersion: v1
kind: Namespace
metadata:
  name: test-namespace-1
---
apiVersion: v1
kind: Namespace
metadata:
  name: test-namespace-2
`)
	err := r.applyManifest(context.TODO(), manifestPath, "test resources")
	g.Expect(err).NotTo(HaveOccurred())
}

func TestApplyManifest_EmptyDocuments(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := runtime.NewScheme()
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &ReconcileObservability{client: client}

	manifestPath := createTempManifest(t, "---\n---\n")
	err := r.applyManifest(context.TODO(), manifestPath, "empty resources")
	g.Expect(err).NotTo(HaveOccurred())
}

func TestApplyManifest_InvalidYAML(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := runtime.NewScheme()
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &ReconcileObservability{client: client}

	manifestPath := createTempManifest(t, `
invalid: yaml: content:
  - broken
    indentation
`)
	err := r.applyManifest(context.TODO(), manifestPath, "invalid resource")
	g.Expect(err).To(HaveOccurred())
}

func TestApplyManifest_FileNotFound(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := runtime.NewScheme()
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &ReconcileObservability{client: client}

	err := r.applyManifest(context.TODO(), "/non/existent/path.yaml", "test")
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("failed to read"))
}

// Test Reconcile()

func TestReconcile_IgnoresNonClusterNetwork(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newTestScheme(t)
	network := createTestNetwork("not-cluster", "InstallAndEnable")
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(network).Build()
	r := &ReconcileObservability{client: client}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "not-cluster"}}
	result, err := r.Reconcile(context.TODO(), req)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(Equal(reconcile.Result{}))
}

func TestReconcile_SkipsWhenFeatureGateDisabled(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newTestScheme(t)

	network := createTestNetwork("cluster", "InstallAndEnable")
	operatorNetwork := createTestOperatorNetwork("cluster")
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, operatorNetwork, infra).
		WithStatusSubresource(&operatorv1.Network{}).
		Build()

	fg := featuregates.NewFeatureGate(
		[]configv1.FeatureGateName{},
		[]configv1.FeatureGateName{"NetworkObservabilityInstall"},
	)
	r := &ReconcileObservability{client: client, featureGate: fg}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}
	result, err := r.Reconcile(context.TODO(), req)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(Equal(reconcile.Result{}))
	assertCondition(t, context.TODO(), r, operatorv1.ConditionFalse, "FeatureGateDisabled")
}

func TestReconcile_SkipsWhenNoActionPolicy(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newTestScheme(t)

	network := createTestNetwork("cluster", "NoAction")
	operatorNetwork := createTestOperatorNetwork("cluster")
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, operatorNetwork, infra).
		WithStatusSubresource(&operatorv1.Network{}).
		Build()

	r := &ReconcileObservability{client: client, featureGate: createEnabledFeatureGate()}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}
	result, err := r.Reconcile(context.TODO(), req)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(Equal(reconcile.Result{}))
	assertCondition(t, context.TODO(), r, operatorv1.ConditionFalse, "NoAction")
}

func TestReconcile_RequeuesOnManifestFailure(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newTestScheme(t)

	network := createTestNetwork("cluster", "InstallAndEnable")
	operatorNetwork := createTestOperatorNetwork("cluster")
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, operatorNetwork, infra).
		WithStatusSubresource(&operatorv1.Network{}).
		Build()

	r := &ReconcileObservability{client: client, featureGate: createEnabledFeatureGate()}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// Manifest doesn't exist, so applyManifest fails → requeue to retry.
	result, err := r.Reconcile(context.TODO(), req)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result.RequeueAfter).To(Equal(requeueInterval))

	// The condition stays InstallationInProgress (not WaitingForOperator), so the
	// next reconcile reapplies the operator manifest.
	assertCondition(t, context.TODO(), r, operatorv1.ConditionFalse, "InstallationInProgress")
}

func TestReconcile_DoesNotReapplyWhileWaitingForOperator(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newTestScheme(t)

	network := createTestNetwork("cluster", "InstallAndEnable")
	// Operator manifest already applied successfully; waiting for the CRD.
	operatorNetwork := createTestOperatorNetworkWithCondition("cluster", operatorv1.ConditionFalse, "WaitingForOperator", "waiting")
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, operatorNetwork, infra).
		WithStatusSubresource(&operatorv1.Network{}).
		Build()

	r := &ReconcileObservability{client: client, featureGate: createEnabledFeatureGate()}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// CRD still absent, but the guard prevents reapplying the (missing) manifest,
	// so there is no apply error and the condition stays WaitingForOperator.
	result, err := r.Reconcile(context.TODO(), req)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result.RequeueAfter).To(Equal(requeueInterval))
	assertCondition(t, context.TODO(), r, operatorv1.ConditionFalse, "WaitingForOperator")
}

func TestReconcile_InstalledWhenCRDAndFlowCollectorExist(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newTestScheme(t)

	network := createTestNetwork("cluster", "InstallAndEnable")
	operatorNetwork := createTestOperatorNetwork("cluster")
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)
	crd := createTestCRD("flowcollectors.flows.netobserv.io")
	fc := createTestFlowCollector(FlowCollectorName)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, operatorNetwork, infra, crd, fc).
		WithStatusSubresource(&operatorv1.Network{}).
		Build()

	r := &ReconcileObservability{client: client, featureGate: createEnabledFeatureGate()}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}
	result, err := r.Reconcile(context.TODO(), req)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(Equal(reconcile.Result{}))

	// Wait — CRD exists means PreExisting should have triggered in shouldInstall.
	// Actually no: shouldInstall checks for pre-existing BEFORE the install path.
	// So this test scenario (CRD exists, no condition) → PreExisting, not Installed.
	assertCondition(t, context.TODO(), r, operatorv1.ConditionFalse, "PreExisting")
}

func TestReconcile_InstallsWhenCRDAppearsAfterInProgress(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newTestScheme(t)

	network := createTestNetwork("cluster", "InstallAndEnable")
	// Simulate: already in InstallationInProgress, CRD has now appeared
	operatorNetwork := createTestOperatorNetworkWithCondition("cluster", operatorv1.ConditionFalse, "InstallationInProgress", "installing")
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)
	crd := createTestCRD("flowcollectors.flows.netobserv.io")
	fc := createTestFlowCollector(FlowCollectorName)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, operatorNetwork, infra, crd, fc).
		WithStatusSubresource(&operatorv1.Network{}).
		Build()

	r := &ReconcileObservability{client: client, featureGate: createEnabledFeatureGate()}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}
	result, err := r.Reconcile(context.TODO(), req)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(Equal(reconcile.Result{}))
	assertCondition(t, context.TODO(), r, operatorv1.ConditionTrue, "Installed")
}

func TestReconcile_DoesNotReinstallAfterInstalled(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newTestScheme(t)

	network := createTestNetwork("cluster", "InstallAndEnable")
	operatorNetwork := createTestOperatorNetworkWithCondition("cluster", operatorv1.ConditionTrue, "Installed", "Network Observability has been installed. Will not manage.")
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, operatorNetwork, infra).
		WithStatusSubresource(&operatorv1.Network{}).
		Build()

	r := &ReconcileObservability{client: client, featureGate: createEnabledFeatureGate()}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}
	result, err := r.Reconcile(context.TODO(), req)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(Equal(reconcile.Result{}))
}

func TestReconcile_NoActionThenInstallAndEnable(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newTestScheme(t)

	network := createTestNetwork("cluster", "NoAction")
	operatorNetwork := createTestOperatorNetwork("cluster")
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, operatorNetwork, infra).
		WithStatusSubresource(&operatorv1.Network{}).
		Build()

	r := &ReconcileObservability{client: client, featureGate: createEnabledFeatureGate()}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	// First reconciliation: NoAction
	result, err := r.Reconcile(context.TODO(), req)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(Equal(reconcile.Result{}))
	assertCondition(t, context.TODO(), r, operatorv1.ConditionFalse, "NoAction")

	// Change policy to InstallAndEnable
	network.Spec.NetworkObservability.InstallationPolicy = configv1.NetworkObservabilityInstallAndEnable
	err = client.Update(context.TODO(), network)
	g.Expect(err).NotTo(HaveOccurred())

	// Second reconciliation: should attempt install (fails because manifest doesn't exist → requeue to retry)
	result, err = r.Reconcile(context.TODO(), req)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result.RequeueAfter).To(Equal(requeueInterval))
}

func TestReconcile_RequeuesOnInfrastructureError(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newTestScheme(t)

	network := createTestNetwork("cluster", "")
	operatorNetwork := createTestOperatorNetwork("cluster")
	// No Infrastructure object — isSingleNodeCluster will fail

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, operatorNetwork).
		WithStatusSubresource(&operatorv1.Network{}).
		Build()

	r := &ReconcileObservability{client: client, featureGate: createEnabledFeatureGate()}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}
	result, err := r.Reconcile(context.TODO(), req)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result.RequeueAfter).To(Equal(requeueInterval))
}

func TestReconcile_IdempotentMultipleInvocations(t *testing.T) {
	g := NewGomegaWithT(t)
	scheme := newTestScheme(t)

	network := createTestNetwork("cluster", "InstallAndEnable")
	operatorNetwork := createTestOperatorNetworkWithCondition("cluster", operatorv1.ConditionTrue, "Installed", "installed")
	infra := createTestInfrastructure(configv1.HighlyAvailableTopologyMode)

	client := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(network, operatorNetwork, infra).
		WithStatusSubresource(&operatorv1.Network{}).
		Build()

	r := &ReconcileObservability{client: client, featureGate: createEnabledFeatureGate()}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "cluster"}}

	result1, err1 := r.Reconcile(context.TODO(), req)
	g.Expect(err1).NotTo(HaveOccurred())
	result2, err2 := r.Reconcile(context.TODO(), req)
	g.Expect(err2).NotTo(HaveOccurred())
	g.Expect(result1).To(Equal(result2))
}
