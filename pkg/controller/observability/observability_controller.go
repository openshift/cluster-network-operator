package observability

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	OperatorYAML         = "manifests/07-observability-operator.yaml"
	FlowCollectorYAML    = "manifests/08-flowcollector.yaml"
	NetObservNamespace   = "netobserv"
	OperatorNamespace    = "openshift-netobserv-operator"
	FlowCollectorVersion = "v1beta2"
	FlowCollectorName    = "cluster"

	checkInterval = 10 * time.Second
	checkTimeout  = 10 * time.Minute

	// NetworkObservabilityDeployed is the condition type that indicates Network Observability was successfully deployed
	NetworkObservabilityDeployed = "NetworkObservabilityDeployed"
)

// Add creates a new controller. Referenced in add_networkconfig.go.
func Add(mgr manager.Manager, status *statusmanager.StatusManager, _ cnoclient.Client, _ featuregates.FeatureGate) error {
	klog.Info("Add Network Observability Operator to manager")
	return add(mgr, newReconciler(mgr.GetClient(), status))
}

func newReconciler(client crclient.Client, status *statusmanager.StatusManager) *ReconcileObservability {
	return &ReconcileObservability{client: client, status: status}
}

func add(mgr manager.Manager, r *ReconcileObservability) error {
	c, err := controller.New("observability-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}
	return c.Watch(source.Kind(mgr.GetCache(), &configv1.Network{}, &handler.TypedEnqueueRequestForObject[*configv1.Network]{}))
}

var _ reconcile.Reconciler = &ReconcileObservability{}

// StatusReporter is an interface for reporting status
type StatusReporter interface {
	SetDegraded(level statusmanager.StatusLevel, reason, message string)
	SetNotDegraded(level statusmanager.StatusLevel)
}

type ReconcileObservability struct {
	client crclient.Client
	status StatusReporter
}

// Reconcile reacts to changes in Network CR
func (r *ReconcileObservability) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	klog.Info("Reconcile Network Observability")

	if req.Name != "cluster" {
		return ctrl.Result{}, nil // only reconcile the singleton Network object
	}

	// Get Network CR information
	var network configv1.Network
	if err := r.client.Get(ctx, types.NamespacedName{Name: "cluster"}, &network); err != nil {
		return ctrl.Result{}, crclient.IgnoreNotFound(err)
	}

	// Check if Network Observability should be enabled
	shouldInstall, err := r.shouldInstallNetworkObservability(ctx, &network)
	if err != nil {
		r.status.SetDegraded(statusmanager.ObservabilityConfig, "CheckInstallError", fmt.Sprintf("Failed to determine if Network Observability should be installed: %v", err))
		return ctrl.Result{}, err
	}
	if !shouldInstall {
		r.status.SetNotDegraded(statusmanager.ObservabilityConfig)
		return ctrl.Result{}, nil
	}

	// Check if Network Observability was previously deployed
	// If so, we're done - no need to check or manage anything
	if r.wasNetworkObservabilityDeployed(&network) {
		klog.Info("Network Observability was previously deployed, skipping reconciliation.")
		r.status.SetNotDegraded(statusmanager.ObservabilityConfig)
		return ctrl.Result{}, nil
	}

	// First time installation - proceed with operator installation
	installed, err := r.isNetObservOperatorInstalled(ctx)
	if err != nil {
		r.status.SetDegraded(statusmanager.ObservabilityConfig, "CheckOperatorError", fmt.Sprintf("Failed to check if Network Observability Operator is installed: %v", err))
		return ctrl.Result{}, err
	}
	if !installed {
		// Create namespace if it doesn't exist
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: OperatorNamespace}}
		if err := r.client.Get(ctx, types.NamespacedName{Name: OperatorNamespace}, ns); err != nil {
			if errors.IsNotFound(err) {
				if err := r.client.Create(ctx, ns); err != nil {
					r.status.SetDegraded(statusmanager.ObservabilityConfig, "CreateNamespaceError", fmt.Sprintf("Failed to create namespace %s: %v", OperatorNamespace, err))
					return ctrl.Result{}, err
				}
			} else {
				r.status.SetDegraded(statusmanager.ObservabilityConfig, "GetNamespaceError", fmt.Sprintf("Failed to get namespace %s: %v", OperatorNamespace, err))
				return ctrl.Result{}, err
			}
		}

		// Install Network Observability Operator
		if err := r.installNetObservOperator(ctx); err != nil {
			r.status.SetDegraded(statusmanager.ObservabilityConfig, "InstallOperatorError", fmt.Sprintf("Failed to install Network Observability Operator: %v", err))
			return ctrl.Result{}, err
		}
	}

	// Wait for Network Observability Operator to be ready (whether just installed or already present)
	klog.Info("Wait for Network Observability to be ready")
	if err := r.waitForNetObservOperator(ctx); err != nil {
		if err == context.DeadlineExceeded {
			klog.Errorf("Timed out waiting for Network Observability Operator to be ready after %v. Stopping reconciliation.", checkTimeout)
			r.status.SetDegraded(statusmanager.ObservabilityConfig, "OperatorNotReady", fmt.Sprintf("Timed out waiting for Network Observability Operator to be ready after %v", checkTimeout))
			return ctrl.Result{RequeueAfter: 0}, nil // Don't requeue
		}
		r.status.SetDegraded(statusmanager.ObservabilityConfig, "WaitOperatorError", fmt.Sprintf("Failed waiting for Network Observability Operator: %v", err))
		return ctrl.Result{}, err
	}

	// Check if FlowCollector already exists
	flowCollectorExists, err := r.isFlowCollectorExists(ctx)
	if err != nil {
		r.status.SetDegraded(statusmanager.ObservabilityConfig, "CheckFlowCollectorError", fmt.Sprintf("Failed to check if FlowCollector exists: %v", err))
		return ctrl.Result{}, err
	}

	if !flowCollectorExists {
		// Create FlowCollector (first time deployment)
		if err := r.createFlowCollector(ctx); err != nil {
			r.status.SetDegraded(statusmanager.ObservabilityConfig, "CreateFlowCollectorError", fmt.Sprintf("Failed to create FlowCollector: %v", err))
			return ctrl.Result{}, err
		}
	}

	// Mark as deployed in Network CR status
	if err := r.markNetworkObservabilityDeployed(ctx, &network); err != nil {
		klog.Warningf("Failed to update Network Observability deployment status: %v", err)
	}

	r.status.SetNotDegraded(statusmanager.ObservabilityConfig)
	return ctrl.Result{}, nil
}

