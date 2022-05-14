package statusmanager

import (
	"context"

	"github.com/openshift/cluster-network-operator/pkg/client"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	v1appsinformers "k8s.io/client-go/informers/apps/v1"
	v1appslisters "k8s.io/client-go/listers/apps/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// PodWatcher is a controller adjacent to the StatusManager that
// triggers a re-reconcile whenever an "interesting" daemonset, deployment,
// or statefulset is created / updated.
// Specifically, it watches for objects with the label
// "networkoperator.openshift.io/generates-operator-status" set.
type PodWatcher struct {
	onUpdate func()
}

// initInformersFor sets up the DaemonSet, Deployment, and StatefulSet informers
// for the given cluster and namespace
// minor hack: Hypershift doesn't have permission to create or watch Deployments and Daemonsets
// in the management cluster. So statefulSetOnly works around that :-/
func (s *StatusManager) initInformersFor(clusterName, namespace string, statefulSetOnly bool) {
	if !statefulSetOnly {
		inf := v1appsinformers.NewFilteredDaemonSetInformer(
			s.client.ClientFor(clusterName).Kubernetes(),
			namespace,
			0, // resync Period
			cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
			func(options *metav1.ListOptions) {
				options.LabelSelector = generateStatusSelector
			})
		s.client.ClientFor(clusterName).AddCustomInformer(inf)
		s.dsInformers[clusterName] = inf
		s.dsListers[clusterName] = v1appslisters.NewDaemonSetLister(inf.GetIndexer())

		inf = v1appsinformers.NewFilteredDeploymentInformer(
			s.client.ClientFor(clusterName).Kubernetes(),
			namespace,
			0, // resync Period
			cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
			func(options *metav1.ListOptions) {
				options.LabelSelector = generateStatusSelector
			})
		s.client.ClientFor(clusterName).AddCustomInformer(inf)
		s.depInformers[clusterName] = inf
		s.depListers[clusterName] = v1appslisters.NewDeploymentLister(inf.GetIndexer())
	}
	inf := v1appsinformers.NewFilteredStatefulSetInformer(
		s.client.ClientFor(clusterName).Kubernetes(),
		namespace,
		0, // resync Period
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
		func(options *metav1.ListOptions) {
			options.LabelSelector = generateStatusSelector
		})
	s.client.ClientFor(clusterName).AddCustomInformer(inf)
	s.ssInformers[clusterName] = inf
	s.ssListers[clusterName] = v1appslisters.NewStatefulSetLister(inf.GetIndexer())
}

// AddPodWatcher wires up the PodWatcher to the controller-manager.
func (s *StatusManager) AddPodWatcher(mgr manager.Manager) error {
	s.initInformersFor("", metav1.NamespaceAll, false)

	// If Hypershift is enable, also watch that single namespace
	if s.hyperShiftConfig.Enabled {
		s.initInformersFor(client.ManagementClusterName, s.hyperShiftConfig.Namespace, true)
	}

	pw := &PodWatcher{
		onUpdate: s.SetFromPods,
	}
	c, err := controller.New("pod-watcher", mgr, controller.Options{Reconciler: pw})
	if err != nil {
		return err
	}

	// Wire up the informers to the controller
	infs := []crcache.Informer{}
	for _, v := range s.dsInformers {
		infs = append(infs, v)
	}
	for _, v := range s.depInformers {
		infs = append(infs, v)
	}
	for _, v := range s.ssInformers {
		infs = append(infs, v)
	}

	for _, inf := range infs {
		if err := c.Watch(&source.Informer{Informer: inf},
			handler.EnqueueRequestsFromMapFunc(enqueueRP),
		); err != nil {
			return err
		}
	}
	return nil
}

// Reconcile triggers a re-update of Status.
func (p *PodWatcher) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	p.onUpdate()
	return reconcile.Result{}, nil
}

// enqueueRP ensure we always have, at most, a single request in the queue.
// by always enquing the same name, it will be coalesced
func enqueueRP(obj crclient.Object) []reconcile.Request {
	klog.Infof("Operand %s %s/%s updated, re-generating status", obj.GetObjectKind().GroupVersionKind().String(), obj.GetNamespace(), obj.GetName())
	return []reconcile.Request{
		{
			NamespacedName: types.NamespacedName{Name: "cluster"}},
	}
}
