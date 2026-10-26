package operconfig

import (
	"testing"

	"github.com/openshift/cluster-network-operator/pkg/hypershift"
	"github.com/openshift/cluster-network-operator/pkg/names"

	operv1 "github.com/openshift/api/operator/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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

// TestSetGCOwnerReference verifies that the ownerReference is set on the
// Network operconfig for default-cluster objects and on the HostedControlPlane
// for management-cluster objects rendered into the HCP namespace, while all
// other cluster-targeted objects are left untouched.
func TestSetGCOwnerReference(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := operv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to register operator scheme: %v", err)
	}
	operConfig := &operv1.Network{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster", UID: types.UID("operconfig-uid")},
	}
	hcp := &hypershift.HostedControlPlane{
		Name:      "test-hc",
		UID:       types.UID("hcp-uid"),
		Namespace: "clusters-test-hc",
	}

	// makeObj returns a ServiceMonitor-like object targeted at the given
	// cluster and namespace.
	makeObj := func(clusterName, namespace string) *unstructured.Unstructured {
		obj := &unstructured.Unstructured{}
		obj.SetAPIVersion("monitoring.coreos.com/v1")
		obj.SetKind("ServiceMonitor")
		obj.SetName("monitor-ovn-control-plane-metrics")
		obj.SetNamespace(namespace)
		if clusterName != "" {
			obj.SetAnnotations(map[string]string{names.ClusterNameAnnotation: clusterName})
		}
		return obj
	}

	t.Run("management-cluster object in HCP namespace gets HCP ownerReference", func(t *testing.T) {
		obj := makeObj(names.ManagementClusterName, hcp.Namespace)
		if err := setGCOwnerReference(operConfig, obj, hcp, scheme); err != nil {
			t.Fatalf("failed to set HCP ownerReference on management-cluster object: %v", err)
		}
		ownerRefs := obj.GetOwnerReferences()
		if len(ownerRefs) != 1 {
			t.Fatalf("expected 1 ownerReference, got %d", len(ownerRefs))
		}
		want := metav1.OwnerReference{
			APIVersion: "hypershift.openshift.io/v1beta1",
			Kind:       "HostedControlPlane",
			Name:       "test-hc",
			UID:        types.UID("hcp-uid"),
		}
		if ownerRefs[0] != want {
			t.Errorf("expected ownerReference %+v, got %+v", want, ownerRefs[0])
		}
	})

	t.Run("management-cluster object outside HCP namespace is untouched", func(t *testing.T) {
		obj := makeObj(names.ManagementClusterName, "openshift-ovn-kubernetes")
		if err := setGCOwnerReference(operConfig, obj, hcp, scheme); err != nil {
			t.Fatalf("failed to set ownerReference on object outside HCP namespace: %v", err)
		}
		if refs := obj.GetOwnerReferences(); len(refs) != 0 {
			t.Errorf("expected no ownerReference, got %+v", refs)
		}
	})

	t.Run("guest-cluster object is untouched", func(t *testing.T) {
		obj := makeObj("guest-cluster", hcp.Namespace)
		if err := setGCOwnerReference(operConfig, obj, hcp, scheme); err != nil {
			t.Fatalf("failed to set ownerReference on guest-cluster object: %v", err)
		}
		if refs := obj.GetOwnerReferences(); len(refs) != 0 {
			t.Errorf("expected no ownerReference, got %+v", refs)
		}
	})

	t.Run("management-cluster object untouched when no HostedControlPlane", func(t *testing.T) {
		obj := makeObj(names.ManagementClusterName, "clusters-test-hc")
		if err := setGCOwnerReference(operConfig, obj, nil, scheme); err != nil {
			t.Fatalf("failed to set ownerReference with no HostedControlPlane: %v", err)
		}
		if refs := obj.GetOwnerReferences(); len(refs) != 0 {
			t.Errorf("expected no ownerReference, got %+v", refs)
		}
	})

	t.Run("default-cluster object gets operconfig ownerReference", func(t *testing.T) {
		obj := makeObj("", "openshift-ovn-kubernetes")
		if err := setGCOwnerReference(operConfig, obj, hcp, scheme); err != nil {
			t.Fatalf("failed to set operconfig ownerReference on default-cluster object: %v", err)
		}
		ownerRefs := obj.GetOwnerReferences()
		if len(ownerRefs) != 1 {
			t.Fatalf("expected 1 ownerReference, got %d", len(ownerRefs))
		}
		if ownerRefs[0].Name != "cluster" || ownerRefs[0].UID != types.UID("operconfig-uid") {
			t.Errorf("expected operconfig ownerReference, got %+v", ownerRefs[0])
		}
		if ownerRefs[0].Controller == nil || !*ownerRefs[0].Controller {
			t.Errorf("expected controller flag set on operconfig ownerReference, got %+v", ownerRefs[0])
		}
	})
}
