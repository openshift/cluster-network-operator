package infrastructureconfig

import (
	"context"
	"fmt"
	"log"
	"reflect"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	configv1 "github.com/openshift/api/config/v1"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
	"github.com/openshift/cluster-network-operator/pkg/apply"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/cluster-network-operator/pkg/names"
)

const ControllerName = "infrastructureconfig"

// Add attaches our control loop to the manager and watches for infrastructure objects
func Add(mgr manager.Manager, status *statusmanager.StatusManager, c cnoclient.Client) error {
	rc, err := newReconciler(mgr, status, c)
	if err != nil {
		return err
	}
	return add(mgr, rc)
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager, status *statusmanager.StatusManager, c cnoclient.Client) (reconcile.Reconciler, error) {
	kubeConfig := c.Default().Config()
	configClient, err := configclient.NewForConfig(kubeConfig)
	if err != nil {
		return nil, err
	}

	return &ReconcileInfrastructureConfig{
		client:      c,
		typedClient: configClient,
		scheme:      mgr.GetScheme(),
		status:      status,
		fieldSyncer: &synchronizer{},
	}, nil
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("infrastructureconfig-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource config.openshift.io/v1/Infrastructure
	err = c.Watch(source.Kind(mgr.GetCache(), &configv1.Infrastructure{}), &handler.EnqueueRequestForObject{}, onPremPlatformPredicate())
	if err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &ReconcileInfrastructureConfig{}

// ReconcileInfrastructureConfig reconciles a cluster Infrastructure object
type ReconcileInfrastructureConfig struct {
	client      cnoclient.Client
	typedClient *configclient.Clientset
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

	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		// Fetch the infrastructure config
		infraConfig := &configv1.Infrastructure{}
		err := r.client.Default().CRClient().Get(ctx, request.NamespacedName, infraConfig)
		if err != nil {
			if apierrors.IsNotFound(err) {
				// Request object not found, could have been deleted after reconcile request.
				// Return and don't requeue
				log.Println("Object seems to have been deleted")
				return nil
			}
			// Error reading the object - requeue the request.
			err = fmt.Errorf("Error while reading infrastructures.%s/cluster: %w", configv1.GroupName, err)
			log.Println(err)
			return err
		}

		// Synchronizing VIPs does not require error handling as it performs an automatic migration
		// for a data structure introduced in OCP 4.12. The function does not operate on any
		// user-provided input, thus errors can only be a result of unhealthy cluster state.
		updatedInfraConfig := r.fieldSyncer.VipsSynchronize(infraConfig)

		updatedInfraConfig, err = r.fieldSyncer.SpecStatusSynchronize(updatedInfraConfig)
		if err != nil {
			err = fmt.Errorf("Error while synchronizing spec and status of infrastructures.%s/cluster: %w", configv1.GroupName, err)
			log.Println(err)

			r.status.SetDegraded(statusmanager.InfrastructureConfig, "SyncInfrastructureSpecAndStatus", err.Error())
			return err
		}

		// The "duplicated" logic below is because Update on custom CRDs is not modifying the Status subresource.
		if !reflect.DeepEqual(updatedInfraConfig.Spec, infraConfig.Spec) {
			if _, err = r.typedClient.ConfigV1().Infrastructures().Update(ctx, updatedInfraConfig, metav1.UpdateOptions{}); err != nil {
				err = fmt.Errorf("Error while client-side updating infrastructures.%s/cluster: %w", configv1.GroupName, err)
				log.Println(err)

				r.status.SetDegraded(statusmanager.InfrastructureConfig, "UpdateInfrastructureSpecOrStatus", err.Error())
				return err
			}
			log.Printf("Successfully synchronized infrastructure config.")
		}

		if !reflect.DeepEqual(updatedInfraConfig.Status, infraConfig.Status) {
			if _, err = r.typedClient.ConfigV1().Infrastructures().UpdateStatus(ctx, updatedInfraConfig, metav1.UpdateOptions{}); err != nil {
				err = fmt.Errorf("Error while client-side updating status of infrastructures.%s/cluster: %w", configv1.GroupName, err)
				log.Println(err)

				r.status.SetDegraded(statusmanager.InfrastructureConfig, "UpdateInfrastructureSpecOrStatus", err.Error())
				return err
			}
			log.Printf("Successfully synchronized infrastructure config status.")
		}

		return nil
	})
	if err != nil {
		return reconcile.Result{}, err
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
