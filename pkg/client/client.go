package client

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/openshift/cluster-network-operator/pkg/util/k8s"
	clientConfig "github.com/openshift/library-go/pkg/config/client"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	kinformer "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	osoperclient "github.com/openshift/client-go/operator/clientset/versioned"
	osoperinformer "github.com/openshift/client-go/operator/informers/externalversions"
	operatorv1helpers "github.com/openshift/library-go/pkg/operator/v1helpers"

	configv1 "github.com/openshift/api/config/v1"
	op_netopv1 "github.com/openshift/api/networkoperator/v1"
	operv1 "github.com/openshift/api/operator/v1"
	netopv1 "github.com/openshift/cluster-network-operator/pkg/apis/network/v1"
	machineapi "github.com/openshift/machine-api-operator/pkg/apis/machine/v1beta1"

	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultResyncPeriod = 5 * time.Minute
	DefaultClusterName  = "default"
)

// OperatorClusterClient is a bag of holding for object clients & informers.
// It is generally responsible for managing informer lifecycle.
//
// int 5, wis 2, dex 10, cha 6
type OperatorClusterClient struct {
	cfg *rest.Config

	// Same configuration, but with protobuf enabled
	// Only to be used for proper k8s api types
	protocfg *rest.Config

	kClient  kubernetes.Interface
	kFactory kinformer.SharedInformerFactory

	// client & informers for operator.openshift.io
	osOperClient  *osoperclient.Clientset
	osOperFactory osoperinformer.SharedInformerFactory

	// restMapper is the mapper from GVK to GVR (among other fun tasks)
	restMapper meta.RESTMapper

	// dynclient is an untyped, uncached client for making direct requests
	// against the apiserver.
	dynclient dynamic.Interface

	// crclient is the controller-runtime ClusterClient, for controllers that have
	// not yet been migrated.
	crclient crclient.Client

	// informers is any other Informer we create, e.g. ones with
	// specific watches, that are't managed by the factories.
	informers []cache.SharedInformer

	// ControllerClient is a simple access layer used by some library-go
	// controllers.
	hc *OperatorHelperClient

	// if the informers are started
	started bool
	donech  <-chan struct{}
}

// enforce that OperatorClusterClient implements the ClusterClient interface
var _ ClusterClient = &OperatorClusterClient{}

func NewClient(cfg, protocfg *rest.Config, inClusterClientName string, extraClusters map[string]string) (*OperatorClient, error) {
	cli := &OperatorClient{
		clusterClients: make(map[string]*OperatorClusterClient),
	}

	inClusterClient, err := NewClusterClient(cfg, protocfg)
	if err != nil {
		return nil, err
	}
	cli.clusterClients[inClusterClientName] = inClusterClient

	for name, kubeConfig := range extraClusters {
		clientConfig, err := clientConfig.GetClientConfig(kubeConfig, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to get config for cluster %s from %s: %w", name, kubeConfig, err)
		}
		protoConfig := rest.CopyConfig(clientConfig)
		protoConfig.AcceptContentTypes = "application/vnd.kubernetes.protobuf,application/json"
		protoConfig.ContentType = "application/vnd.kubernetes.protobuf"

		clusterCli, err := NewClusterClient(clientConfig, protoConfig)
		if err != nil {
			return nil, fmt.Errorf("failed create new cluster client for cluster %s: %w", name, err)
		}
		cli.clusterClients[name] = clusterCli
	}
	return cli, nil
}

type OperatorClient struct {
	clusterClients map[string]*OperatorClusterClient
}

// ensure OperatorClient implements Client
var _ Client = &OperatorClient{}

// ClientFor returns a ClusterClient reference based on the name provided, if name is empty returns the default ClusterClient
func (c *OperatorClient) ClientFor(name string) ClusterClient {
	if len(name) == 0 {
		return c.Default()
	}
	return c.clusterClients[name]
}

func (c *OperatorClient) Default() ClusterClient {
	return c.clusterClients[DefaultClusterName]
}

func (c *OperatorClient) Start(ctx context.Context) error {
	for _, cc := range c.clusterClients {
		if err := cc.Start(ctx); err != nil {
			return err
		}
	}

	return nil
}

func (c *OperatorClient) Clients() map[string]ClusterClient {
	out := make(map[string]ClusterClient, len(c.clusterClients))
	for k, v := range c.clusterClients {
		out[k] = v
	}
	return out
}

