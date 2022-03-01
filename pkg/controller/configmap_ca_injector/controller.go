package configmapcainjector

import (
	"context"
	"fmt"
	"log"

	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/util/validation"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"

	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

func Add(mgr manager.Manager, status *statusmanager.StatusManager, _ *cnoclient.Client) error {
	reconciler := newReconciler(mgr, status)
	if reconciler == nil {
		return fmt.Errorf("failed to create reconciler")
	}

	return add(mgr, reconciler)
}

func newReconciler(mgr manager.Manager, status *statusmanager.StatusManager) reconcile.Reconciler {
	return &ReconcileConfigMapInjector{client: mgr.GetClient(), scheme: mgr.GetScheme(), status: status}
}

func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller.
	c, err := controller.New("configmap-trust-bundle-injector-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// The events fire for changes/creation of the trusted-ca-bundle and any configmaps with the
	// label "config.openshift.io/inject-trusted-cabundle".
	pred := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			return shouldUpdateConfigMaps(e.ObjectNew)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false
		},
		CreateFunc: func(e event.CreateEvent) bool {
			return shouldUpdateConfigMaps(e.Object)
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return shouldUpdateConfigMaps(e.Object)
		},
	}

	err = c.Watch(&source.Kind{Type: &corev1.ConfigMap{}}, &handler.EnqueueRequestForObject{}, pred)
	if err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &ReconcileConfigMapInjector{}

type ReconcileConfigMapInjector struct {
	client crclient.Client
	scheme *runtime.Scheme
	status *statusmanager.StatusManager
}

