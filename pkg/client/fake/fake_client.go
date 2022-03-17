package fake

import (
	"context"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"

	osoperclient "github.com/openshift/client-go/operator/clientset/versioned"
	operatorv1helpers "github.com/openshift/library-go/pkg/operator/v1helpers"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type FakeClient struct {
	clusterClients map[string]*FakeClusterClient
}

type FakeClusterClient struct {
	// dynclient is an untyped, uncached client for making direct requests
	// against the apiserver.
	dynclient dynamic.Interface

	// crclient is the controller-runtime ClusterClient, for controllers that have
	// not yet been migrated.
	crclient crclient.Client
}

func (fc *FakeClient) ClientFor(name string) cnoclient.ClusterClient {
	return fc.clusterClients[name]
}

func (fc *FakeClient) Default() cnoclient.ClusterClient {
	return fc.ClientFor(cnoclient.DefaultClusterName)
}

func (fc *FakeClient) Start(context.Context) error {
	return nil
}

func (fc *FakeClient) Clients() map[string]cnoclient.ClusterClient {
	out := make(map[string]cnoclient.ClusterClient)
	for k, v := range fc.clusterClients {
		out[k] = v
	}
	return out
}

// NewFakeClient creates a fake client with a backing store that contains the given objexts.
//
// Note that, due to limitations in the test infrastructure, each client has an independent store.
// This means that changes made in, say, the crclient, won't show up in the Dynamic client or the typed
// Kubernetes client
// TODO: stop using the crclient entirely
// TODO: Somehow convince upstream client-go to allow sharing the store between the dynamic and typed clients.
//       (this is't that big a deal since we don't actually use the typed client that much).
func NewFakeClient(objs ...crclient.Object) cnoclient.Client {
	// silly go type conversion
	oo := make([]runtime.Object, 0, len(objs))
	for _, o := range objs {
		oo = append(oo, o)
	}
	scheme := scheme.Scheme
	cnoclient.RegisterTypes(scheme)
	fc := FakeClusterClient{
		// kClient:   faketyped.NewSimpleClientset(oo...), // TODO: fix this, it doesn't work for non-kubernetes objects
		dynclient: fakedynamic.NewSimpleDynamicClient(scheme, oo...),
		crclient:  crfake.NewClientBuilder().WithObjects(objs...).Build(),
	}

	return &FakeClient{
		clusterClients: map[string]*FakeClusterClient{
			cnoclient.DefaultClusterName: &fc,
		},
	}
}

func (fc *FakeClusterClient) Kubernetes() kubernetes.Interface {
	panic("not implemented!")
}

func (fc *FakeClusterClient) OpenshiftOperatorClient() *osoperclient.Clientset {
	panic("not implemented!")
}

func (fc *FakeClusterClient) Config() *rest.Config {
	return nil
}

func (fc *FakeClusterClient) Dynamic() dynamic.Interface {
	return fc.dynclient
}

func (fc *FakeClusterClient) CRClient() crclient.Client {
	return fc.crclient
}

func (fc *FakeClusterClient) RESTMapper() meta.RESTMapper {
	panic("not implemented!")
}

func (fc *FakeClusterClient) Scheme() *runtime.Scheme {
	panic("not implemented!")
}
func (fc *FakeClusterClient) OperatorHelperClient() operatorv1helpers.OperatorClient {
	panic("not implemented!")
}

func (fc *FakeClusterClient) HostPort() (string, string) {
	return "testing", "9999"
}
