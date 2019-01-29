package clusterconfig

import (
	"context"
	"log"

	"github.com/pkg/errors"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/apply"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/network"
	"github.com/openshift/cluster-network-operator/pkg/util/clusteroperator"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// and Start it when the Manager is Started.
func Add(mgr manager.Manager, status *clusteroperator.StatusManager) error {
	return add(mgr, newReconciler(mgr, status))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager, status *clusteroperator.StatusManager) reconcile.Reconciler {
	configv1.Install(mgr.GetScheme())
	return &ReconcileClusterConfig{client: mgr.GetClient(), scheme: mgr.GetScheme(), status: status}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("clusterconfig-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource config.openshift.io/v1/Network
	err = c.Watch(&source.Kind{Type: &configv1.Network{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &ReconcileClusterConfig{}

// ReconcileClusterConfig reconciles a cluster Network object
type ReconcileClusterConfig struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client client.Client
	scheme *runtime.Scheme
	status *clusteroperator.StatusManager
}

// Reconcile propagates changes from the cluster config to the operator config.
// In other words, it watches Network.config.openshift.io/v1/cluster and updates
// NetworkConfig.networkoperator.openshift.io/v1/default.
func (r *ReconcileClusterConfig) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	log.Printf("Reconciling Network.config.openshift.io %s\n", request.Name)

	// We won't create more than one network
	if request.Name != names.CLUSTER_CONFIG {
		log.Printf("Ignoring Network without default name " + names.CLUSTER_CONFIG)
		return reconcile.Result{}, nil
	}

	// Fetch the cluster config
	clusterConfig := &configv1.Network{}
	err := r.client.Get(context.TODO(), request.NamespacedName, clusterConfig)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Return and don't requeue
			log.Println("Object seems to have been deleted")
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Println(err)
		// FIXME: operator status?
		return reconcile.Result{}, err
	}

	// Validate the cluster config
	if err := network.ValidateClusterConfig(clusterConfig.Spec); err != nil {
		err = errors.Wrapf(err, "failed to validate Network.Spec")
		log.Println(err)
		r.status.SetConfigFailing("InvalidClusterConfig", err)
		return reconcile.Result{}, err
	}

	operatorConfig, err := r.UpdateOperatorConfig(context.TODO(), *clusterConfig)
	if err != nil {
		err = errors.Wrapf(err, "failed to generate NetworkConfig CRD")
		log.Println(err)
		r.status.SetConfigFailing("UpdateOperatorConfig", err)
		return reconcile.Result{}, err
	}

	// Clear any cluster config-related errors before applying operator config
	r.status.SetConfigSuccess()

	if operatorConfig != nil {
		if err := apply.ApplyObject(context.TODO(), r.client, operatorConfig); err != nil {
			err = errors.Wrapf(err, "could not apply (%s) %s/%s", operatorConfig.GroupVersionKind(), operatorConfig.GetNamespace(), operatorConfig.GetName())
			log.Println(err)
			r.status.SetConfigFailing("ApplyClusterConfig", err)
			return reconcile.Result{}, err
		}

		log.Printf("successfully updated ClusterNetwork (%s) %s/%s", operatorConfig.GroupVersionKind(), operatorConfig.GetNamespace(), operatorConfig.GetName())
	}

	return reconcile.Result{}, nil
}
