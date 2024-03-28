package network

import (
	"context"
	"fmt"

	"github.com/onsi/gomega/types"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/hypershift"
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
