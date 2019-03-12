package operconfig

import (
	"log"

	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// newPodReconciler returns a new reconcile.Reconciler
func newPodReconciler(status *statusmanager.StatusManager) *ReconcilePods {
	return &ReconcilePods{status: status}
}

var _ reconcile.Reconciler = &ReconcilePods{}

// ReconcilePods watches for updates to specified resources and then updates its StatusManager
type ReconcilePods struct {
	status *statusmanager.StatusManager

	resources []types.NamespacedName
}

func (r *ReconcilePods) SetResources(resources []types.NamespacedName) {
	r.resources = resources
}

// Reconcile updates the ClusterOperator.Status to match the current state of the
// watched Deployments/DaemonSets
func (r *ReconcilePods) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	found := false
	for _, name := range r.resources {
		if name.Namespace == request.Namespace && name.Name == request.Name {
			found = true
			break
		}
	}
	if !found {
		return reconcile.Result{}, nil
	}

	log.Printf("Reconciling update to %s/%s\n", request.Namespace, request.Name)
	r.status.SetFromPods()

	return reconcile.Result{RequeueAfter: ResyncPeriod}, nil
}
