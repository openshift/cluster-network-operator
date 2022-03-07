package k8s

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/openshift/cluster-network-operator/pkg/names"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func TestSame(t *testing.T) {
	obj1b := `
kind: DaemonSet
apiVersion: apps/v1beta1
metadata:
  name: foo1
  namespace: myns
  annotations:
    foo: bar
spec:
  a: b`
	obj1 := parseManifest(t, obj1b)

	obj2b := `
kind: DaemonSet
apiVersion: apps/v1
metadata:
  name: foo1
  namespace: myns
  annotations:
    foo: bar
spec:
  a: c`
	obj2 := parseManifest(t, obj2b)

	if !Same(obj1, obj2) {
		t.Fatal("Expected Same == true")
	}

	obj3 := obj2.DeepCopy()
	obj3.SetNamespace("otherns")
	if Same(obj1, obj3) {
		t.Fatal("Different ns, shouldn't be Same")
	}

	obj4 := obj2.DeepCopy()
	obj4.SetKind("Deployment")
	if Same(obj1, obj4) {
		t.Fatal("Different Kind, shouldn't be Same")
	}

	obj5 := obj2.DeepCopy()
	obj5.SetName("foo2")
	if Same(obj1, obj5) {
		t.Fatal("Different name, shouldn't be Same")
	}
}

func TestReplace(t *testing.T) {
	specs := []string{
		`
kind: DaemonSet
apiVersion: apps/v1beta1
metadata:
  name: foo1
  namespace: myns
  annotations:
    foo: bar
spec:
  a: b`,
		`
kind: Deployment
apiVersion: apps/v1
metadata:
  name: foo1
  namespace: myns
  annotations:
    foo: bar
spec:
  a: c`,
	}

	objs := []*uns.Unstructured{}
	for _, spec := range specs {
		objs = append(objs, parseManifest(t, spec))
	}

	replacement := parseManifest(t, `
kind: DaemonSet
apiVersion: apps/v1beta1
metadata:
  name: foo1
  namespace: myns
  annotations:
    foo: bar
spec:
  a: b`)

	replaced := ReplaceObj(objs, replacement)

	if len(replaced) != 2 {
		t.Fatalf("Expected replacent to have 2 entries, actual %d", len(replaced))
	}

	found := false
	for _, obj := range replaced {
		if reflect.DeepEqual(obj, replacement) {
			found = true
		}
	}

	if !found {
		t.Fatal("Replacement object didn't seem to be found")
	}
}

func TestUpdate(t *testing.T) {

	specs := []string{
		`
kind: DaemonSet
apiVersion: apps/v1beta1
metadata:
  name: foo1
  namespace: myns
  annotations:
    foo: bar
spec:
  a: b`,
		`
kind: Deployment
apiVersion: apps/v1
metadata:
  name: foo1
  namespace: myns
  annotations:
    foo: bar
spec:
  a: c`,
	}

	objs := []*uns.Unstructured{}
	for _, spec := range specs {
		objs = append(objs, parseManifest(t, spec))
	}

	UpdateObjByGroupKindName(objs, "apps", "DaemonSet", "myns", "foo1", func(o *uns.Unstructured) {
		anno := o.GetAnnotations()
		anno[names.CreateOnlyAnnotation] = "true"
		o.SetAnnotations(anno)
	})

	found := false
	for _, obj := range objs {
		if found {
			continue
		}
		_, ok := obj.GetAnnotations()[names.CreateOnlyAnnotation]
		if ok && obj.GetKind() == "DaemonSet" {
			found = true
		}
	}

	if !found {
		t.Fatal("did not find object with expected new annotation")
	}
}

func parseManifest(t *testing.T, manifest string) *uns.Unstructured {
	t.Helper()
	buf := bytes.Buffer{}
	buf.WriteString(manifest)
	decoder := yaml.NewYAMLOrJSONDecoder(&buf, 4096)
	out := uns.Unstructured{}

	if err := decoder.Decode(&out); err != nil {
		t.Fatal(err)
	}

	return &out
}
