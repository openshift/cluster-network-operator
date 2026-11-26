package operconfig

import (
	"encoding/json"
	"testing"

	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/client/fake"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/cluster-network-operator/pkg/names"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

// TestNamespacePredicate verifies that only changes affecting the Multus
// admission controller's ignore set trigger reconciliation.
func TestNamespacePredicate(t *testing.T) {
	p := namespacePredicate()

	managedNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "open-cluster-management-agent-addon",
		Labels: map[string]string{
			"openshift.io/cluster-monitoring": "true",
		},
		Annotations: map[string]string{
			"workload.openshift.io/allowed": "management",
		},
	}}
	otherManagedNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "openshift-monitoring",
		Labels: map[string]string{
			"openshift.io/cluster-monitoring": "true",
		},
		Annotations: map[string]string{
			"workload.openshift.io/allowed": "management",
		},
	}}
	userNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "user-namespace",
		Labels: map[string]string{
			"openshift.io/cluster-monitoring": "true",
		},
	}}

	if !p.Create(event.CreateEvent{Object: managedNamespace}) {
		t.Error("managed namespace create should trigger reconciliation")
	}
	if p.Create(event.CreateEvent{Object: userNamespace}) {
		t.Error("user namespace create should not trigger reconciliation")
	}

	if !p.Update(event.UpdateEvent{ObjectOld: userNamespace, ObjectNew: managedNamespace}) {
		t.Error("namespace entering the ignore set should trigger reconciliation")
	}
	if !p.Update(event.UpdateEvent{ObjectOld: managedNamespace, ObjectNew: userNamespace}) {
		t.Error("namespace leaving the ignore set should trigger reconciliation")
	}
	if p.Update(event.UpdateEvent{ObjectOld: managedNamespace, ObjectNew: otherManagedNamespace}) {
		t.Error("irrelevant update to an ignored namespace should not trigger reconciliation")
	}

	if !p.Delete(event.DeleteEvent{Object: managedNamespace}) {
		t.Error("managed namespace deletion should trigger reconciliation")
	}
	if p.Delete(event.DeleteEvent{Object: &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "user-namespace"}}}) {
		t.Error("user namespace deletion should not trigger reconciliation")
	}
}

// TestApplyObjectsSkipsControllerReferenceOnCRDs verifies that the apply path
// marks ordinary objects for garbage collection with the Network CR but leaves
// CRDs unowned, so that deleting the Network CR can never cascade into
// deleting a CRD and all of its CRs. The fake dynamic client cannot evaluate
// server-side apply patches, so the test records the apply body sent to the
// server and asserts on that.
func TestApplyObjectsSkipsControllerReferenceOnCRDs(t *testing.T) {
	operConfig := &operv1.Network{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
			UID:  "test-uid",
		},
	}

	tests := []struct {
		name           string
		obj            *uns.Unstructured
		expectOwnerRef bool
	}{
		{
			name: "Deployment gets controller ownerReference",
			obj: &uns.Unstructured{
				Object: map[string]any{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]any{
						"name":      "test-deployment",
						"namespace": "openshift-network-operator",
					},
				},
			},
			expectOwnerRef: true,
		},
		{
			name: "CRD does not get controller ownerReference",
			obj: &uns.Unstructured{
				Object: map[string]any{
					"apiVersion": "apiextensions.k8s.io/v1",
					"kind":       "CustomResourceDefinition",
					"metadata": map[string]any{
						"name": "frrconfigurations.frrk8s.metallb.io",
					},
				},
			},
			expectOwnerRef: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := fake.NewFakeClient()
			var applied []*uns.Unstructured
			client.Default().Dynamic().(*fakedynamic.FakeDynamicClient).PrependReactor("patch", "*",
				func(action clienttesting.Action) (bool, runtime.Object, error) {
					patch := action.(clienttesting.PatchAction)
					if patch.GetPatchType() != types.ApplyPatchType {
						return false, nil, nil
					}
					obj := &uns.Unstructured{}
					if err := json.Unmarshal(patch.GetPatch(), &obj.Object); err != nil {
						return true, nil, err
					}
					applied = append(applied, obj)
					return true, obj, nil
				})

			r := &ReconcileOperConfig{
				client: client,
				status: statusmanager.New(client, "testing", names.StandAloneClusterName),
			}
			if err := r.applyObjects(t.Context(), operConfig, []*uns.Unstructured{tc.obj}); err != nil {
				t.Fatalf("applyObjects(%s %s/%s): %v", tc.obj.GetKind(), tc.obj.GetNamespace(), tc.obj.GetName(), err)
			}
			if len(applied) != 1 {
				t.Fatalf("%s %s/%s: expected 1 apply patch, got %d", tc.obj.GetKind(), tc.obj.GetNamespace(), tc.obj.GetName(), len(applied))
			}

			controllerRef := metav1.GetControllerOf(applied[0])
			if tc.expectOwnerRef && controllerRef == nil {
				t.Errorf("%s %s/%s: expected controller ownerReference in apply patch, got none", tc.obj.GetKind(), tc.obj.GetNamespace(), tc.obj.GetName())
			}
			if !tc.expectOwnerRef && controllerRef != nil {
				t.Errorf("%s %s/%s: expected no controller ownerReference in apply patch, got %v", tc.obj.GetKind(), tc.obj.GetNamespace(), tc.obj.GetName(), controllerRef)
			}
		})
	}
}

// TestIsCRD verifies that CRDs are identified by both group and kind.
func TestIsCRD(t *testing.T) {
	tests := []struct {
		name       string
		apiVersion string
		kind       string
		expected   bool
	}{
		{
			name:       "apiextensions CRD",
			apiVersion: "apiextensions.k8s.io/v1",
			kind:       "CustomResourceDefinition",
			expected:   true,
		},
		{
			name:       "non-CRD resource with same Kind in different group",
			apiVersion: "example.com/v1",
			kind:       "CustomResourceDefinition",
			expected:   false,
		},
		{
			name:       "Deployment",
			apiVersion: "apps/v1",
			kind:       "Deployment",
			expected:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obj := &uns.Unstructured{
				Object: map[string]any{
					"apiVersion": tc.apiVersion,
					"kind":       tc.kind,
					"metadata":   map[string]any{"name": "test"},
				},
			}
			if got := isCRD(obj); got != tc.expected {
				t.Errorf("isCRD() = %v, want %v", got, tc.expected)
			}
		})
	}
}
