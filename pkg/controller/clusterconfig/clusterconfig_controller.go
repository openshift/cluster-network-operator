package clusterconfig

import (
	"context"
	"fmt"
	"log"

	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/apply"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/network"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// and Start it when the Manager is Started.
func Add(mgr manager.Manager, status *statusmanager.StatusManager, c *cnoclient.Client) error {
	return add(mgr, newReconciler(mgr, status, c.Default()))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager, status *statusmanager.StatusManager, c *cnoclient.ClusterClient) reconcile.Reconciler {
	return &ReconcileClusterConfig{client: c, scheme: mgr.GetScheme(), status: status}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("clusterconfig-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource config.openshift.io/v1/Network
	err = c.Watch(&source.Kind{Type: &configv1.Network{}}, &handler.EnqueueRequestForObject{}, predicate.GenerationChangedPredicate{})
	if err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &ReconcileClusterConfig{}

// ReconcileClusterConfig reconciles a cluster Network object
type ReconcileClusterConfig struct {
	client *cnoclient.ClusterClient
	scheme *runtime.Scheme
	status *statusmanager.StatusManager
}

// Reconcile propagates changes from the cluster config to the operator config.
// In other words, it watches Network.config.openshift.io/v1/cluster and updates
// Network.operator.openshift.io/v1/cluster.
func (r *ReconcileClusterConfig) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Printf("Reconciling Network.config.openshift.io %s\n", request.Name)

	// We won't create more than one network
	if request.Name != names.CLUSTER_CONFIG {
		log.Printf("Ignoring Network without default name " + names.CLUSTER_CONFIG)
		return reconcile.Result{}, nil
	}

	// Fetch the cluster config
	clusterConfig := &configv1.Network{}
	err := r.client.CRClient().Get(ctx, request.NamespacedName, clusterConfig)
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
	if err := network.ValidateClusterConfig(clusterConfig.Spec, r.client.CRClient()); err != nil {
		log.Printf("Failed to validate Network.Spec: %v", err)
		r.status.SetDegraded(statusmanager.ClusterConfig, "InvalidClusterConfig",
			fmt.Sprintf("The cluster configuration is invalid (%v). Use 'oc edit network.config.openshift.io cluster' to fix.", err))
		return reconcile.Result{}, err
	}

	// Generate a stub operator config and patch it in
	// This will cause only the fields we change to be set.
	operConfig := &operv1.Network{
		TypeMeta:   metav1.TypeMeta{APIVersion: operv1.GroupVersion.String(), Kind: "Network"},
		ObjectMeta: metav1.ObjectMeta{Name: names.OPERATOR_CONFIG},
	}
	network.MergeClusterConfig(&operConfig.Spec, clusterConfig.Spec)

	if err := apply.ApplyObject(ctx, r.client, operConfig, "clusterconfig"); err != nil {
		r.status.SetDegraded(statusmanager.ClusterConfig, "ApplyOperatorConfig",
			fmt.Sprintf("Error while trying to update operator configuration: %v", err))
		log.Printf("Could not propagate configuration from network.config.openshift.io to network.operator.openshift.io: %v", err)
		return reconcile.Result{}, fmt.Errorf("could not apply updated operator configuration: %w", err)
	}
	log.Println("Successfully updated Operator config from Cluster config")

	r.status.SetNotDegraded(statusmanager.ClusterConfig)
	return reconcile.Result{}, nil
}
