package apply

import (
	"context"
	"reflect"
	"testing"

	operv1 "github.com/openshift/api/operator/v1"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/client/fake"

	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
)

func init() {
	utilruntime.Must(operv1.AddToScheme(scheme.Scheme))
}

func Test_Merge(t *testing.T) {
	tests := []struct {
		name     string
		kind     schema.GroupVersionKind
		initial  Object
		input    Object
		expected Object
	}{
		{
			"no merge if operator config does not exist",
			schema.GroupVersionKind{
				Group:   operv1.GroupName,
				Kind:    "Network",
				Version: operv1.GroupVersion.Version,
			},
			nil,
			&operv1.Network{},
			&operv1.Network{},
		},
		{
			"merge operator config DisableNetworkDiagnostics remains at current value",
			schema.GroupVersionKind{
				Group:   operv1.GroupName,
				Kind:    "Network",
				Version: operv1.GroupVersion.Version,
			},
			&operv1.Network{
				Spec: operv1.NetworkSpec{
					DisableNetworkDiagnostics: true,
				},
			},
			&operv1.Network{},
			&operv1.Network{
				Spec: operv1.NetworkSpec{
					DisableNetworkDiagnostics: true,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewGomegaWithT(t)
			var client cnoclient.Client
			if tt.initial != nil {
				client = fake.NewFakeClient(tt.initial)
			} else {
				client = fake.NewFakeClient()
			}
			tt.input.GetObjectKind().SetGroupVersionKind(tt.kind)
			tt.expected.GetObjectKind().SetGroupVersionKind(tt.kind)
			merge := getMergeForUpdate(tt.input)
			g.Expect(merge).NotTo(BeNil())
			uns, err := merge(context.Background(), client.Default())
			g.Expect(err).NotTo(HaveOccurred())
			output := reflect.New(reflect.ValueOf(tt.expected).Elem().Type()).Interface()
			err = runtime.DefaultUnstructuredConverter.FromUnstructured(uns.Object, output)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal(tt.expected))
		})
	}
}
