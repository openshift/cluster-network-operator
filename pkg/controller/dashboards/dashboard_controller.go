package dashboards

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/openshift/cluster-network-operator/pkg/apply"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/render"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	v1coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	manifestDir = "bindata/dashboards"
)

var (
	//go:embed netstats.json
	netstatsContent string
	//go:embed ovn-health.json
	ovnHealthContent string
	dashboardRefs    []dashboardRef = []dashboardRef{
		{
			name:      "grafana-dashboard-network-stats",
			json:      netstatsContent,
			tplSuffix: "NetStats",
		},
		{
			name:      "grafana-dashboard-ovn-health",
			json:      ovnHealthContent,
			tplSuffix: "OVNHealth",
		},
	}
)

type dashboardRef struct {
	name      string
	json      string
	tplSuffix string
}

func Add(mgr manager.Manager, status *statusmanager.StatusManager, c cnoclient.Client) error {
	return add(mgr, newReconciler(mgr, status, c))
}

func newReconciler(mgr manager.Manager, status *statusmanager.StatusManager, c cnoclient.Client) *ReconcileDashboard {
	return &ReconcileDashboard{client: c, scheme: mgr.GetScheme(), status: status}
}

func add(mgr manager.Manager, r *ReconcileDashboard) error {
	c, err := controller.New("dashboard-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// watch for changes in all configmaps in our namespace
	cmInformer := v1coreinformers.NewConfigMapInformer(
		r.client.Default().Kubernetes(),
		names.DashboardNamespace,
		0, // don't resync
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
	)

	r.client.Default().AddCustomInformer(cmInformer) // Tell the ClusterClient about this informer

	return c.Watch(&source.Informer{Informer: cmInformer},
		&handler.EnqueueRequestForObject{},
		predicate.ResourceVersionChangedPredicate{},
		predicate.NewPredicateFuncs(func(object crclient.Object) bool {
			for _, ref := range dashboardRefs {
				if object.GetName() == ref.name {
					return true
				}
			}
			return false
		}),
	)
}

var _ reconcile.Reconciler = &ReconcileDashboard{}

type ReconcileDashboard struct {
	client cnoclient.Client
	scheme *runtime.Scheme
	status *statusmanager.StatusManager
}

func (r *ReconcileDashboard) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	klog.Infof("Reconcile dashboards")
	manifests, err := renderManifests()
	if err != nil {
		klog.Errorf("Failed to render dashboard manifests: %v", err)
		return reconcile.Result{}, err
	}
	err = r.applyManifests(ctx, manifests)
	if err != nil {
		klog.Errorf("Failed to apply dashboard manifests: %v", err)
		return reconcile.Result{}, err
	}

	// TODO: we might want to use r.status.SetDegraded / r.status.SetNotDegraded instead of throwing errors
	// might need some guidance...

	return reconcile.Result{}, nil
}

func renderManifests() ([]*unstructured.Unstructured, error) {
	data := render.MakeRenderData()
	data.Data["DashboardNamespace"] = names.DashboardNamespace
	for _, ref := range dashboardRefs {
		data.Data["DashboardName"+ref.tplSuffix] = ref.name
		data.Data["DashboardContent"+ref.tplSuffix] = ref.json
	}
	return render.RenderDir(manifestDir, &data)
}

func (r *ReconcileDashboard) applyManifests(ctx context.Context, manifests []*unstructured.Unstructured) error {
	klog.Infof("Applying dashboards manifests")
	for _, obj := range manifests {
		if err := apply.ApplyObject(ctx, r.client, obj, "dashboards"); err != nil {
			return fmt.Errorf("could not apply dashboard %s %s/%s: %w", obj.GroupVersionKind(), obj.GetNamespace(), obj.GetName(), err)
		}
	}
	return nil
}