func NewClusterClient(cfg, protocfg *rest.Config) (*OperatorClusterClient, error) {
	c := OperatorClusterClient{
		cfg:      cfg,
		protocfg: protocfg,
	}
	var err error

	if c.kClient, err = kubernetes.NewForConfig(protocfg); err != nil {
		return nil, err
	}
	c.kFactory = kinformer.NewSharedInformerFactory(c.kClient, defaultResyncPeriod)

	if c.osOperClient, err = osoperclient.NewForConfig(cfg); err != nil {
		return nil, err
	}
	c.osOperFactory = osoperinformer.NewSharedInformerFactory(c.osOperClient, defaultResyncPeriod)

	// Initialize the client-go dynamic client
	c.dynclient, err = dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	// And the DynamicRESTMapper (which handles on-the-fly CRD creation)
	if c.restMapper, err = k8s.NewDynamicRESTMapper(cfg); err != nil {
		return nil, err
	}
	// And the controller-runtime client, which is similar to the client-go dynamic client.
	if c.crclient, err = crclient.New(cfg, crclient.Options{Mapper: c.restMapper}); err != nil {
		return nil, err
	}

	RegisterTypes(c.Scheme())

	return &c, nil
}

func (c *OperatorClusterClient) Kubernetes() kubernetes.Interface {
	return c.kClient
}

// OpenshiftOperatorClient returns the clientset for operator.openshift.io
func (c *OperatorClusterClient) OpenshiftOperatorClient() *osoperclient.Clientset {
	return c.osOperClient
}

// Dynamic returns an untyped, dynamic client.
func (c *OperatorClusterClient) Dynamic() dynamic.Interface {
	return c.dynclient
}

func (c *OperatorClusterClient) CRClient() crclient.Client {
	return c.crclient
}

func (c *OperatorClusterClient) RESTMapper() meta.RESTMapper {
	return c.restMapper
}

func (c *OperatorClusterClient) Scheme() *runtime.Scheme {
	return scheme.Scheme
}

func (c *OperatorClusterClient) Config() *rest.Config {
	return c.cfg
}

func (c *OperatorClusterClient) Start(ctx context.Context) error {
	if c.started {
		return fmt.Errorf("Trying to start ClusterClient twice")
	}
	c.started = true
	c.donech = ctx.Done()

	klog.Info("Starting informers...")

	// Start shared informer factories
	c.kFactory.Start(ctx.Done())
	c.osOperFactory.Start(ctx.Done())

	// Start one-off informers
	for _, inf := range c.informers {
		go inf.Run(ctx.Done())
	}

	klog.Info("Waiting for informers to sync...")

	// Wait for informer factories to sync
	for iType, synced := range c.kFactory.WaitForCacheSync(ctx.Done()) {
		if !synced {
			return fmt.Errorf("error in syncing cache for %v informer", iType)
		}
	}
	for iType, synced := range c.osOperFactory.WaitForCacheSync(ctx.Done()) {
		if !synced {
			return fmt.Errorf("error in syncing cache for %v informer", iType)
		}
	}

	// and any additional informers too
	for _, inf := range c.informers {
		if !cache.WaitForCacheSync(ctx.Done(), inf.HasSynced) {
			return fmt.Errorf("error in syncing an informer")
		}
	}

	klog.Info("Informers started and synced")
	return nil
}

// OperatorHelperClient returns an implementation of the
// v1helpers.OperatorClient interface for use by the library-go
// controllers.
func (c *OperatorClusterClient) OperatorHelperClient() operatorv1helpers.OperatorClient {
	if c.hc != nil {
		return c.hc
	}
	c.hc = &OperatorHelperClient{
		informer: c.osOperFactory.Operator().V1().Networks(),
		client:   c.osOperClient.OperatorV1().Networks(),
	}

	return c.hc
}

// AddCustomInformer adds any informers not created by the factory to
// the ClusterClient lifecycle management.
//
// Example for a label-selected ConfigMap watch:
//
// c.AddCustomInformer(
//     v1coreinformers.NewFilteredServiceInformer(
//          c.Kubernetes(),
//			kapi.NamespaceAll,
//			5 * time.Minute, // resync Period
//			cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
//			func(options *metav1.ListOptions) {
//				// use k8s.io/apimachinery/pkg/labels for more sophisticated selectors
//				options.LabelSelector = "operator.example.dev/mylabel=myval"
//			}))
//
func (c *OperatorClusterClient) AddCustomInformer(inf cache.SharedInformer) {
	c.informers = append(c.informers, inf)
	if c.started {
		go inf.Run(c.donech)
	}
}

func RegisterTypes(s *runtime.Scheme) {
	// Add types to the scheme.
	if err := operv1.Install(s); err != nil {
		log.Fatal(err)
	}
	if err := configv1.Install(s); err != nil {
		log.Fatal(err)
	}
	if err := netopv1.Install(s); err != nil {
		log.Fatal(err)
	}
	if err := machineapi.AddToScheme(s); err != nil {
		log.Fatal(err)
	}
	if err := op_netopv1.Install(s); err != nil {
		log.Fatal(err)
	}
}

// URL returns the host and port of the apiserver endpoint for this client
func (c *OperatorClusterClient) HostPort() (string, string) {
	url, _, err := rest.DefaultServerURL(c.cfg.Host, c.cfg.APIPath, schema.GroupVersion{}, true)
	if err != nil { // can't happen, url was validated earlier when we created the client
		panic(err)

	}
	host := url.Hostname()
	port := url.Port()
	if port == "" {
		port = "443"
	}
	return host, port
}
