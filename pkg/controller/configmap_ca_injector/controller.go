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
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	v1coreinformers "k8s.io/client-go/informers/core/v1"
	v1corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var labelSelector = labels.Set{names.TrustedCABundleConfigMapLabel: "true"}

func Add(mgr manager.Manager, status *statusmanager.StatusManager, c cnoclient.Client, _ featuregates.FeatureGate) error {
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
		names.TrustedCABundleConfigMapNamespace,
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
	if err := c.Watch(&source.Informer{
		Informer: r.labelInformer,
		Handler:  &handler.EnqueueRequestForObject{},
		Predicates: []predicate.TypedPredicate[crclient.Object]{
			predicate.ResourceVersionChangedPredicate{},
		},
	}); err != nil {
		return err
	}
	if err := c.Watch(&source.Informer{
		Informer: r.nsInformer,
		Handler:  &handler.EnqueueRequestForObject{},
		Predicates: []predicate.TypedPredicate[crclient.Object]{
			predicate.NewPredicateFuncs(isCABundle),
		},
	}); err != nil {
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

// Reconcile injects openshift-config-managed/trusted-ca-bundle into labeled
// ConfigMaps. A bundle change or a request for a deleted target reconciles all
// remaining targets so that recovery does not hide other injection failures.
func (r *ReconcileConfigMapInjector) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	defer utilruntime.HandleCrash(r.status.SetDegradedOnPanicAndCrash)
	log.Printf("Reconciling configmap from  %s/%s\n", request.Namespace, request.Name)

	trustedCAbundleConfigMap, err := r.nsLister.ConfigMaps(names.TrustedCABundleConfigMapNamespace).Get(names.TrustedCABundleConfigMapName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Printf("ConfigMap '%s/%s' not found; reconciliation will be skipped", names.TrustedCABundleConfigMapNamespace, names.TrustedCABundleConfigMapName)
			return reconcile.Result{}, nil
		}
		log.Println(err)
		return reconcile.Result{}, err
	}
	_, trustedCAbundleData, err := validation.TrustBundleConfigMap(trustedCAbundleConfigMap, names.TrustedCABundleConfigMapKey)

	if err != nil {
		log.Println(err)
		r.status.SetDegraded(ctx, statusmanager.InjectorConfig, "InvalidInjectorConfig",
			fmt.Sprintf("Failed to validate trusted CA certificates in %s", trustedCAbundleConfigMap.Name))
		return reconcile.Result{}, err
	}
	// Build a list of configMaps.
	configMapsToChange := []*corev1.ConfigMap{}

	// An individual ConfigMap event normally updates only that target. If it has
	// disappeared from the cache, fall through to reconcile all remaining targets.
	if request.Name != names.TrustedCABundleConfigMapName || request.Namespace != names.TrustedCABundleConfigMapNamespace {
		cm, getErr := r.labelLister.ConfigMaps(request.Namespace).Get(request.Name)
		if getErr != nil && !apierrors.IsNotFound(getErr) {
			// Unlikely -- this is an informer
			r.status.SetDegraded(ctx, statusmanager.InjectorConfig, "ClusterConfigError",
				fmt.Sprintf("failed to get configmap '%s/%s': %v", request.Namespace, request.Name, getErr))
			return reconcile.Result{}, fmt.Errorf("failed to get configmap %s/%s: %w", request.Namespace, request.Name, getErr)
		}
		if getErr == nil {
			configMapsToChange = append(configMapsToChange, cm)
		}
	}

	listConfigMaps := func() ([]*corev1.ConfigMap, error) {
		configMaps, err := r.labelLister.List(labelSelector.AsSelector())
		if err != nil { // unlikely -- informer list
			r.status.SetDegraded(ctx, statusmanager.InjectorConfig, "ListConfigMapError",
				fmt.Sprintf("Error getting the list of affected configmaps: %v", err))
			return nil, fmt.Errorf("failed to list configmaps for CA injection: %w", err)
		}
		klog.Infof("Updating trusted CA bundle in %d ConfigMaps", len(configMaps))
		return configMaps, nil
	}

	// A bundle change or a deleted target requires checking all remaining targets
	// before clearing the shared injector failure state.
	checkingAllTargets := len(configMapsToChange) == 0
	if checkingAllTargets {
		configMapsToChange, err = listConfigMaps()
		if err != nil {
			return reconcile.Result{}, err
		}
	}

	errs := []error{}

	for i := 0; i < len(configMapsToChange); i++ {
		configMap := configMapsToChange[i]
		err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			needsOwner := len(configMap.Annotations[names.OpenShiftComponent]) == 0
			if existing, ok := configMap.Data[names.TrustedCABundleConfigMapKey]; !needsOwner && ok && existing == string(trustedCAbundleData) {
				// Nothing to update the new and old configmap object would be the same.
				log.Printf("ConfigMap %s/%s %s unchanged, skipping", configMap.Namespace, configMap.Name, names.TrustedCABundleConfigMapKey)
				return nil
			}

			// create sparse object with only the keys we care about.
			// so that server-side-apply will DTRT.
			configMapToUpdate := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: configMap.Namespace,
					Name:      configMap.Name,
					Annotations: map[string]string{
						names.OpenShiftComponent: configMap.Annotations[names.OpenShiftComponent],
					},
				},
				Data: map[string]string{
					names.TrustedCABundleConfigMapKey: string(trustedCAbundleData),
				},
			}
			// this lets a configmap writer to claim ownership
			if needsOwner {
				configMapToUpdate.Annotations[names.OpenShiftComponent] = trustedCAbundleConfigMap.Annotations[names.OpenShiftComponent]
			}

			return apply.ApplyObject(ctx, r.client, configMapToUpdate, "configmap_ca")
		})
		if apierrors.IsNotFound(err) || apierrors.HasStatusCause(err, corev1.NamespaceTerminatingCause) {
			klog.Infof("Skipping CA update for ConfigMap %s/%s: target or namespace is deleted or terminating", configMap.Namespace, configMap.Name)
			if !checkingAllTargets {
				remainingTargets, listErr := listConfigMaps()
				if listErr != nil {
					return reconcile.Result{}, listErr
				}
				// Expand this reconciliation once, excluding the target just attempted.
				for _, target := range remainingTargets {
					if target.Namespace != configMap.Namespace || target.Name != configMap.Name {
						configMapsToChange = append(configMapsToChange, target)
					}
				}
				checkingAllTargets = true
			}
			continue
		}
		if err != nil {
			klog.ErrorS(err, "Failed to update trusted CA ConfigMap", "namespace", configMap.Namespace, "name", configMap.Name)
			errs = append(errs, err)
			if len(errs) > 5 {
				r.status.MaybeSetDegraded(ctx, statusmanager.InjectorConfig, "ConfigMapUpdateFailure",
					"Too many errors seen when updating trusted CA configmaps")
				return reconcile.Result{}, fmt.Errorf("too many errors attempting to update configmaps with CA cert data")
			}
		}
	}
	if len(errs) > 0 {
		r.status.MaybeSetDegraded(ctx, statusmanager.InjectorConfig, "ConfigmapUpdateFailure",
			"some configmaps didn't fully update with CA cert. data")
		return reconcile.Result{}, fmt.Errorf("some configmaps didn't fully update with CA cert. data")
	}
	r.status.SetNotDegraded(ctx, statusmanager.InjectorConfig)
	return reconcile.Result{}, nil
}

func isCABundle(meta crclient.Object) bool {
	return (meta.GetName() == names.TrustedCABundleConfigMapName && meta.GetNamespace() == names.TrustedCABundleConfigMapNamespace)
}