// shouldInstallNetworkObservability returns true if Network Observability should be installed.
// Valid values: "", "Enable", "Disable"
// "Disable": skip installation (user opted out)
// "Enable": install Network Observability (even on SNO clusters)
// "" or nil: install Network Observability (opt-out model), except for SNO clusters
// SNO (Single Node OpenShift) clusters: skip installation by default unless explicitly set to "Enable"
func (r *ReconcileObservability) shouldInstallNetworkObservability(ctx context.Context, network *configv1.Network) (bool, error) {
	// Check explicit value
	if network.Spec.InstallNetworkObservability != nil {
		value := *network.Spec.InstallNetworkObservability

		// Explicit disable
		if value == "Disable" {
			return false, nil
		}

		// Explicit enable - install regardless of topology
		if value == "Enable" {
			return true, nil
		}

		// Empty string falls through to default behavior
	}

	// Default behavior (nil or ""): check if this is a SNO cluster
	isSNO, err := r.isSingleNodeCluster(ctx)
	if err != nil {
		return false, err
	}

	if isSNO {
		// SNO clusters: don't install by default
		return false, nil
	}

	// Non-SNO clusters: install by default (opt-out model)
	return true, nil
}

// isSingleNodeCluster returns true if the cluster is a Single Node OpenShift (SNO) cluster.
// A cluster is SNO if ControlPlaneTopology is SingleReplica.
func (r *ReconcileObservability) isSingleNodeCluster(ctx context.Context) (bool, error) {
	infra := &configv1.Infrastructure{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: "cluster"}, infra); err != nil {
		return false, err
	}

	return infra.Status.ControlPlaneTopology == configv1.SingleReplicaTopologyMode, nil
}

// isNetObservOperatorInstalled returns true if the netobserv-operator Subscription exists
func (r *ReconcileObservability) isNetObservOperatorInstalled(ctx context.Context) (bool, error) {
	subscription := &unstructured.Unstructured{}
	subscription.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "operators.coreos.com",
		Version: "v1alpha1",
		Kind:    "Subscription",
	})

	err := r.client.Get(ctx, types.NamespacedName{
		Name:      "netobserv-operator",
		Namespace: OperatorNamespace,
	}, subscription)

	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

// applyManifest reads a YAML file and applies all resources using server-side apply
func (r *ReconcileObservability) applyManifest(ctx context.Context, yamlPath, description string) error {
	yamlBytes, err := os.ReadFile(yamlPath)
	if err != nil {
		return fmt.Errorf("failed to read %s manifest %s: %w", description, yamlPath, err)
	}

	dec := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(yamlBytes), 4096)
	for {
		obj := &unstructured.Unstructured{}
		if err := dec.Decode(obj); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		if obj.GetKind() == "" {
			continue
		}
		obj.SetManagedFields(nil)
		if err := r.client.Patch(ctx, obj, crclient.Apply, &crclient.PatchOptions{
			Force:        ptr.To(true),
			FieldManager: "cno-observability-controller",
		}); err != nil {
			return fmt.Errorf("failed to apply %s %s: %w", obj.GetKind(), obj.GetName(), err)
		}
		klog.Infof("Applied %s %s", description, obj.GetName())
	}
	klog.Infof("Successfully applied %s", description)
	return nil
}

