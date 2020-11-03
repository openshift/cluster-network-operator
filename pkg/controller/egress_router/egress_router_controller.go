package egress_router

// egress router imnplements a controller for the egress router CNI plugin

import (
	"context"
	"log"

	netopv1 "github.com/openshift/cluster-network-operator/pkg/apis/network/v1"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Attach control loop to the manager and watch for Egress Router objects
func Add(mgr manager.Manager, status *statusmanager.StatusManager) error {
	r, err := newEgressRouterReconciler(mgr, status)
	if err != nil {
		return err
	}

	// Create a new controller
	c, err := controller.New("egress-router-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource EgressRouter.network.operator.openshift.io/v1
	err = c.Watch(&source.Kind{Type: &netopv1.EgressRouter{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &EgressRouterReconciler{}

type EgressRouterReconciler struct {
	mgr       manager.Manager
	clientset *kubernetes.Clientset
	status    *statusmanager.StatusManager
}

func newEgressRouterReconciler(mgr manager.Manager, status *statusmanager.StatusManager) (reconcile.Reconciler, error) {
	clientset, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		return nil, err
	}

	return &EgressRouterReconciler{
		mgr:       mgr,
		status:    status,
		clientset: clientset,
	}, nil
}

func (r EgressRouterReconciler) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	log.Printf("Reconciling egressrouter.network.operator.openshift.io %s\n", request.NamespacedName)

	obj := &netopv1.EgressRouter{}
	err := r.mgr.GetClient().Get(context.TODO(), request.NamespacedName, obj)

	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Printf("Egress Router %s seems to have been deleted\n", request.NamespacedName)
		}
	}

	return _,err
}

