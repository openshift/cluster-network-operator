package infrastructureconfig

import (
	"context"
	"fmt"
	"log"
	"reflect"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/apply"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const ControllerName = "infrastructureconfig"

// Add attaches our control loop to the manager and watches for infrastructure objects
func Add(mgr manager.Manager, status *statusmanager.StatusManager, c cnoclient.Client, _ featuregates.FeatureGate) error {
	return add(mgr, newReconciler(mgr, status, c))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager, status *statusmanager.StatusManager, c cnoclient.Client) reconcile.Reconciler {
	return &ReconcileInfrastructureConfig{
		client:      c,
		scheme:      mgr.GetScheme(),
		status:      status,
		fieldSyncer: &synchronizer{},
	}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("infrastructureconfig-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource config.openshift.io/v1/Infrastructure
	err = c.Watch(source.Kind[crclient.Object](mgr.GetCache(), &configv1.Infrastructure{}, &handler.EnqueueRequestForObject{}, onPremPlatformPredicate()))
	if err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &ReconcileInfrastructureConfig{}

// ReconcileInfrastructureConfig reconciles a cluster Infrastructure object
type ReconcileInfrastructureConfig struct {
	client      cnoclient.Client
	scheme      *runtime.Scheme
	status      *statusmanager.StatusManager
	fieldSyncer fieldSynchronizer
}

// Reconcile handles Infrastructure.config.openshift.io/cluster. It is responsible for allowing
// modifications to the PlatformSpec that stores on-prem network configuration (e.g. VIPs).
// It also syncs the new and deprecated API & Ingress VIP fields to have consistent APIs between
// versions, something that has been introduced when dual-stack VIPs were implemented in the first
// place.
func (r *ReconcileInfrastructureConfig) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	defer utilruntime.HandleCrash(r.status.SetDegradedOnPanicAndCrash)
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
		err = fmt.Errorf("error while reading infrastructures.%s/cluster: %w", configv1.GroupName, err)
		log.Println(err)
		return reconcile.Result{}, err
	}

	// Synchronizing VIPs does not require error handling as it performs an automatic migration
	// for a data structure introduced in OCP 4.12. The function does not operate on any
	// user-provided input, thus errors can only be a result of unhealthy cluster state.
	updatedInfraConfig := r.fieldSyncer.VipsSynchronize(infraConfig)

	updatedInfraConfig, err = r.fieldSyncer.SpecStatusSynchronize(updatedInfraConfig)
	if err != nil {
		err = fmt.Errorf("error while synchronizing spec and status of infrastructures.%s/cluster: %w", configv1.GroupName, err)
		log.Println(err)

		r.status.SetDegraded(statusmanager.InfrastructureConfig, "SyncInfrastructureSpecAndStatus", err.Error())
		return reconcile.Result{}, err
	}

	// The "duplicated" logic below is a direct result of how server-side-apply works for this
	// object. Where it would be natural that `apply.ApplyObject` updates both Spec and Status
	// at the same time, in reality the first call only updates Spec and leaves Status with the
	// old content. To fix that and have Status also updated, we are executing a second call with
	// explicit marker that "status" subresource should be updated.
	if !reflect.DeepEqual(updatedInfraConfig.Spec, infraConfig.Spec) {
		if err = r.updateInfrastructureConfig(ctx, updatedInfraConfig); err != nil {
			err = fmt.Errorf("error while updating infrastructures.%s/cluster: %w", configv1.GroupName, err)
			log.Println(err)

			r.status.SetDegraded(statusmanager.InfrastructureConfig, "UpdateInfrastructureSpecOrStatus", err.Error())
			return reconcile.Result{}, err
		}
		log.Printf("Successfully synchronized infrastructure config.")
	}

	if !reflect.DeepEqual(updatedInfraConfig.Status, infraConfig.Status) {
		if err = r.updateInfrastructureConfig(ctx, updatedInfraConfig, "status"); err != nil {
			err = fmt.Errorf("error while updating status of infrastructures.%s/cluster: %w", configv1.GroupName, err)
			log.Println(err)

			r.status.SetDegraded(statusmanager.InfrastructureConfig, "UpdateInfrastructureStatus", err.Error())
			return reconcile.Result{}, err
		}
		log.Printf("Successfully synchronized infrastructure config status")
	}

	r.status.SetNotDegraded(statusmanager.InfrastructureConfig)
	return reconcile.Result{}, nil
}

func (r *ReconcileInfrastructureConfig) updateInfrastructureConfig(ctx context.Context, infraConfig *configv1.Infrastructure, subresources ...string) error {
	infraConfigToApply := &configv1.Infrastructure{
		ObjectMeta: v1.ObjectMeta{
			Name: infraConfig.Name,
		},
		Status: infraConfig.Status,
		Spec:   infraConfig.Spec,
	}

	return apply.ApplyObject(ctx, r.client, infraConfigToApply, ControllerName, subresources...)
}
