package operconfig

import (
	"context"
	"fmt"
	"log"
	"reflect"
	"time"

	"github.com/pkg/errors"

	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/apply"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/network"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	v1 "k8s.io/client-go/kubernetes/typed/core/v1"
	// "k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// ResyncPeriod will re-run the reconciliation logic, even if the network configuration
// hasn't changed.
var ResyncPeriod = 5 * time.Minute

// ManifestPath is the path to the manifest templates
// bad, but there's no way to pass configuration to the reconciler right now
var ManifestPath = "./bindata"

// Add creates a new OperConfig Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager, status *statusmanager.StatusManager) error {
	return add(mgr, newReconciler(mgr, status))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager, status *statusmanager.StatusManager) *ReconcileOperConfig {
	configv1.Install(mgr.GetScheme())
	operv1.Install(mgr.GetScheme())
	return &ReconcileOperConfig{
		client:        mgr.GetClient(),
		scheme:        mgr.GetScheme(),
		status:        status,
		mapper:        mgr.GetRESTMapper(),
		mgr:           mgr,
		podReconciler: newPodReconciler(status),
	}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r *ReconcileOperConfig) error {
	// Create a new controller
	c, err := controller.New("operconfig-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource Network
	err = c.Watch(&source.Kind{Type: &operv1.Network{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	// Likewise for the Pod reconciler
	c, err = controller.New("pod-controller", mgr, controller.Options{Reconciler: r.podReconciler})
	if err != nil {
		return err
	}
	err = c.Watch(&source.Kind{Type: &appsv1.DaemonSet{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}
	err = c.Watch(&source.Kind{Type: &appsv1.Deployment{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &ReconcileOperConfig{}

// ReconcileOperConfig reconciles a Network.operator.openshift.io object
type ReconcileOperConfig struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client              client.Client
	scheme              *runtime.Scheme
	status              *statusmanager.StatusManager
	mapper              meta.RESTMapper
	mgr                 manager.Manager
	podReconciler       *ReconcilePods
	namespaceController [2]factory.Controller
	eventRecorder       [2]events.Recorder
	// namespaceGetter     v1.NamespacesGetter
}

// Reconcile updates the state of the cluster to match that which is desired
// in the operator configuration (Network.operator.openshift.io)
func (r *ReconcileOperConfig) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	log.Printf("Reconciling Network.operator.openshift.io %s\n", request.Name)

	// We won't create more than one network
	if request.Name != names.OPERATOR_CONFIG {
		log.Printf("Ignoring Network.operator.openshift.io without default name")
		return reconcile.Result{}, nil
	}

	// Fetch the Network.operator.openshift.io instance
	operConfig := &operv1.Network{TypeMeta: metav1.TypeMeta{APIVersion: operv1.GroupVersion.String(), Kind: "Network"}}
	err := r.client.Get(context.TODO(), request.NamespacedName, operConfig)
	if err != nil {
		if apierrors.IsNotFound(err) {
			r.status.SetDegraded(statusmanager.OperatorConfig, "NoOperatorConfig",
				fmt.Sprintf("Operator configuration %s was deleted", request.NamespacedName.String()))
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected, since we set
			// the ownerReference (see https://kubernetes.io/docs/concepts/workloads/controllers/garbage-collection/).
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Printf("Unable to retrieve Network.operator.openshift.io object: %v", err)
		// FIXME: operator status?
		return reconcile.Result{}, err
	}

	// Merge in the cluster configuration, in case the administrator has updated some "downstream" fields
	// This will also commit the change back to the apiserver.
	if err := r.MergeClusterConfig(context.TODO(), operConfig); err != nil {
		log.Printf("Failed to merge the cluster configuration: %v", err)
		r.status.SetDegraded(statusmanager.OperatorConfig, "MergeClusterConfig",
			fmt.Sprintf("Internal error while merging cluster configuration and operator configuration: %v", err))
		return reconcile.Result{}, err
	}

	// Convert to a canonicalized form
	network.Canonicalize(&operConfig.Spec)

	// Validate the configuration
	if err := network.Validate(&operConfig.Spec); err != nil {
		log.Printf("Failed to validate Network.operator.openshift.io.Spec: %v", err)
		r.status.SetDegraded(statusmanager.OperatorConfig, "InvalidOperatorConfig",
			fmt.Sprintf("The operator configuration is invalid (%v). Use 'oc edit network.operator.openshift.io cluster' to fix.", err))
		return reconcile.Result{}, err
	}

	// Retrieve the previously applied operator configuration
	prev, err := GetAppliedConfiguration(context.TODO(), r.client, operConfig.ObjectMeta.Name)
	if err != nil {
		log.Printf("Failed to retrieve previously applied configuration: %v", err)
		// FIXME: operator status?
		return reconcile.Result{}, err
	}
	// up-convert Prev by filling defaults
	if prev != nil {
		network.FillDefaults(prev, prev)
	}

	// Fill all defaults explicitly
	network.FillDefaults(&operConfig.Spec, prev)

	// Compare against previous applied configuration to see if this change
	// is safe.
	if prev != nil {
		// Check if the operator is put in the 'Network Migration' mode.
		if _, ok := operConfig.GetAnnotations()[names.NetworkMigrationAnnotation]; !ok {
			// We may need to fill defaults here -- sort of as a poor-man's
			// upconversion scheme -- if we add additional fields to the config.
			err = network.IsChangeSafe(prev, &operConfig.Spec)
			if err != nil {
				log.Printf("Not applying unsafe change: %v", err)
				r.status.SetDegraded(statusmanager.OperatorConfig, "InvalidOperatorConfig",
					fmt.Sprintf("Not applying unsafe configuration change: %v. Use 'oc edit network.operator.openshift.io cluster' to undo the change.", err))
				return reconcile.Result{}, err
			}
		}
	}

	// Bootstrap any resources
	bootstrapResult, err := network.Bootstrap(&operConfig.Spec, r.client)
	if err != nil {
		log.Printf("Failed to reconcile platform networking resources: %v", err)
		r.status.SetDegraded(statusmanager.OperatorConfig, "BootstrapError",
			fmt.Sprintf("Internal error while reconciling platform networking resources: %v", err))
		return reconcile.Result{}, err
	}

	// Generate the objects
	objs, err := network.Render(&operConfig.Spec, bootstrapResult, ManifestPath)
	if err != nil {
		log.Printf("Failed to render: %v", err)
		r.status.SetDegraded(statusmanager.OperatorConfig, "RenderError",
			fmt.Sprintf("Internal error while rendering operator configuration: %v", err))
		return reconcile.Result{}, err
	}

	// The first object we create should be the record of our applied configuration. The last object we create is config.openshift.io/v1/Network.Status
	app, err := AppliedConfiguration(operConfig)
	if err != nil {
		log.Printf("Failed to render applied: %v", err)
		r.status.SetDegraded(statusmanager.OperatorConfig, "RenderError",
			fmt.Sprintf("Internal error while recording new operator configuration: %v", err))
		return reconcile.Result{}, err
	}
	objs = append([]*uns.Unstructured{app}, objs...)

	// Set up the Pod reconciler before we start creating DaemonSets/Deployments
	daemonSets := []types.NamespacedName{}
	deployments := []types.NamespacedName{}
	relatedObjects := []configv1.ObjectReference{}
	for _, obj := range objs {
		if obj.GetAPIVersion() == "apps/v1" && obj.GetKind() == "DaemonSet" {
			daemonSets = append(daemonSets, types.NamespacedName{Namespace: obj.GetNamespace(), Name: obj.GetName()})
		} else if obj.GetAPIVersion() == "apps/v1" && obj.GetKind() == "Deployment" {
			deployments = append(deployments, types.NamespacedName{Namespace: obj.GetNamespace(), Name: obj.GetName()})
		}
		restMapping, err := r.mapper.RESTMapping(obj.GroupVersionKind().GroupKind())
		if err != nil {
			log.Printf("Failed to get REST mapping for storing related object: %v", err)
			continue
		}
		relatedObjects = append(relatedObjects, configv1.ObjectReference{
			Group:     obj.GetObjectKind().GroupVersionKind().Group,
			Resource:  restMapping.Resource.Resource,
			Name:      obj.GetName(),
			Namespace: obj.GetNamespace(),
		})
	}

	// Namespace finalization setup.

	// We need a typed client, because we cannot access the finalizer from the controller-runtime.
	// This is because finalizers are a subresource, but just for namespaces.
	clientset, err := kubernetes.NewForConfig(r.mgr.GetConfig())
	if err != nil {
		err = errors.Wrapf(err, "Failed to get clientset for namespace finalizer controllers: %v", err)
		return reconcile.Result{}, err
	}

	finalizeNamespaces := []string{"openshift-multus", "openshift-sdn"}

	for i, usingnamespace := range finalizeNamespaces {
		var c *finalizerController
		c = &finalizerController{
			namespaceName:  usingnamespace,
			client:         r.client,
			typedclientset: clientset,
		}
		var eventif v1.EventInterface
		var objectref apiv1.ObjectReference

		log.Printf("!bang -- CREATING CONTROLLER: %s", usingnamespace)
		r.eventRecorder[i] = events.NewRecorder(eventif, usingnamespace, &objectref)
		r.namespaceController[i] = factory.New().ResyncEvery(time.Second).WithSync(c.sync).WithInformers().ToController(usingnamespace+"-ns-controller", r.eventRecorder[i])
	}

	for j := range r.namespaceController {
		go r.namespaceController[j].Run(context.Background(), 1)
	}

	relatedObjects = append(relatedObjects, configv1.ObjectReference{
		Resource: "namespaces",
		Name:     names.APPLIED_NAMESPACE,
	})

	r.status.SetDaemonSets(daemonSets)
	r.status.SetDeployments(deployments)
	r.status.SetRelatedObjects(relatedObjects)

	allResources := []types.NamespacedName{}
	allResources = append(allResources, daemonSets...)
	allResources = append(allResources, deployments...)
	r.podReconciler.SetResources(allResources)

	// Apply the objects to the cluster
	for _, obj := range objs {
		// Mark the object to be GC'd if the owner is deleted.
		if err := controllerutil.SetControllerReference(operConfig, obj, r.scheme); err != nil {
			err = errors.Wrapf(err, "could not set reference for (%s) %s/%s", obj.GroupVersionKind(), obj.GetNamespace(), obj.GetName())
			log.Println(err)
			r.status.SetDegraded(statusmanager.OperatorConfig, "InternalError",
				fmt.Sprintf("Internal error while updating operator configuration: %v", err))
			return reconcile.Result{}, err
		}

		// Open question: should an error here indicate we will never retry?
		if err := apply.ApplyObject(context.TODO(), r.client, obj); err != nil {
			err = errors.Wrapf(err, "could not apply (%s) %s/%s", obj.GroupVersionKind(), obj.GetNamespace(), obj.GetName())
			log.Println(err)

			// Ignore errors if we've asked to do so.
			anno := obj.GetAnnotations()
			if anno != nil {
				if _, ok := anno[names.IgnoreObjectErrorAnnotation]; ok {
					log.Println("Object has ignore-errors annotation set, continuing")
					continue
				}
			}
			r.status.SetDegraded(statusmanager.OperatorConfig, "ApplyOperatorConfig",
				fmt.Sprintf("Error while updating operator configuration: %v", err))
			return reconcile.Result{}, err
		}
	}

	// Run a pod status check just to clear any initial inconsitencies at startup of the CNO
	r.status.SetFromPods()

	// Update Network.config.openshift.io.Status
	status, err := r.ClusterNetworkStatus(context.TODO(), operConfig)
	if err != nil {
		log.Printf("Could not generate network status: %v", err)
		r.status.SetDegraded(statusmanager.OperatorConfig, "StatusError",
			fmt.Sprintf("Could not update cluster configuration status: %v", err))
		return reconcile.Result{}, err
	}
	if status != nil {
		// Don't set the owner reference in this case -- we're updating
		// the status of our owner.
		if err := apply.ApplyObject(context.TODO(), r.client, status); err != nil {
			err = errors.Wrapf(err, "could not apply (%s) %s/%s", status.GroupVersionKind(), status.GetNamespace(), status.GetName())
			log.Println(err)
			r.status.SetDegraded(statusmanager.OperatorConfig, "StatusError",
				fmt.Sprintf("Could not update cluster configuration status: %v", err))
			return reconcile.Result{}, err
		}
	}

	r.status.SetNotDegraded(statusmanager.OperatorConfig)

	// All was successful. Request that this be re-triggered after ResyncPeriod,
	// so we can reconcile state again.
	return reconcile.Result{RequeueAfter: ResyncPeriod}, nil
}

type finalizerController struct {
	namespaceName  string
	client         client.Client
	typedclientset *kubernetes.Clientset
}

func (c finalizerController) sync(ctx context.Context, syncCtx factory.SyncContext) error {

	// !bang

	// Query for the namespace.
	ns := &apiv1.Namespace{}
	err := c.client.Get(context.Background(), client.ObjectKey{
		Name: c.namespaceName,
	}, ns)

	log.Printf("!bang [%v] Namespace finalizers BEFORE?: %+v", c.namespaceName, ns.Spec.Finalizers)

	if err != nil {
		// We don't care if it's not found, that's probably good.
		if apierrors.IsNotFound(err) {
			log.Printf("!bang [%v] IS NOT FOUND", c.namespaceName)
			return nil
		}

		err = errors.Wrapf(err, "could not query for namespace %s", c.namespaceName)
		log.Println(err)
		return err
	}

	// Next, check if it's been deleted.
	// We don't care anymore if it's not deleted.
	if ns.DeletionTimestamp == nil {
		log.Printf("!bang [%v] ITS NOT DELETED", c.namespaceName)
		return nil
	}

	// Now we emulate the Kubernetes finalizer, so we essentially honor it but take it over.
	// allow one minute of grace for most things to terminate.
	deletedMoreThanAMinute := ns.DeletionTimestamp.Time.Add(1 * time.Minute).Before(time.Now())
	if !deletedMoreThanAMinute {
		syncCtx.Queue().AddAfter(c.namespaceName, 1*time.Minute)
		return nil
	}

	// Check out how many pods there are...
	pods := &apiv1.PodList{}
	err = c.client.List(context.TODO(), pods, client.InNamespace(c.namespaceName))

	if err != nil {
		err = errors.Wrapf(err, "could not query for pods %s", c.namespaceName)
		log.Println(err)
		return err
	}

	log.Printf("!bang [%v] PODSIZE!!!!!!!!!!!!?: %v", c.namespaceName, len(pods.Items))
	// Keep this running until the pods are gone...
	if len(pods.Items) > 0 {
		return nil
	}

	// Check out how many daemonsets there are...
	dslist := &appsv1.DaemonSetList{}
	err = c.client.List(context.TODO(), dslist, client.InNamespace(c.namespaceName))

	log.Printf("!bang [%v] DSSIZE!!!!!!!!!!!!?: %v", c.namespaceName, len(dslist.Items))

	if err != nil {
		err = errors.Wrapf(err, "could not query for daemonsets %s", c.namespaceName)
		log.Println(err)
		return err
	}

	// Keep this running until the ds'es are gone...
	if len(dslist.Items) > 0 {
		return nil
	}

	ns = ns.DeepCopy()

	// Strip out ONLY the Kubernetes finalizer, this way other finalizers are respected.
	newFinalizers := []apiv1.FinalizerName{}
	for _, curr := range ns.Spec.Finalizers {
		if curr == apiv1.FinalizerKubernetes {
			continue
		}
		newFinalizers = append(newFinalizers, curr)
	}
	if reflect.DeepEqual(newFinalizers, ns.Spec.Finalizers) {
		return nil
	}
	ns.Spec.Finalizers = newFinalizers
	log.Printf("!bang [%v] Namespace finalizers AFTER?: %+v", c.namespaceName, ns.Spec.Finalizers)

	coreclient := c.typedclientset.CoreV1()

	// ---------------------------------------------------------------------------------- this can probably be removed
	// remove this because it
	// And try to update it...
	updatedns, err := coreclient.Namespaces().Update(context.TODO(), ns, metav1.UpdateOptions{})
	if err != nil {
		err = errors.Wrapf(err, "Error updating namespace: %s", err)
		return err
	}
	log.Printf("!bang [%v] Namespace finalizers RESULT?: %+v", c.namespaceName, updatedns.Spec.Finalizers)
	// ---------------------------------------------------------------------------------- this can probably be removed

	// Run the finalizers now with our updated namespace object (which now is without the kube finalizer)
	_, err = coreclient.Namespaces().Finalize(context.TODO(), ns, metav1.UpdateOptions{})

	if err != nil {
		err = errors.Wrapf(err, "Error running Finalize on namespace %s: %s", c.namespaceName, err)
		return err
	}

	// ---------------------------------------------------------------------- double check for finalizers.
	// ...remove this later.
	checknsgain := &apiv1.Namespace{}
	err = c.client.Get(context.Background(), client.ObjectKey{
		Name: c.namespaceName,
	}, checknsgain)
	// ---------------------------------------------------------------------- double check for finalizers.

	log.Printf("!bang [%v] Namespace finalizers ARE DONE!: %+v", c.namespaceName, checknsgain.Spec.Finalizers)

	return nil

}
