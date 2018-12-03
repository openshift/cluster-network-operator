package networkconfig

import (
	"log"

	"github.com/openshift/cluster-network-operator/pkg/util/clusteroperator"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// newDaemonSetReconciler returns a new reconcile.Reconciler
func newDaemonSetReconciler(status *clusteroperator.StatusManager) *ReconcileDaemonSets {
	return &ReconcileDaemonSets{status: status}
}

var _ reconcile.Reconciler = &ReconcileDaemonSets{}

// ReconcileDaemonSets updates the ClusterOperator.Status according to the states of DaemonSet objects
type ReconcileDaemonSets struct {
	status *clusteroperator.StatusManager

	daemonSets []types.NamespacedName
}

func (r *ReconcileDaemonSets) SetDaemonSets(daemonSets []types.NamespacedName) {
	r.daemonSets = daemonSets
}

// Reconcile updates the ClusterOperator.Status to match the current state of the
// watched DaemonSets
func (r *ReconcileDaemonSets) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	found := false
	for _, ds := range r.daemonSets {
		if ds.Namespace == request.Namespace && ds.Name == request.Name {
			found = true
			break
		}
	}
	if !found {
		return reconcile.Result{}, nil
	}

	log.Printf("Reconciling update to DaemonSet %s/%s\n", request.Namespace, request.Name)
	r.status.SetFromDaemonSets(r.daemonSets)

	return reconcile.Result{RequeueAfter: ResyncPeriod}, nil
}