// Reconcile expects requests to refers to configmaps of two different types.
// 1. a configmap named trusted-ca-bundle in namespace openshift-config-managed and will ensure that all configmaps with the label
// config.openshift.io/inject-trusted-cabundle = true have the certificate information stored in trusted-ca-bundle's ca-bundle.crt entry.
// 2. a configmap in any namespace with the label config.openshift.io/inject-trusted-cabundle = true and will insure that it contains the ca-bundle.crt
// entry in the configmap named trusted-ca-bundle in namespace openshift-config-managed.
func (r *ReconcileConfigMapInjector) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Printf("Reconciling configmap from  %s/%s\n", request.Namespace, request.Name)

	trustedCAbundleConfigMap := &corev1.ConfigMap{}
	trustedCAbundleConfigMapName := types.NamespacedName{
		Namespace: names.TRUSTED_CA_BUNDLE_CONFIGMAP_NS,
		Name:      names.TRUSTED_CA_BUNDLE_CONFIGMAP,
	}
	err := r.client.Get(ctx, trustedCAbundleConfigMapName, trustedCAbundleConfigMap)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Printf("ConfigMap '%s/%s' not found; reconciliation will be skipped", trustedCAbundleConfigMapName.Namespace, trustedCAbundleConfigMapName.Name)
			return reconcile.Result{}, nil
		}
		log.Println(err)
		return reconcile.Result{}, err
	}
	_, trustedCAbundleData, err := validation.TrustBundleConfigMap(trustedCAbundleConfigMap)

	if err != nil {
		log.Println(err)
		r.status.SetDegraded(statusmanager.InjectorConfig, "InvalidInjectorConfig",
			fmt.Sprintf("Failed to validate trusted CA certificates in %s", trustedCAbundleConfigMap.Name))
		return reconcile.Result{}, err
	}
	// Build a list of configMaps.
	configMapsToChange := []corev1.ConfigMap{}

	// The trusted-ca-bundle changed.
	if request.Name == names.TRUSTED_CA_BUNDLE_CONFIGMAP && request.Namespace == names.TRUSTED_CA_BUNDLE_CONFIGMAP_NS {

		configMapList := &corev1.ConfigMapList{}
		matchingLabels := &crclient.MatchingLabels{names.TRUSTED_CA_BUNDLE_CONFIGMAP_LABEL: "true"}
		err = r.client.List(ctx, configMapList, matchingLabels)
		if err != nil {
			log.Println(err)
			r.status.SetDegraded(statusmanager.InjectorConfig, "ListConfigMapError",
				fmt.Sprintf("Error getting the list of affected configmaps: %v", err))
			return reconcile.Result{}, err
		}
		configMapsToChange = configMapList.Items
		log.Printf("%s changed, updating %d configMaps", names.TRUSTED_CA_BUNDLE_CONFIGMAP, len(configMapsToChange))
	} else {
		// Changing a single labeled configmap.

		// Get the requested object.
		requestedCAbundleConfigMap := &corev1.ConfigMap{}
		requestedCAbundleConfigMapName := types.NamespacedName{
			Namespace: request.Namespace,
			Name:      request.Name,
		}
		err = r.client.Get(ctx, requestedCAbundleConfigMapName, requestedCAbundleConfigMap)
		if err != nil {
			if apierrors.IsNotFound(err) {
				log.Printf("ConfigMap '%s/%s' not found; reconciliation will be skipped", request.Namespace, request.Name)
				return reconcile.Result{}, nil
			}
			r.status.SetDegraded(statusmanager.InjectorConfig, "ClusterConfigError",
				fmt.Sprintf("failed to get configmap '%s/%s': %v", request.Namespace, request.Name, err))
			log.Println(err)
			return reconcile.Result{}, err
		}
		configMapsToChange = append(configMapsToChange, *requestedCAbundleConfigMap)
	}

	errs := []error{}

	for _, configMap := range configMapsToChange {
		err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			retrievedConfigMap := &corev1.ConfigMap{}
			err = r.client.Get(ctx, types.NamespacedName{Namespace: configMap.Namespace, Name: configMap.Name}, retrievedConfigMap)
			if err != nil {
				if errors.IsNotFound(err) {
					return nil
				}
				log.Println(err)
				return err
			}
			configMapToUpdate := retrievedConfigMap.DeepCopy()
			if configMapToUpdate.Data == nil {
				configMapToUpdate.Data = map[string]string{names.TRUSTED_CA_BUNDLE_CONFIGMAP_KEY: string(trustedCAbundleData)}
			} else {
				configMapToUpdate.Data[names.TRUSTED_CA_BUNDLE_CONFIGMAP_KEY] = string(trustedCAbundleData)
			}
			if equality.Semantic.DeepEqual(configMapToUpdate, retrievedConfigMap) {
				// Nothing to update the new and old configmap object would be the same.
				return nil
			}
			err = r.client.Update(ctx, configMapToUpdate)
			if err != nil {
				log.Println(err)
				return err
			}
			return nil
		})
		if err != nil {
			errs = append(errs, err)
			if len(errs) > 5 {
				r.status.SetDegraded(statusmanager.InjectorConfig, "ConfigMapUpdateFailure",
					"Too many errors seen when updating trusted CA configmaps")
				return reconcile.Result{}, fmt.Errorf("Too many errors attempting to update configmaps with CA cert. data")
			}
		}
	}
	if len(errs) > 0 {
		r.status.SetDegraded(statusmanager.InjectorConfig, "ConfigmapUpdateFailure",
			"some configmaps didn't fully update with CA cert. data")
		return reconcile.Result{}, fmt.Errorf("some configmaps didn't fully update with CA cert. data")
	}
	r.status.SetNotDegraded(statusmanager.InjectorConfig)
	return reconcile.Result{}, nil
}

func shouldUpdateConfigMaps(meta metav1.Object) bool {
	return meta.GetLabels()[names.TRUSTED_CA_BUNDLE_CONFIGMAP_LABEL] == "true" ||
		(meta.GetName() == names.TRUSTED_CA_BUNDLE_CONFIGMAP && meta.GetNamespace() == names.TRUSTED_CA_BUNDLE_CONFIGMAP_NS)
}
