package client

import (
	"context"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	osoperclient "github.com/openshift/client-go/operator/clientset/versioned"
	operatorv1helpers "github.com/openshift/library-go/pkg/operator/v1helpers"

	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// Client holds all apiserver connections.
type Client interface {
	// ClientFor returns the ClusterClient for a given named cluster.
	ClientFor(name string) ClusterClient

	// Default returns the "default" cluster's Client. This is probably what you want.
	Default() ClusterClient

	// Start may start all informers for all clients, if applicable.
	Start(context.Context) error
}

// ClusterClient is the connection to a single cluster / apiserver. It exposes
// various "clients" to this single apiserver.
type ClusterClient interface {
	// Kuberetes returns the typed Kubernetes client
	Kubernetes() kubernetes.Interface

	// OpenshiftOperatorClient returns the clientset for operator.openshift.io
	OpenshiftOperatorClient() *osoperclient.Clientset

	// Dynamic returns an untyped, dynamic client.
	Dynamic() dynamic.Interface

	// CRClient returns the controller-runtime client, another untyped client
	CRClient() crclient.Client

	// RESTMapper returns this cluster's RESTMapper, a mapping from type to api resource
	RESTMapper() meta.RESTMapper

	Scheme() *runtime.Scheme

	// OpenshiftOperatorClient returns the clientset for operator.openshift.io
	OperatorHelperClient() operatorv1helpers.OperatorClient
}
