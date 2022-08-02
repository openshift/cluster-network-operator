package infrastructureconfig

import (
	"context"
	"fmt"
	"log"
	"reflect"

	configv1 "github.com/openshift/api/config/v1"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/cluster-network-operator/pkg/names"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const ControllerName = "infrastructureconfig"

// Add attaches our control loop to the manager and watches for infrastructure objects
func Add(mgr manager.Manager, status *statusmanager.StatusManager, c cnoclient.Client) error {
	return add(mgr, newReconciler(mgr, status, c))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager, status *statusmanager.StatusManager, c cnoclient.Client) reconcile.Reconciler {
	return &ReconcileInfrastructureConfig{client: c, scheme: mgr.GetScheme(), status: status, apiAndIngressVIPsSyncer: &apiAndIngressVipsSynchronizer{}}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("infrastructureconfig-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource config.openshift.io/v1/Infrastructure
	err = c.Watch(&source.Kind{Type: &configv1.Infrastructure{}}, &handler.EnqueueRequestForObject{}, onPremPlatformPredicate())
	if err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &ReconcileInfrastructureConfig{}

// ReconcileInfrastructureConfig reconciles a cluster Infrastructure object
type ReconcileInfrastructureConfig struct {
	client                  cnoclient.Client
	scheme                  *runtime.Scheme
	status                  *statusmanager.StatusManager
	apiAndIngressVIPsSyncer vipsSynchronizer
}

// Reconcile watches Infrastructure.config.openshift.io/cluster and syncs the
// new and deprecated API & Ingress VIP fields to have consistent APIs between
// versions.
func (r *ReconcileInfrastructureConfig) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Printf("Reconciling Infrastructure.config.openshift.io %s\n", request.Name)

	// Only check on the default infrastructure config
	if request.Name != names.INFRASTRUCTURE_CONFIG {
		log.Printf("Ignoring Infrastructure config %s. Only handling Infrastructure config with default name %s", request.Name, names.INFRASTRUCTURE_CONFIG)
		return reconcile.Result{}, nil
	}

	// Fetch the infrastructure config
	infraConfig := &configv1.Infrastructure{}
	err := r.client.Default().CRClient().Get(ctx, request.NamespacedName, infraConfig)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Return and don't requeue
			log.Println("Object seems to have been deleted")
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		err = fmt.Errorf("Error while reading infrastructures.%s/cluster: %w", configv1.GroupName, err)
		log.Println(err)
		return reconcile.Result{}, err
	}

	// Sync API & Ingress VIPs
	updatedInfraConfig := r.apiAndIngressVIPsSyncer.VipsSynchronize(infraConfig)

	if !reflect.DeepEqual(updatedInfraConfig.Status, infraConfig.Status) {
		if err = r.client.Default().CRClient().Status().Update(ctx, updatedInfraConfig, &client.UpdateOptions{}); err != nil {
			err = fmt.Errorf("Error while updating infrastructures.%s/cluster status: %w", configv1.GroupName, err)
			log.Println(err)

			r.status.SetDegraded(statusmanager.InfrastructureConfig, "UpdateInfrastructureStatus", err.Error())
			return reconcile.Result{}, err
		}

		log.Printf("Successfully synchronized API & Ingress VIPs in infrastructure config")
	}

	r.status.SetNotDegraded(statusmanager.InfrastructureConfig)
	return reconcile.Result{}, nil
}
