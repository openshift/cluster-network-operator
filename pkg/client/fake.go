package client

import (
	"k8s.io/apimachinery/pkg/runtime"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// NewFakeClient creates a fake client with a backing store that contains the given objexts.
//
// Note that, due to limitations in the test infrastructure, each client has an independent store.
// This means that changes made in, say, the crclient, won't show up in the Dynamic client or the typed
// Kubernetes client
// TODO: stop using the crclient entirely
// TODO: Somehow convince upstream client-go to allow sharing the store between the dynamic and typed clients.
//       (this is't that big a deal since we don't actually use the typed client that much).
// TODO: make this an interface, move this silly function to a separate package.
func NewFakeClient(objs ...crclient.Object) *Client {
	// silly go type conversion
	oo := make([]runtime.Object, 0, len(objs))
	for _, o := range objs {
		oo = append(oo, o)
	}
	scheme := scheme.Scheme
	registerTypes(scheme)
	cc := ClusterClient{
		cfg: &rest.Config{Host: "https://testing:8443"},
		// kClient:   faketyped.NewSimpleClientset(oo...), // TODO: fix this, it doesn't work for non-kubernetes objects
		dynclient: fakedynamic.NewSimpleDynamicClient(scheme, oo...),
		crclient:  crfake.NewClientBuilder().WithObjects(objs...).Build(),
	}
	return &Client{
		clusterClients: map[string]*ClusterClient{
			DefaultClusterName: &cc,
		},
	}
}
