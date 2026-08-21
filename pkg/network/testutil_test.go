package network

import (
	"context"
	"fmt"
	"slices"
	"testing"

	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/hypershift"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
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

func fakeBootstrapResult() *bootstrap.BootstrapResult {
	return &bootstrap.BootstrapResult{
		Infra: bootstrap.InfraStatus{
			PlatformType:           "GCP",
			PlatformRegion:         "moon-2",
			ControlPlaneTopology:   configv1.HighlyAvailableTopologyMode,
			InfrastructureTopology: configv1.HighlyAvailableTopologyMode,
			APIServers: map[string]bootstrap.APIServer{
				bootstrap.APIServerDefault: {
					Host: "testing.test",
					Port: "8443",
				},
			},
		},
	}
}

func fakeBootstrapResultWithHyperShift() *bootstrap.BootstrapResult {
	return &bootstrap.BootstrapResult{
		Infra: bootstrap.InfraStatus{
			PlatformType:           "GCP",
			PlatformRegion:         "moon-2",
			ControlPlaneTopology:   configv1.HighlyAvailableTopologyMode,
			InfrastructureTopology: configv1.HighlyAvailableTopologyMode,
			APIServers: map[string]bootstrap.APIServer{
				bootstrap.APIServerDefault: {
					Host: "testing.test",
					Port: "8443",
				},
			},
			HostedControlPlane: &hypershift.HostedControlPlane{
				ClusterID:    "test",
				NodeSelector: map[string]string{},
			},
		},
	}
}

// createProxy creates an empty proxy object.
func createProxy(client client.Client) error {
	proxy := &configv1.Proxy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
	}
	return client.Default().CRClient().Create(context.TODO(), proxy)
}

// mustFindRenderedObj finds and converts an unstructured object from a list of rendered objects.
// It uses Go generics to return the properly typed object.
func mustFindRenderedObj[T any](t *testing.T, objs []*uns.Unstructured, kind, name string) T {
	t.Helper()
	g := NewWithT(t)

	index := slices.IndexFunc(objs, func(obj *uns.Unstructured) bool {
		return obj.GetKind() == kind && obj.GetName() == name
	})
	g.Expect(index).NotTo(Equal(-1), "Could not find object with kind %q and name %q", kind, name)

	var result T
	err := runtime.DefaultUnstructuredConverter.FromUnstructured(objs[index].Object, &result)
	g.Expect(err).NotTo(HaveOccurred())

	return result
}
