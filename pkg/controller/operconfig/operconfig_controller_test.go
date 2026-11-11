package operconfig

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
