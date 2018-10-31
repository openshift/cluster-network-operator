package operator

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/onsi/gomega/types"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
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

// UnstructuredFromYaml creates an unstructured object from a raw yaml string
func UnstructuredFromYaml(t *testing.T, obj string) *uns.Unstructured {
	t.Helper()
	buf := bytes.NewBufferString(obj)
	decoder := yaml.NewYAMLOrJSONDecoder(buf, 4096)

	u := uns.Unstructured{}
	err := decoder.Decode(&u)
	if err != nil {
		t.Fatalf("failed to parse test yaml: %v", err)
	}

	return &u
}
