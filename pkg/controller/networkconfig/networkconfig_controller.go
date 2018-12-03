package networkconfig

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/pkg/errors"

	configv1 "github.com/openshift/api/config/v1"
	networkoperatorv1 "github.com/openshift/cluster-network-operator/pkg/apis/networkoperator/v1"
	"github.com/openshift/cluster-network-operator/pkg/apply"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/network"
	"github.com/openshift/cluster-network-operator/pkg/util/clusteroperator"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// The periodic resync interval.
// We will re-run the reconciliation logic, even if the network configuration
// hasn't changed.
var ResyncPeriod = 5 * time.Minute

// ManifestPaths is the path to the manifest templates
// bad, but there's no way to pass configuration to the reconciler right now
var ManifestPath = "./bindata"

// Add creates a new NetworkConfig Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager, status *clusteroperator.StatusManager) error {
	return add(mgr, newReconciler(mgr, status))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager, status *clusteroperator.StatusManager) *ReconcileNetworkConfig {
	configv1.Install(mgr.GetScheme())
	return &ReconcileNetworkConfig{
		client: mgr.GetClient(),
		scheme: mgr.GetScheme(),
		status: status,

		daemonSetReconciler: newDaemonSetReconciler(status),
	}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r *ReconcileNetworkConfig) error {
	// Create a new controller
	c, err := controller.New("networkconfig-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource NetworkConfig
	err = c.Watch(&source.Kind{Type: &networkoperatorv1.NetworkConfig{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	// Likewise for the DaemonSet reconciler
	c, err = controller.New("daemonset-controller", mgr, controller.Options{Reconciler: r.daemonSetReconciler})
	if err != nil {
		return err
	}
	err = c.Watch(&source.Kind{Type: &appsv1.DaemonSet{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &ReconcileNetworkConfig{}

// ReconcileNetworkConfig reconciles a NetworkConfig object
type ReconcileNetworkConfig struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client client.Client
	scheme *runtime.Scheme
	status *clusteroperator.StatusManager

	daemonSetReconciler *ReconcileDaemonSets
}

// Reconcile updates the state of the cluster to match that which is desired
// in the operator configuration (NetworkConfig.networkoperator.openshift.io)
func (r *ReconcileNetworkConfig) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	log.Printf("Reconciling NetworkConfig.networkoperator.openshift.io %s\n", request.Name)

	// We won't create more than one network
	if request.Name != names.OPERATOR_CONFIG {
		log.Printf("Ignoring NetworkConfig without default name")
		return reconcile.Result{}, nil
	}

	// Fetch the NetworkConfig instance
	operConfig := &networkoperatorv1.NetworkConfig{TypeMeta: metav1.TypeMeta{APIVersion: "networkoperator.openshift.io/v1", Kind: "NetworkConfig"}}
	err := r.client.Get(context.TODO(), request.NamespacedName, operConfig)
	if err != nil {
		if apierrors.IsNotFound(err) {
			r.status.SetConfigFailing("NoOperatorConfig", fmt.Errorf("NetworkConfig was deleted"))
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected, since we set
			// the ownerReference (see https://kubernetes.io/docs/concepts/workloads/controllers/garbage-collection/).
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Printf("Unable to retrieve NetworkConfig object: %v", err)
		// FIXME: operator status?
		return reconcile.Result{}, err
	}

	// Merge in the cluster configuration, in case the administrator has updated some "downstream" fields
	// This will also commit the change back to the apiserver.
	if err := r.MergeClusterConfig(context.TODO(), operConfig); err != nil {
		log.Printf("Failed to merge the cluster configuration: %v", err)
		r.status.SetConfigFailing("MergeClusterConfig", err)
		return reconcile.Result{}, err
	}

	// Validate the configuration
	if err := network.Validate(&operConfig.Spec); err != nil {
		log.Printf("Failed to validate NetworkConfig.Spec: %v", err)
		r.status.SetConfigFailing("InvalidOperatorConfig", err)
		return reconcile.Result{}, err
	}

	// Retrieve the previously applied operator configuration
	prev, err := GetAppliedConfiguration(context.TODO(), r.client, operConfig.ObjectMeta.Name)
	if err != nil {
		log.Printf("Failed to retrieve previously applied configuration: %v", err)
		// FIXME: operator status?
		return reconcile.Result{}, err
	}

	// Fill all defaults explicitly
	network.FillDefaults(&operConfig.Spec, prev)

	// Compare against previous applied configuration to see if this change
	// is safe.
	if prev != nil {
		// We may need to fill defaults here -- sort of as a poor-man's
		// upconversion scheme -- if we add additional fields to the config.
		err = network.IsChangeSafe(prev, &operConfig.Spec)
		if err != nil {
			log.Printf("Not applying unsafe change: %v", err)
			errors.Wrapf(err, "not applying unsafe change")
			r.status.SetConfigFailing("InvalidOperatorConfig", err)
			return reconcile.Result{}, err
		}
	}

	// Generate the objects
	objs, err := network.Render(&operConfig.Spec, ManifestPath)
	if err != nil {
		log.Printf("Failed to render: %v", err)
		err = errors.Wrapf(err, "failed to render")
		r.status.SetConfigFailing("RenderError", err)
		return reconcile.Result{}, err
	}

	// The first object we create should be the record of our applied configuration. The last object we create is config.openshift.io/v1/Network.Status
	app, err := AppliedConfiguration(operConfig)
	if err != nil {
		log.Printf("Failed to render applied: %v", err)
		err = errors.Wrapf(err, "failed to render applied")
		r.status.SetConfigFailing("RenderError", err)
		return reconcile.Result{}, err
	}
	objs = append([]*uns.Unstructured{app}, objs...)

	// Set up the DaemonSet reconciler before we start creating the DaemonSets
	r.status.SetConfigSuccess()
	daemonSets := []types.NamespacedName{}
	for _, obj := range objs {
		if obj.GetAPIVersion() == "apps/v1" && obj.GetKind() == "DaemonSet" {
			daemonSets = append(daemonSets, types.NamespacedName{Namespace: obj.GetNamespace(), Name: obj.GetName()})
		}
	}
	r.daemonSetReconciler.SetDaemonSets(daemonSets)

	// Apply the objects to the cluster
	for _, obj := range objs {
		// Mark the object to be GC'd if the owner is deleted.
		if err := controllerutil.SetControllerReference(operConfig, obj, r.scheme); err != nil {
			err = errors.Wrapf(err, "could not set reference for (%s) %s/%s", obj.GroupVersionKind(), obj.GetNamespace(), obj.GetName())
			log.Println(err)
			r.status.SetConfigFailing("InternalError", err)
			return reconcile.Result{}, err
		}

		// Open question: should an error here indicate we will never retry?
		if err := apply.ApplyObject(context.TODO(), r.client, obj); err != nil {
			err = errors.Wrapf(err, "could not apply (%s) %s/%s", obj.GroupVersionKind(), obj.GetNamespace(), obj.GetName())
			log.Println(err)
			r.status.SetConfigFailing("ApplyOperatorConfig", err)
			return reconcile.Result{}, err
		}
	}

	// Update Network.config.openshift.io.Status
	status, err := r.ClusterNetworkStatus(context.TODO(), operConfig)
	if err != nil {
		err = errors.Wrapf(err, "could not generate network status")
		log.Println(err)
		r.status.SetConfigFailing("StatusError", err)
		return reconcile.Result{}, err
	}
	if status != nil {
		// Don't set the owner reference in this case -- we're updating
		// the status of our owner.
		if err := apply.ApplyObject(context.TODO(), r.client, status); err != nil {
			err = errors.Wrapf(err, "could not apply (%s) %s/%s", status.GroupVersionKind(), status.GetNamespace(), status.GetName())
			log.Println(err)
			r.status.SetConfigFailing("StatusError", err)
			return reconcile.Result{}, err
		}
	}

	log.Printf("all objects successfully applied")

	// All was successful. Request that this be re-triggered after ResyncPeriod,
	// so we can reconcile state again.
	return reconcile.Result{RequeueAfter: ResyncPeriod}, nil
}
