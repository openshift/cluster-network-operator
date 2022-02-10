package ingressconfig

import (
	"context"
	"log"
	"time"

	operv1 "github.com/openshift/api/operator/v1"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/cluster-network-operator/pkg/names"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// The periodic resync interval.
// We will re-run the reconciliation logic, even if the network configuration
// hasn't changed.
var ResyncPeriod = 3 * time.Minute

// ManifestPaths is the path to the manifest templates
// bad, but there's no way to pass configuration to the reconciler right now
var ManifestPath = "./bindata"

// Add creates a new ingressConfig controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager, status *statusmanager.StatusManager, _ *cnoclient.ClusterClient) error {

	return add(mgr, newIngressConfigReconciler(mgr.GetClient()))
}

// newIngressConfigReconciler returns a new reconcile.Reconciler
func newIngressConfigReconciler(client crclient.Client) *ReconcileIngressConfigs {
	return &ReconcileIngressConfigs{client: client}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r *ReconcileIngressConfigs) error {
	// create a controller and register watcher for ingresscontroller resource
	c, err := controller.New("ingress-config-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}
	err = c.Watch(&source.Kind{Type: &operv1.IngressController{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}
	return nil
}

var _ reconcile.Reconciler = &ReconcileIngressConfigs{}

// ReconcileIngressConfigs watches for updates to ingress controller configuration
// and sets the network policy related labels on the openshift-host-network namespace
type ReconcileIngressConfigs struct {
	client crclient.Client
}

// Reconcile sets the openshift-host-network namespaces' labels as per the
// endpointPublishingStrategy of the `default` ingress controller object.
// In particular, when the endpointPublishingStrategy is HostNetwork, it will
// add the "policy-group.network.openshift.io/ingress="" label and also add
// the "network.openshift.io/policy-group=ingress" label for legacy reasons
// to the host network namespace.
// When the endpointPublishingStrategy is changed to anything other than
// HostNetwork, it reconciles and removes these labels from the host network
// namespace.
func (r *ReconcileIngressConfigs) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	if request.Namespace != names.IngressControllerNamespace || request.Name != names.DefaultIngressControllerName {
		return reconcile.Result{}, nil
	}
	log.Printf("Reconciling update to IngressController %s/%s\n", request.Namespace, request.Name)
	ingressControllerConfig := &operv1.IngressController{TypeMeta: metav1.TypeMeta{APIVersion: operv1.GroupVersion.String(), Kind: "IngressController"}}
	err := r.client.Get(ctx, request.NamespacedName, ingressControllerConfig)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.Printf("Ingress Controller configuration %s was deleted", request.NamespacedName.String())
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected, since we set
			// the ownerReference (see https://kubernetes.io/docs/concepts/workloads/controllers/garbage-collection/).
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Printf("Unable to retrieve IngressController.operator.openshift.io object: %v", err)
		return reconcile.Result{}, err
	}
	addLabel := ingressControllerConfig.Status.EndpointPublishingStrategy != nil &&
		ingressControllerConfig.Status.EndpointPublishingStrategy.Type == operv1.HostNetworkStrategyType

	err = r.updatePolicyGroupLabelOnNamespace(ctx, names.HostNetworkNamespace, addLabel)
	if err != nil {
		log.Printf("Error setting the host network label on namespace %s: %v", names.HostNetworkNamespace, err)
		return reconcile.Result{}, err
	}
	return reconcile.Result{RequeueAfter: ResyncPeriod}, nil
}

// setLabelsOnNamespace sets the labels specified on the target namespace using the client API
func (r *ReconcileIngressConfigs) updatePolicyGroupLabelOnNamespace(ctx context.Context, targetNamespace string, add bool) error {
	var err error
	namespace := &corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: corev1.SchemeGroupVersion.String(), Kind: "Namespace"}}
	err = r.client.Get(ctx, types.NamespacedName{Name: targetNamespace}, namespace)
	if err != nil {
		// FIXME: abhat - this needs to be handled better. Currently we have no good way to tell
		// the difference as to whether the error is a result of
		// a) host-network namespace should have existed, but GET failed
		// b) host-network namespace should not have existed in the first place,
		//    and therefore this code should ideally not even have been called.
		// The right way to address this would be to not even spawn the ingress
		// controller if we are running in the context of a third party plugin
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	newNamespace := namespace.DeepCopy()
	existingLabels := newNamespace.GetLabels()
	if existingLabels == nil {
		existingLabels = map[string]string{}
	}
	if !add {
		delete(existingLabels, names.PolicyGroupLabelIngress)
		delete(existingLabels, names.PolicyGroupLabelLegacy)
	} else {
		existingLabels[names.PolicyGroupLabelIngress] = names.PolicyGroupLabelIngressValue
		existingLabels[names.PolicyGroupLabelLegacy] = names.PolicyGroupLabelLegacyValue
	}

	newNamespace.SetLabels(existingLabels)

	return r.client.Patch(context.TODO(), newNamespace, crclient.MergeFrom(namespace))
}
