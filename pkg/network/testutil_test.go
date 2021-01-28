package network

import (
	"fmt"

	"github.com/onsi/gomega/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Fun matcher for testing the presence of Kubernetes objects
func HaveKubernetesID(kind, namespace, name string) types.GomegaMatcher {
	return &KubeObjectMatcher{
		kind:      kind,
		namespace: namespace,
		name:      name,
	}
}

type KubeObjectMatcher struct {
	kind, namespace, name string
}

func (k *KubeObjectMatcher) Match(actual interface{}) (bool, error) {
	obj, ok := actual.(*uns.Unstructured)
	if !ok {
		return false, fmt.Errorf("cannot match object of type %t", actual)
	}

	ok = obj.GetKind() == k.kind &&
		obj.GetNamespace() == k.namespace &&
		obj.GetName() == k.name
	return ok, nil
}

func (k *KubeObjectMatcher) FailureMessage(actual interface{}) string {
	obj, ok := actual.(*uns.Unstructured)
	if !ok {
		return "not of type Unstructured"
	}

	return fmt.Sprintf("Expected Kind, Namespace, Name to match (%v, %v, %v) but got (%v, %v, %v)",
		k.kind, k.namespace, k.name,
		obj.GetKind(), obj.GetNamespace(), obj.GetName())
}

func (k *KubeObjectMatcher) NegatedFailureMessage(actual interface{}) string {
	obj, ok := actual.(*uns.Unstructured)
	if !ok {
		return "not of type Unstructured"
	}

	return fmt.Sprintf("Expected Kind, Namespace, Name not to match (%v, %v, %v) but got (%v, %v, %v)",
		k.kind, k.namespace, k.name,
		obj.GetKind(), obj.GetNamespace(), obj.GetName())

}

// tweakMetaForCompare adjust the metadata so that if you compare an object
// reconciled with itself, it will come out DeepEqual
// This is just because we always set some metadata, so it diverges in a way
// that is kubernetes equivalent but not go equal.
func tweakMetaForCompare(obj *uns.Unstructured) {
	obj.SetResourceVersion("")
	obj.SetCreationTimestamp(metav1.Time{})
}
