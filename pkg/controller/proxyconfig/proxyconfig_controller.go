package proxyconfig

import (
	"context"
	"fmt"
	"k8s.io/apimachinery/pkg/types"
	"log"
	"net/url"
	"strconv"
	"strings"

	"github.com/ghodss/yaml"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/proxy"

	corev1 "k8s.io/api/core/v1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// and Start it when the Manager is Started.
func Add(mgr manager.Manager, status *statusmanager.StatusManager) error {
	return add(mgr, newReconciler(mgr, status))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager, status *statusmanager.StatusManager) reconcile.Reconciler {
	configv1.Install(mgr.GetScheme())
	return &ReconcileProxyConfig{client: mgr.GetClient(), scheme: mgr.GetScheme(), status: status}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("proxyconfig-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource config.openshift.io/v1/Proxy
	err = c.Watch(&source.Kind{Type: &configv1.Proxy{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &ReconcileProxyConfig{}

// ReconcileProxyConfig reconciles a Proxy object
type ReconcileProxyConfig struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver.
	client client.Client
	scheme *runtime.Scheme
	status *statusmanager.StatusManager
}

// Reconcile expects request to refer to a proxy object named "cluster" in the
// default namespace, and will ensure proxy is in the desired state.
func (r *ReconcileProxyConfig) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	log.Printf("Reconciling Proxy.config.openshift.io %s\n", request.Name)

	// Only reconcile the "cluster" proxy.
	if request.Name != names.PROXY_CONFIG {
		log.Printf("Ignoring Proxy without default name " + names.PROXY_CONFIG)
		return reconcile.Result{}, nil
	}

	// Fetch the proxy config
	proxyConfig := &configv1.Proxy{}
	err := r.client.Get(context.TODO(), request.NamespacedName, proxyConfig)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Return and don't requeue
			log.Println("proxy not found; reconciliation will be skipped", "request", request)
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, fmt.Errorf("failed to get proxy %q: %v", request, err)
	}

	// Only proceed if we can collect cluster config.
	infraConfig := &configv1.Infrastructure{}
	if err := r.client.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, infraConfig); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to get infrastructure config 'cluster': %v", err)
	}
	netConfig := &configv1.Network{}
	if err := r.client.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, infraConfig); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to get network config 'cluster': %v", err)
	}
	clusterConfig := &corev1.ConfigMap{}
	if err := r.client.Get(context.TODO(), types.NamespacedName{Name: "cluster-config-v1"}, clusterConfig); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to get network config 'cluster': %v", err)
	}

	if err := proxy.ValidateProxyConfig(r.client, proxyConfig.Spec); err != nil {
		log.Printf("Failed to validate Proxy.Spec: %v", err)
		r.status.SetDegraded(statusmanager.ProxyConfig, "InvalidProxyConfig",
			fmt.Sprintf("The proxy configuration is invalid (%v). Use 'oc edit proxy.config.openshift.io cluster' to fix.", err))
		return reconcile.Result{}, err
	}

	if err := r.syncProxyStatus(proxyConfig, infraConfig, netConfig, clusterConfig); err != nil {
		log.Printf("Failed to enforce NoProxy default values: %v", err)
		r.status.SetDegraded(statusmanager.ProxyConfig, "DefaultNoProxyFailedEnforcement",
			fmt.Sprintf("Failed to enforce system default NoProxy values: %v", err))
		return reconcile.Result{}, err
	}

	// TODO: How should proxy reconciliation be reflected in clusteroperator/network status?

	r.status.SetNotDegraded(statusmanager.ProxyConfig)
	return reconcile.Result{}, nil
}

// syncProxyStatus...
func (r *ReconcileProxyConfig) syncProxyStatus(proxy *configv1.Proxy, infra *configv1.Infrastructure, network *configv1.Network, cluster *corev1.ConfigMap) error {
	updated := proxy.DeepCopy()

	apiServerURL, err := url.Parse(infra.Status.APIServerURL)
	if err != nil {
		return fmt.Errorf("failed to parse API server URL")
	}
	internalAPIServer, err := url.Parse(infra.Status.APIServerInternalURL)
	if err != nil {
		return fmt.Errorf("failed to parse API server internal URL")
	}

	set := sets.NewString(
		"127.0.0.1",
		"localhost",
		network.Status.ServiceNetwork[0],
		apiServerURL.Hostname(),
		internalAPIServer.Hostname(),
	)
	platform := infra.Status.PlatformStatus.Type

	// TODO: Does a better way exist to get machineCIDR and controlplane replicas?
	type installConfig struct {
		ControlPlane struct {
			Replicas string `json:"replicas"`
		} `json:"controlPlane"`
		Networking struct {
			MachineCIDR string `json:"machineCIDR"`
		} `json:"networking"`
	}
	var ic installConfig
	data, ok := cluster.Data["install-config"]
	if !ok {
		return fmt.Errorf("missing install-config in configmap")
	}
	if err := yaml.Unmarshal([]byte(data), &ic); err != nil {
		return fmt.Errorf("invalid install-config: %v\njson:\n%s", err, data)
	}

	if platform != configv1.VSpherePlatformType && platform != configv1.NonePlatformType {
		set.Insert("169.254.169.254", ic.Networking.MachineCIDR)
	}

	replicas, err := strconv.Atoi(ic.ControlPlane.Replicas)
	if err != nil {
		return fmt.Errorf("failed to parse install config replicas: %v", err)
	}

	for i := int64(0); i < int64(replicas); i++ {
		etcdHost := fmt.Sprintf("etcd-%d.%s", i, infra.Status.EtcdDiscoveryDomain)
		set.Insert(etcdHost)
	}

	for _, clusterNetwork := range network.Status.ClusterNetwork {
		set.Insert(clusterNetwork.CIDR)
	}

	for _, userValue := range strings.Split(proxy.Spec.NoProxy, ",") {
		set.Insert(userValue)
	}

	updated.Status.NoProxy = strings.Join(set.List(), ",")

	if !proxyStatusesEqual(proxy.Status, updated.Status) {
		if err := r.client.Status().Update(context.TODO(), updated); err != nil {
			return fmt.Errorf("failed to update proxy status: %v", err)
		}
	}

	return nil
}

// proxyStatusesEqual compares two ProxyStatus values. Returns true if the
// provided values should be considered equal for the purpose of determining
// whether an update is necessary, false otherwise.
func proxyStatusesEqual(a, b configv1.ProxyStatus) bool {
	if a.HTTPProxy != b.HTTPProxy || a.HTTPSProxy != b.HTTPSProxy || a.NoProxy != b.NoProxy {
		return false
	}

	return true
}
