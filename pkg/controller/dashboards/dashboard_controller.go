package dashboards

import (
	"context"
	_ "embed"
	"fmt"

	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/apply"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/render"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
	manifest = "bindata/dashboards/configmaps.yaml"
)

var (
	//go:embed netstats.json
	netstatsContent string
	//go:embed ovn-health.json
	ovnHealthContent string
	dashboardRefs    []dashboardRef = []dashboardRef{
		{
			name: "grafana-dashboard-network-stats",
			json: netstatsContent,
			// Suffix used in configmap template; if modified, dashboards/configmaps.yaml should be changed accordingly
			tplSuffix: "NetStats",
		},
		{
			name: "grafana-dashboard-ovn-health",
			json: ovnHealthContent,
			// Suffix used in configmap template; if modified, dashboards/configmaps.yaml should be changed accordingly
			tplSuffix: "OVNHealth",
		},
	}
)

type dashboardRef struct {
	name      string
	json      string
	tplSuffix string
}

func Add(mgr manager.Manager, status *statusmanager.StatusManager, c cnoclient.Client, _ featuregates.FeatureGate) error {
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

	firstRun := true
	return c.Watch(&source.Informer{
		Informer: cmInformer,
		Handler:  &handler.EnqueueRequestForObject{},
		Predicates: []predicate.TypedPredicate[crclient.Object]{
			predicate.ResourceVersionChangedPredicate{},
			predicate.NewPredicateFuncs(func(object crclient.Object) bool {
				if firstRun {
					firstRun = false
					return true
				}
				for _, ref := range dashboardRefs {
					if object.GetName() == ref.name {
						return true
					}
				}
				return false
			}),
		},
	})
}

var _ reconcile.Reconciler = &ReconcileDashboard{}

type ReconcileDashboard struct {
	client cnoclient.Client
	scheme *runtime.Scheme
	status *statusmanager.StatusManager
}

func (r *ReconcileDashboard) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	klog.Info("Reconcile dashboards")

	// Fetch the Network.operator.openshift.io instance to get Network Type
	operConfig := &operv1.Network{TypeMeta: metav1.TypeMeta{APIVersion: operv1.GroupVersion.String(), Kind: "Network"}}
	err := r.client.Default().CRClient().Get(ctx, types.NamespacedName{Name: names.CLUSTER_CONFIG}, operConfig)
	if err != nil {
		err = fmt.Errorf("unable to retrieve Network.operator.openshift.io object: %w", err)
		klog.Error(err)
		r.status.SetDegraded(statusmanager.DashboardConfig, "DashboardError", err.Error())
		return reconcile.Result{}, err
	}

	err = r.applyManifests(ctx, operConfig)
	if err != nil {
		err = fmt.Errorf("failed to apply dashboard manifests: %w", err)
		klog.Error(err)
		r.status.SetDegraded(statusmanager.DashboardConfig, "DashboardError", err.Error())
		return reconcile.Result{}, err
	}

	r.status.SetNotDegraded(statusmanager.DashboardConfig)

	return reconcile.Result{}, nil
}

func (r *ReconcileDashboard) applyManifests(ctx context.Context, cfg *operv1.Network) error {
	klog.Info("Applying dashboards manifests")
	manifests, err := renderManifests(cfg)
	if err != nil {
		return fmt.Errorf("could not render dashboards manifests: %v", err)
	}
	for _, obj := range manifests {
		if err := apply.ApplyObject(ctx, r.client, obj, "dashboards"); err != nil {
			return fmt.Errorf("could not apply dashboard %s %s/%s: %w", obj.GroupVersionKind(), obj.GetNamespace(), obj.GetName(), err)
		}
	}
	return nil
}

func renderManifests(cfg *operv1.Network) ([]*unstructured.Unstructured, error) {
	data := render.MakeRenderData()
	data.Data["DashboardNamespace"] = names.DashboardNamespace
	data.Data["IsOVN"] = cfg.Spec.DefaultNetwork.Type == operv1.NetworkTypeOVNKubernetes
	for _, ref := range dashboardRefs {
		data.Data["DashboardName"+ref.tplSuffix] = ref.name
		data.Data["DashboardContent"+ref.tplSuffix] = ref.json
	}
	return render.RenderTemplate(manifest, &data)
}
