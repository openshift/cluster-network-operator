package tls

import (
	"context"
	"reflect"

	configv1 "github.com/openshift/api/config/v1"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/hypershift"
	"github.com/openshift/cluster-network-operator/pkg/names"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// ReconcileTLS watches for TLS profile changes and triggers operator restart
type ReconcileTLS struct {
	client         cnoclient.Client
	triggerRestart context.CancelFunc
}

// Add creates a new TLS restart controller and adds it to the Manager
func Add(mgr manager.Manager, client cnoclient.Client, triggerRestart context.CancelFunc) error {
	r := &ReconcileTLS{
		client:         client,
		triggerRestart: triggerRestart,
	}

	c, err := controller.New("tls-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch APIServer for TLS profile changes
	err = c.Watch(source.Kind(mgr.GetCache(), &configv1.APIServer{}, &handler.TypedEnqueueRequestForObject[*configv1.APIServer]{},
		predicate.TypedFuncs[*configv1.APIServer]{
			CreateFunc: func(evt event.TypedCreateEvent[*configv1.APIServer]) bool {
				// Don't reconcile on initial creation
				return false
			},
			UpdateFunc: func(evt event.TypedUpdateEvent[*configv1.APIServer]) bool {
				if evt.ObjectOld == nil || evt.ObjectNew == nil {
					return false
				}

				oldAPI := evt.ObjectOld
				newAPI := evt.ObjectNew

				// Only trigger on TLS profile or adherence changes
				tlsProfileChanged := !reflect.DeepEqual(oldAPI.Spec.TLSSecurityProfile, newAPI.Spec.TLSSecurityProfile)
				adherenceChanged := oldAPI.Spec.TLSAdherence != newAPI.Spec.TLSAdherence

				return tlsProfileChanged || adherenceChanged
			},
		},
	))
	if err != nil {
		return err
	}

	// In HyperShift mode, also watch HostedControlPlane
	hc := hypershift.NewHyperShiftConfig()
	if hc.Enabled {
		// Create a dynamic informer for HostedControlPlane in the management cluster
		dynClient := client.ClientFor(names.ManagementClusterName).Dynamic()
		hostedControlPlaneGVR := hypershift.HostedControlPlaneGVK.GroupVersion().WithResource("hostedcontrolplanes")
		hostedControlPlaneInformer := cache.NewSharedIndexInformer(
			cache.ToListWatcherWithWatchListSemantics(&cache.ListWatch{
				ListWithContextFunc: func(ctx context.Context, options metav1.ListOptions) (runtime.Object, error) {
					return dynClient.Resource(hostedControlPlaneGVR).Namespace(hc.Namespace).List(ctx, options)
				},
				WatchFuncWithContext: func(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
					return dynClient.Resource(hostedControlPlaneGVR).Namespace(hc.Namespace).Watch(ctx, options)
				},
			}, dynClient),
			&uns.Unstructured{},
			0, // don't resync
			cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
		)

		client.ClientFor(names.ManagementClusterName).AddCustomInformer(hostedControlPlaneInformer)

		err = c.Watch(&source.Informer{
			Informer: hostedControlPlaneInformer,
			Handler: handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj crclient.Object) []reconcile.Request {
				return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: "cluster"}}}
			}),
			Predicates: []predicate.TypedPredicate[crclient.Object]{
				predicate.NewPredicateFuncs(func(obj crclient.Object) bool {
					// Only watch our specific HostedControlPlane
					return obj.GetName() == hc.Name && obj.GetNamespace() == hc.Namespace
				}),
				predicate.Funcs{
					CreateFunc: func(evt event.CreateEvent) bool {
						// Don't reconcile on initial creation/add events
						return false
					},
					UpdateFunc: func(evt event.UpdateEvent) bool {
						newObj, ok := evt.ObjectNew.(*uns.Unstructured)
						if !ok {
							return false
						}

						oldObj, ok := evt.ObjectOld.(*uns.Unstructured)
						if !ok {
							return false
						}

						// Check if TLS profile or adherence changed
						oldTLSProfile, _, _ := uns.NestedFieldCopy(oldObj.Object, "spec", "configuration", "apiServer", "tlsSecurityProfile")
						newTLSProfile, _, _ := uns.NestedFieldCopy(newObj.Object, "spec", "configuration", "apiServer", "tlsSecurityProfile")

						oldAdherence, _, _ := uns.NestedString(oldObj.Object, "spec", "configuration", "apiServer", "tlsAdherence")
						newAdherence, _, _ := uns.NestedString(newObj.Object, "spec", "configuration", "apiServer", "tlsAdherence")

						// Only reconcile if TLS profile or adherence changed
						return !reflect.DeepEqual(oldTLSProfile, newTLSProfile) || oldAdherence != newAdherence
					},
				},
			},
		})
		if err != nil {
			return err
		}
	}

	return nil
}

func (r *ReconcileTLS) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	klog.Infof("TLS profile or adherence changed, triggering graceful operator restart")

	r.triggerRestart()

	return reconcile.Result{}, nil
}
