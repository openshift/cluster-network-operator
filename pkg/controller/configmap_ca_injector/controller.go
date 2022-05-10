package configmapcainjector

import (
	"context"
	"fmt"
	"log"

	"github.com/openshift/cluster-network-operator/pkg/apply"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/util/validation"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	v1coreinformers "k8s.io/client-go/informers/core/v1"
	v1corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"

	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var labelSelector = labels.Set{names.TRUSTED_CA_BUNDLE_CONFIGMAP_LABEL: "true"}

func Add(mgr manager.Manager, status *statusmanager.StatusManager, c cnoclient.Client) error {
	reconciler := newReconciler(mgr, status, c)
	if reconciler == nil {
		return fmt.Errorf("failed to create reconciler")
	}

	return add(mgr, reconciler)
}

func newReconciler(mgr manager.Manager, status *statusmanager.StatusManager, c cnoclient.Client) *ReconcileConfigMapInjector {
	li := v1coreinformers.NewFilteredConfigMapInformer(
		c.Default().Kubernetes(),
		metav1.NamespaceAll,
		0, // no resync
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
		func(options *metav1.ListOptions) {
			options.LabelSelector = labelSelector.String()
		})
	ni := v1coreinformers.NewConfigMapInformer(
		c.Default().Kubernetes(),
		names.TRUSTED_CA_BUNDLE_CONFIGMAP_NS,
		0, // no resync
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})

	r := &ReconcileConfigMapInjector{
		client:        c,
		scheme:        mgr.GetScheme(),
		status:        status,
		labelInformer: li,
		labelLister:   v1corelisters.NewConfigMapLister(li.GetIndexer()),
		nsInformer:    ni,
		nsLister:      v1corelisters.NewConfigMapLister(ni.GetIndexer()),
	}

	c.Default().AddCustomInformer(r.labelInformer)
	c.Default().AddCustomInformer(r.nsInformer)
	return r
}

func add(mgr manager.Manager, r *ReconcileConfigMapInjector) error {
	// Create a new controller.
	c, err := controller.New("configmap-trust-bundle-injector-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Wire up the informers to the queue
	if err := c.Watch(&source.Informer{Informer: r.labelInformer},
		&handler.EnqueueRequestForObject{},
		predicate.ResourceVersionChangedPredicate{},
	); err != nil {
		return err
	}
	if err := c.Watch(&source.Informer{Informer: r.nsInformer},
		&handler.EnqueueRequestForObject{},
		predicate.NewPredicateFuncs(isCABundle),
	); err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &ReconcileConfigMapInjector{}

type ReconcileConfigMapInjector struct {
	client cnoclient.Client
	scheme *runtime.Scheme
	status *statusmanager.StatusManager

	labelInformer cache.SharedIndexInformer
	labelLister   v1corelisters.ConfigMapLister
	nsInformer    cache.SharedIndexInformer
	nsLister      v1corelisters.ConfigMapLister
}

// Reconcile expects requests to refers to configmaps of two different types.
// 1. a configmap named trusted-ca-bundle in namespace openshift-config-managed and will ensure that all configmaps with the label
// config.openshift.io/inject-trusted-cabundle = true have the certificate information stored in trusted-ca-bundle's ca-bundle.crt entry.
// 2. a configmap in any namespace with the label config.openshift.io/inject-trusted-cabundle = true and will insure that it contains the ca-bundle.crt
// entry in the configmap named trusted-ca-bundle in namespace openshift-config-managed.
func (r *ReconcileConfigMapInjector) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Printf("Reconciling configmap from  %s/%s\n", request.Namespace, request.Name)

	trustedCAbundleConfigMap, err := r.nsLister.ConfigMaps(names.TRUSTED_CA_BUNDLE_CONFIGMAP_NS).Get(names.TRUSTED_CA_BUNDLE_CONFIGMAP)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Printf("ConfigMap '%s/%s' not found; reconciliation will be skipped", names.TRUSTED_CA_BUNDLE_CONFIGMAP_NS, names.TRUSTED_CA_BUNDLE_CONFIGMAP)
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
	configMapsToChange := []*corev1.ConfigMap{}

	// The trusted-ca-bundle changed.
	if request.Name == names.TRUSTED_CA_BUNDLE_CONFIGMAP && request.Namespace == names.TRUSTED_CA_BUNDLE_CONFIGMAP_NS {
		cms, err := r.labelLister.List(labelSelector.AsSelector())
		if err != nil { // unlikely -- informer list
			log.Println(err)
			r.status.SetDegraded(statusmanager.InjectorConfig, "ListConfigMapError",
				fmt.Sprintf("Error getting the list of affected configmaps: %v", err))
			return reconcile.Result{}, err

		}
		configMapsToChange = cms
		log.Printf("%s changed, updating %d configMaps", names.TRUSTED_CA_BUNDLE_CONFIGMAP, len(configMapsToChange))
	} else {
		// Changing a single labeled configmap.

		// Get the requested object.
		cm, err := r.labelLister.ConfigMaps(request.Namespace).Get(request.Name)
		if err != nil {
			if apierrors.IsNotFound(err) {
				log.Printf("ConfigMap '%s/%s' not found; reconciliation will be skipped", request.Namespace, request.Name)
				return reconcile.Result{}, nil
			}
			// Unlikely -- this is an informer
			r.status.SetDegraded(statusmanager.InjectorConfig, "ClusterConfigError",
				fmt.Sprintf("failed to get configmap '%s/%s': %v", request.Namespace, request.Name, err))
			log.Println(err)
			return reconcile.Result{}, err
		}
		configMapsToChange = append(configMapsToChange, cm)
	}

	errs := []error{}

	for _, configMap := range configMapsToChange {
		err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			if existing, ok := configMap.Data[names.TRUSTED_CA_BUNDLE_CONFIGMAP_KEY]; ok && existing == string(trustedCAbundleData) {
				// Nothing to update the new and old configmap object would be the same.
				log.Printf("ConfigMap %s/%s %s unchanged, skipping", configMap.Namespace, configMap.Name, names.TRUSTED_CA_BUNDLE_CONFIGMAP_KEY)
				return nil
			}

			// create sparse object with only the keys we care about.
			// so that server-side-apply will DTRT.
			configMapToUpdate := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: configMap.Namespace,
					Name:      configMap.Name,
				},
				Data: map[string]string{
					names.TRUSTED_CA_BUNDLE_CONFIGMAP_KEY: string(trustedCAbundleData),
				},
			}

			err = apply.ApplyObject(ctx, r.client, configMapToUpdate, "configmap_ca")
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

func isCABundle(meta crclient.Object) bool {
	return (meta.GetName() == names.TRUSTED_CA_BUNDLE_CONFIGMAP && meta.GetNamespace() == names.TRUSTED_CA_BUNDLE_CONFIGMAP_NS)
}