func (r *ReconcileObservability) installNetObservOperator(ctx context.Context) error {
	return r.applyManifest(ctx, OperatorYAML, "Network Observability Operator")
}

func (r *ReconcileObservability) waitForNetObservOperator(ctx context.Context) error {
	condition := func(ctx context.Context) (bool, error) {
		// List ClusterServiceVersions in the operator namespace
		csvs := &unstructured.UnstructuredList{}
		csvs.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "operators.coreos.com",
			Version: "v1alpha1",
			Kind:    "ClusterServiceVersion",
		})

		if err := r.client.List(ctx, csvs, crclient.InNamespace(OperatorNamespace)); err != nil {
			return false, err
		}

		// Find the netobserv operator CSV
		for _, csv := range csvs.Items {
			name := csv.GetName()
			// CSV names are typically like "netobserv-operator.v1.2.3"
			if strings.HasPrefix(name, "netobserv-operator") {
				phase, found, err := unstructured.NestedString(csv.Object, "status", "phase")
				if err != nil {
					return false, err
				}
				if !found {
					return false, nil
				}
				return phase == "Succeeded", nil
			}
		}
		return false, nil
	}
	return wait.PollUntilContextTimeout(ctx, checkInterval, checkTimeout, true, condition)
}

// isFlowCollectorExists returns true if a FlowCollector instance exists.
// Note: FlowCollector is a cluster-scoped singleton resource and can only be named "cluster".
func (r *ReconcileObservability) isFlowCollectorExists(ctx context.Context) (bool, error) {
	flowCollector := &unstructured.Unstructured{}
	flowCollector.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "flows.netobserv.io",
		Version: FlowCollectorVersion,
		Kind:    "FlowCollector",
	})

	err := r.client.Get(ctx, types.NamespacedName{Name: FlowCollectorName}, flowCollector)
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

// wasNetworkObservabilityDeployed checks if Network Observability was previously deployed
// by looking at the Network CR status conditions.
func (r *ReconcileObservability) wasNetworkObservabilityDeployed(network *configv1.Network) bool {
	for _, condition := range network.Status.Conditions {
		if condition.Type == NetworkObservabilityDeployed && condition.Status == metav1.ConditionTrue {
			return true
		}
	}
	return false
}

// markNetworkObservabilityDeployed updates the Network CR status to indicate
// that Network Observability was successfully deployed.
func (r *ReconcileObservability) markNetworkObservabilityDeployed(ctx context.Context, network *configv1.Network) error {
	// Check if condition already exists and is true
	for _, condition := range network.Status.Conditions {
		if condition.Type == NetworkObservabilityDeployed && condition.Status == metav1.ConditionTrue {
			return nil // Already marked as deployed
		}
	}

	// Get the latest version of the Network CR to avoid conflicts
	latest := &configv1.Network{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: "cluster"}, latest); err != nil {
		return err
	}

	// Remove any existing NetworkObservabilityDeployed condition
	newConditions := []metav1.Condition{}
	for _, condition := range latest.Status.Conditions {
		if condition.Type != NetworkObservabilityDeployed {
			newConditions = append(newConditions, condition)
		}
	}

	// Add the new condition
	now := metav1.Now()
	newConditions = append(newConditions, metav1.Condition{
		Type:               NetworkObservabilityDeployed,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: now,
		Reason:             "DeploymentComplete",
		Message:            "Network Observability FlowCollector was successfully deployed",
	})

	latest.Status.Conditions = newConditions

	// Update the status
	return r.client.Status().Update(ctx, latest)
}

func (r *ReconcileObservability) createFlowCollector(ctx context.Context) error {
	// Ensure the netobserv namespace exists before applying manifests.
	ns := &corev1.Namespace{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: NetObservNamespace}, ns); err != nil {
		if errors.IsNotFound(err) {
			if err := r.client.Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: NetObservNamespace},
			}); err != nil {
				return fmt.Errorf("failed to create namespace %s: %w", NetObservNamespace, err)
			}
			klog.Infof("Created namespace %s", NetObservNamespace)
		} else {
			return err
		}
	}

	return r.applyManifest(ctx, FlowCollectorYAML, "FlowCollector")
}
