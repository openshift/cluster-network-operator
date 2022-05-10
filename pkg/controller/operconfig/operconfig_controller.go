package operconfig

import (
	"context"
	"fmt"
	"log"
	"reflect"
	"strings"
	"time"

	"github.com/pkg/errors"

	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/apply"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/network"
	"github.com/openshift/cluster-network-operator/pkg/platform"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	v1coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/tools/cache"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
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

// Add creates a new OperConfig Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager, status *statusmanager.StatusManager, c cnoclient.Client) error {
	return add(mgr, newReconciler(mgr, status, c))
}

const ControllerName = "operconfig"

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager, status *statusmanager.StatusManager, c cnoclient.Client) *ReconcileOperConfig {
	return &ReconcileOperConfig{
		client:        c,
		scheme:        mgr.GetScheme(),
		status:        status,
		mapper:        mgr.GetRESTMapper(),
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

	// Watch for changes to primary resource Network (as long as the spec changes)
	err = c.Watch(&source.Kind{Type: &operv1.Network{}}, &handler.EnqueueRequestForObject{}, predicate.Funcs{
		UpdateFunc: func(evt event.UpdateEvent) bool {
			old, ok := evt.ObjectOld.(*operv1.Network)
			if !ok {
				return true
			}
			new, ok := evt.ObjectNew.(*operv1.Network)
			if !ok {
				return true
			}
			if reflect.DeepEqual(old.Spec, new.Spec) {
				log.Printf("Skipping reconcile of Network.operator.openshift.io: spec unchanged")
				return false
			}
			return true
		},
	})
	if err != nil {
		return err
	}

	// watch for changes in all configmaps in our namespace
	// Currently, this would catch the mtu-prober reporting or the ovs flows config map.
	// Need to do this with a custom namespaced informer.
	cmInformer := v1coreinformers.NewConfigMapInformer(
		r.client.Default().Kubernetes(),
		names.APPLIED_NAMESPACE,
		0, // don't resync
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})

	r.client.Default().AddCustomInformer(cmInformer) // Tell the ClusterClient about this informer

	if err := c.Watch(&source.Informer{Informer: cmInformer},
		handler.EnqueueRequestsFromMapFunc(reconcileOperConfig),
		predicate.ResourceVersionChangedPredicate{},
		predicate.NewPredicateFuncs(func(object crclient.Object) bool {
			// Ignore ConfigMaps we manage as part of this loop
			return !(object.GetName() == "network-operator-lock" ||
				object.GetName() == "applied-cluster")
		}),
	); err != nil {
		return err
	}

	// Watch when nodes are created too
	newNodePredicate := predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool {
			return true
		},
		UpdateFunc: func(_ event.UpdateEvent) bool {
			return false
		},
	}
	if err := c.Watch(
		&source.Kind{Type: &corev1.Node{}},
		handler.EnqueueRequestsFromMapFunc(reconcileOperConfig),
		newNodePredicate,
	); err != nil {
		return err
	}

	// Likewise for the Pod reconciler
	podController, err := controller.New("pod-controller", mgr, controller.Options{Reconciler: r.podReconciler})
	if err != nil {
		return err
	}
	err = podController.Watch(&source.Kind{Type: &appsv1.DaemonSet{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}
	err = podController.Watch(&source.Kind{Type: &appsv1.Deployment{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}
	err = podController.Watch(&source.Kind{Type: &appsv1.StatefulSet{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	if hyperShiftConfig := network.NewHyperShiftConfig(); hyperShiftConfig.Enabled {
		// In HyperShift, only StatefulSets are deployed in the management cluster.
		// To add more resources the CNOs ClusterRole has to be updated in HyperShift
		mgmtCluster, err := cluster.New(r.client.ClientFor(cnoclient.ManagementClusterName).Config(), func(opts *cluster.Options) { opts.Namespace = hyperShiftConfig.Namespace })
		if err != nil {
			return err
		}
		err = podController.Watch(source.NewKindWithCache(&appsv1.StatefulSet{}, mgmtCluster.GetCache()), &handler.EnqueueRequestForObject{})
		if err != nil {
			return err
		}
		err = mgr.Add(mgmtCluster)
		if err != nil {
			return err
		}
	}

	return nil
}

var _ reconcile.Reconciler = &ReconcileOperConfig{}

// ReconcileOperConfig reconciles a Network.operator.openshift.io object
type ReconcileOperConfig struct {
	client        cnoclient.Client
	scheme        *runtime.Scheme
	status        *statusmanager.StatusManager
	mapper        meta.RESTMapper
	podReconciler *ReconcilePods

	// If we can skip cleaning up the MTU prober job.
	mtuProberCleanedUp bool
}

// Reconcile updates the state of the cluster to match that which is desired
// in the operator configuration (Network.operator.openshift.io)
func (r *ReconcileOperConfig) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log.Printf("Reconciling Network.operator.openshift.io %s\n", request.Name)

	// We won't create more than one network
	if request.Name != names.OPERATOR_CONFIG {
		log.Printf("Ignoring Network.operator.openshift.io without default name")
		return reconcile.Result{}, nil
	}

	// Fetch the Network.operator.openshift.io instance
	operConfig := &operv1.Network{TypeMeta: metav1.TypeMeta{APIVersion: operv1.GroupVersion.String(), Kind: "Network"}}
	err := r.client.Default().CRClient().Get(ctx, request.NamespacedName, operConfig)
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

	if operConfig.Spec.ManagementState == operv1.Unmanaged {
		log.Printf("Operator configuration state is %s - skipping operconfig reconciliation", operConfig.Spec.ManagementState)
		return reconcile.Result{}, nil
	}

	// Merge in the cluster configuration, in case the administrator has updated some "downstream" fields
	// This will also commit the change back to the apiserver.
	if err := r.MergeClusterConfig(ctx, operConfig); err != nil {
		log.Printf("Failed to merge the cluster configuration: %v", err)
		r.status.SetDegraded(statusmanager.OperatorConfig, "MergeClusterConfig",
			fmt.Sprintf("Internal error while merging cluster configuration and operator configuration: %v", err))
		return reconcile.Result{}, err
	}

	// Convert certain fields to canonicalized form for backward compatibility
	network.DeprecatedCanonicalize(&operConfig.Spec)

	// Validate the configuration
	if err := network.Validate(&operConfig.Spec); err != nil {
		log.Printf("Failed to validate Network.operator.openshift.io.Spec: %v", err)
		r.status.SetDegraded(statusmanager.OperatorConfig, "InvalidOperatorConfig",
			fmt.Sprintf("The operator configuration is invalid (%v). Use 'oc edit network.operator.openshift.io cluster' to fix.", err))
		return reconcile.Result{}, err
	}

	// Retrieve the previously applied operator configuration
	prev, err := GetAppliedConfiguration(ctx, r.client.Default().CRClient(), operConfig.ObjectMeta.Name)
	if err != nil {
		log.Printf("Failed to retrieve previously applied configuration: %v", err)
		// FIXME: operator status?
		return reconcile.Result{}, err
	}

	// Gather the Infra status, we'll need it a few places
	infraStatus, err := platform.InfraStatus(r.client)
	if err != nil {
		log.Printf("Failed to retrieve infrastructure status: %v", err)
		return reconcile.Result{}, err
	}

	// If we need to, probe the host's MTU via a Job.
	// It's okay if this is 0, since running clusters have no need of this
	// and thus do not need to probe MTU
	mtu := 0
	if network.NeedMTUProbe(prev, &operConfig.Spec) {
		mtu, err = r.probeMTU(ctx, operConfig, infraStatus)
		if err != nil {
			log.Printf("Failed to probe MTU: %v", err)
			r.status.SetDegraded(statusmanager.OperatorConfig, "MTUProbeFailed",
				fmt.Sprintf("Failed to probe MTU: %v", err))
			return reconcile.Result{}, fmt.Errorf("could not probe MTU -- maybe no available nodes: %w", err)
		}
		log.Printf("Using detected MTU %d", mtu)
	}

	// up-convert Prev by filling defaults
	if prev != nil {
		network.FillDefaults(prev, prev, mtu)
	}

	// Fill all defaults explicitly
	network.FillDefaults(&operConfig.Spec, prev, mtu)

	// Compare against previous applied configuration to see if this change
	// is safe.
	if prev != nil {
		// We may need to fill defaults here -- sort of as a poor-man's
		// upconversion scheme -- if we add additional fields to the config.
		err = network.IsChangeSafe(prev, &operConfig.Spec, infraStatus)
		if err != nil {
			log.Printf("Not applying unsafe change: %v", err)
			r.status.SetDegraded(statusmanager.OperatorConfig, "InvalidOperatorConfig",
				fmt.Sprintf("Not applying unsafe configuration change: %v. Use 'oc edit network.operator.openshift.io cluster' to undo the change.", err))
			return reconcile.Result{}, err
		}
	}

	newOperConfig := operConfig.DeepCopy()

	// Bootstrap any resources
	bootstrapResult, err := network.Bootstrap(newOperConfig, r.client)
	if err != nil {
		log.Printf("Failed to reconcile platform networking resources: %v", err)
		r.status.SetDegraded(statusmanager.OperatorConfig, "BootstrapError",
			fmt.Sprintf("Internal error while reconciling platform networking resources: %v", err))
		return reconcile.Result{}, err
	}

	if !reflect.DeepEqual(operConfig, newOperConfig) {
		if err := r.UpdateOperConfig(ctx, newOperConfig); err != nil {
			log.Printf("Failed to update the operator configuration: %v", err)
			r.status.SetDegraded(statusmanager.OperatorConfig, "UpdateOperatorConfig",
				fmt.Sprintf("Internal error while updating operator configuration: %v", err))
			return reconcile.Result{}, err
		}
	}

	// Generate the objects
	objs, progressing, err := network.Render(&operConfig.Spec, bootstrapResult, ManifestPath)
	if err != nil {
		log.Printf("Failed to render: %v", err)
		r.status.SetDegraded(statusmanager.OperatorConfig, "RenderError",
			fmt.Sprintf("Internal error while rendering operator configuration: %v", err))
		return reconcile.Result{}, err
	}

	if progressing {
		r.status.SetProgressing(statusmanager.OperatorRender, "RenderProgressing",
			"Waiting to render manifests")
	} else {
		r.status.UnsetProgressing(statusmanager.OperatorRender)
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

	// Set up the Pod reconciler before we start creating DaemonSets/Deployments/StatefulSets
	daemonSets := []statusmanager.ClusteredName{}
	deployments := []statusmanager.ClusteredName{}
	statefulSets := []statusmanager.ClusteredName{}
	relatedObjects := []configv1.ObjectReference{}
	relatedClusterObjects := []network.RelatedObject{}
	for _, obj := range objs {
		if obj.GetAPIVersion() == "apps/v1" && obj.GetKind() == "DaemonSet" {
			daemonSets = append(daemonSets, statusmanager.ClusteredName{ClusterName: apply.GetClusterName(obj), Namespace: obj.GetNamespace(), Name: obj.GetName()})
		} else if obj.GetAPIVersion() == "apps/v1" && obj.GetKind() == "Deployment" {
			deployments = append(deployments, statusmanager.ClusteredName{ClusterName: apply.GetClusterName(obj), Namespace: obj.GetNamespace(), Name: obj.GetName()})
		} else if obj.GetAPIVersion() == "apps/v1" && obj.GetKind() == "StatefulSet" {
			statefulSets = append(statefulSets, statusmanager.ClusteredName{ClusterName: apply.GetClusterName(obj), Namespace: obj.GetNamespace(), Name: obj.GetName()})
		}
		restMapping, err := r.mapper.RESTMapping(obj.GroupVersionKind().GroupKind())
		if err != nil {
			log.Printf("Failed to get REST mapping for storing related object: %v", err)
			continue
		}
		if apply.GetClusterName(obj) != "" {
			relatedClusterObjects = append(relatedClusterObjects, network.RelatedObject{
				ObjectReference: configv1.ObjectReference{
					Group:     obj.GetObjectKind().GroupVersionKind().Group,
					Resource:  restMapping.Resource.Resource,
					Name:      obj.GetName(),
					Namespace: obj.GetNamespace(),
				},
				ClusterName: apply.GetClusterName(obj),
			})
			// Don't add management cluster objects in relatedObjects
			continue
		}
		relatedObjects = append(relatedObjects, configv1.ObjectReference{
			Group:     obj.GetObjectKind().GroupVersionKind().Group,
			Resource:  restMapping.Resource.Resource,
			Name:      obj.GetName(),
			Namespace: obj.GetNamespace(),
		})
	}

	relatedObjects = append(relatedObjects, configv1.ObjectReference{
		Resource: "namespaces",
		Name:     names.APPLIED_NAMESPACE,
	})

	// Add operator.openshift.io/v1/network to relatedObjects for must-gather
	relatedObjects = append(relatedObjects, configv1.ObjectReference{
		Group:    "operator.openshift.io",
		Resource: "networks",
		Name:     "cluster",
	})

	// Add NetworkPolicy, EgressFirewall, EgressIP, CloudPrivateIPConfig for must-gather
	relatedObjects = append(relatedObjects, configv1.ObjectReference{
		Group:    "networking.k8s.io",
		Resource: "NetworkPolicy",
	})

	relatedObjects = append(relatedObjects, configv1.ObjectReference{
		Group:    "k8s.ovn.org",
		Resource: "EgressFirewall",
	})

	relatedObjects = append(relatedObjects, configv1.ObjectReference{
		Group:    "k8s.ovn.org",
		Resource: "EgressIP",
	})

	relatedObjects = append(relatedObjects, configv1.ObjectReference{
		Group:    "cloud.network.openshift.io",
		Resource: "CloudPrivateIPConfig",
	})

	// This Namespace is rendered by the CVO, but it's really our operand.
	relatedObjects = append(relatedObjects, configv1.ObjectReference{
		Resource: "namespaces",
		Name:     "openshift-cloud-network-config-controller",
	})

	r.status.SetDaemonSets(daemonSets)
	r.status.SetDeployments(deployments)
	r.status.SetStatefulSets(statefulSets)
	r.status.SetRelatedObjects(relatedObjects)
	r.status.SetRelatedClusterObjects(relatedClusterObjects)

	allResources := []statusmanager.ClusteredName{}
	allResources = append(allResources, daemonSets...)
	allResources = append(allResources, deployments...)
	allResources = append(allResources, statefulSets...)
	r.podReconciler.SetResources(allResources)

	// Apply the objects to the cluster
	for _, obj := range objs {
		// TODO: OwnerRef for non default clusters. For HyperShift this should probably be HostedControlPlane CR
		if apply.GetClusterName(obj) == "" {
			// Mark the object to be GC'd if the owner is deleted.
			if err := controllerutil.SetControllerReference(operConfig, obj, r.scheme); err != nil {
				err = errors.Wrapf(err, "could not set reference for (%s) %s/%s", obj.GroupVersionKind(), obj.GetNamespace(), obj.GetName())
				log.Println(err)
				r.status.SetDegraded(statusmanager.OperatorConfig, "InternalError",
					fmt.Sprintf("Internal error while updating operator configuration: %v", err))
				return reconcile.Result{}, err
			}
		}

		// Open question: should an error here indicate we will never retry?
		if err := apply.ApplyObject(ctx, r.client, obj, ControllerName); err != nil {
			err = errors.Wrapf(err, "could not apply (%s) %s/%s", obj.GroupVersionKind(), obj.GetNamespace(), obj.GetName())

			// If error comes from nonexistent namespace print out a help message.
			if obj.GroupVersionKind().Kind == "NetworkAttachmentDefinition" && strings.Contains(err.Error(), "namespaces") {
				err = errors.Wrapf(err, "could not apply (%s) %s/%s; Namespace error for networkattachment definition, consider possible solutions: (1) Edit config files to include existing namespace (2) Create non-existent namespace (3) Delete erroneous network-attachment-definition", obj.GroupVersionKind(), obj.GetNamespace(), obj.GetName())
			}

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
	status, err := r.ClusterNetworkStatus(ctx, operConfig)
	if err != nil {
		log.Printf("Could not generate network status: %v", err)
		r.status.SetDegraded(statusmanager.OperatorConfig, "StatusError",
			fmt.Sprintf("Could not update cluster configuration status: %v", err))
		return reconcile.Result{}, err
	}
	if status != nil {
		// Don't set the owner reference in this case -- we're updating
		// the status of our owner.
		if err := apply.ApplyObject(ctx, r.client, status, ControllerName); err != nil {
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
	log.Printf("Operconfig Controller complete")
	return reconcile.Result{RequeueAfter: ResyncPeriod}, nil
}

func reconcileOperConfig(obj crclient.Object) []reconcile.Request {
	log.Printf("%s %s/%s changed, triggering operconf reconciliation", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetNamespace(), obj.GetName())
	return []reconcile.Request{{NamespacedName: types.NamespacedName{
		Name:      names.OPERATOR_CONFIG,
		Namespace: names.APPLIED_NAMESPACE,
	}}}
}
