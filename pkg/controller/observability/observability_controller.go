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
	operatorv1 "github.com/openshift/api/operator/v1"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	operatorv1helpers "github.com/openshift/library-go/pkg/operator/v1helpers"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/klog/v2"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	OperatorYAML         = "bindata/observability/07-observability-operator.yaml"
	FlowCollectorYAML    = "bindata/observability/08-flowcollector.yaml"
	OperatorNamespace    = "netobserv-operator"
	FlowCollectorVersion = "v1beta2"
	FlowCollectorName    = "cluster"
	NetworkCRName        = "cluster"

	NetworkObservabilityDeployed = "NetworkObservabilityDeployed"

	// Requeue interval for install/wait and standard operations
	requeueInterval = 30 * time.Second
	// Requeue interval while waiting for the cluster to become available
	clusterAvailableRequeueInterval = 5 * time.Minute
	// Deadline for the OLM resource rollback API calls
	teardownTimeout = 30 * time.Second

	// Max time to wait for the cluster to become available. On day 0, it may
	// take some time to come up.
	clusterAvailableTimeout = 90 * time.Minute
	// Max time to install Network Observability and create FlowCollector
	// once cluster is available
	installTimeout = 20 * time.Minute

	// Mark the resources that CNO created. Teardown only deletes the
	// operator namespace when this marker is present.
	createdByCNOAnnotation = "network.operator.openshift.io/created-by-cno"
)

// Add creates a new controller. Referenced in add_networkconfig.go.
func Add(mgr manager.Manager, _ *statusmanager.StatusManager, _ cnoclient.Client, featureGate featuregates.FeatureGate) error {
	klog.Info("Add Network Observability Operator to manager")
	return add(mgr, newReconciler(mgr.GetClient(), featureGate))
}

func newReconciler(client crclient.Client, featureGate featuregates.FeatureGate) *ReconcileObservability {
	return &ReconcileObservability{
		client:      client,
		featureGate: featureGate,
	}
}

func add(mgr manager.Manager, r *ReconcileObservability) error {
	c, err := controller.New("observability-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch config.openshift.io/v1 Network CR for spec changes
	if err := c.Watch(source.Kind(mgr.GetCache(), &configv1.Network{}, &handler.TypedEnqueueRequestForObject[*configv1.Network]{})); err != nil {
		return err
	}

	// Watch operator.openshift.io/v1 Network CR for status changes
	// This ensures that status condition changes trigger reconciliation
	return c.Watch(source.Kind(mgr.GetCache(), &operatorv1.Network{}, &handler.TypedEnqueueRequestForObject[*operatorv1.Network]{}))
}

var _ reconcile.Reconciler = &ReconcileObservability{}

type ReconcileObservability struct {
	client      crclient.Client
	featureGate featuregates.FeatureGate
}

// Reconcile reacts to changes in Network CR
func (r *ReconcileObservability) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	klog.Info("Reconcile Network Observability")

	if req.Name != NetworkCRName {
		return reconcile.Result{}, nil // only reconcile the singleton Network object
	}

	// Check if NetworkObservabilityInstall feature gate is enabled
	if !r.isFeatureGateEnabled() {
		klog.V(4).Info("NetworkObservabilityInstall feature gate is disabled, skipping Network Observability management")
		return reconcile.Result{}, nil
	}

	// Check if Network Observability should be enabled
	shouldInstall, err := r.shouldInstallNetworkObservability(ctx)
	if err != nil {
		klog.Warningf("Failed to determine if Network Observability should be installed: %v. Will retry in %v.", err, requeueInterval)
		return reconcile.Result{RequeueAfter: requeueInterval}, nil
	}
	if !shouldInstall {
		return reconcile.Result{}, nil
	}

	// Check if installation has been failing for too long
	if r.hasInstallTimedOut(ctx) {
		if err := r.handleInstallTimeout(ctx); err != nil {
			klog.Warningf("Failed to handle Network Observability install timeout: %v. Will retry in %v.", err, requeueInterval)
			return reconcile.Result{RequeueAfter: requeueInterval}, nil
		}
		return reconcile.Result{}, nil
	}

	// Check if Network Observability Operator is installed
	installed, err := r.isNetObservOperatorInstalled(ctx)
	if err != nil {
		klog.Warningf("Failed to check if Network Observability Operator is installed: %v. Will retry in %v.", err, requeueInterval)
		_ = r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, "DeploymentFailed", fmt.Sprintf("Failed to check Network Observability Operator status: %v", err))
		return reconcile.Result{RequeueAfter: requeueInterval}, nil
	}
	if !installed {
		// Don't apply the Subscription until the cluster is available.
		if available, _ := r.clusterVersionAvailableSince(ctx); !available {
			klog.Infof("Cluster not available yet, will recheck in %v", clusterAvailableRequeueInterval)
			_ = r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, "WaitingForCluster", "Waiting for the cluster to become available")
			return reconcile.Result{RequeueAfter: clusterAvailableRequeueInterval}, nil
		}

		if err := r.installNetObservOperator(ctx); err != nil {
			klog.Warningf("Failed to install Network Observability Operator: %v. Will retry in %v.", err, requeueInterval)
			_ = r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, "DeploymentFailed", fmt.Sprintf("Failed to install Network Observability Operator: %v", err))
			return reconcile.Result{RequeueAfter: requeueInterval}, nil
		}
		klog.Infof("Applied Subscription for netobserv-operator, will check installation status in %v", requeueInterval)
		_ = r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, "InstallationInProgress", "Network Observability Operator installation in progress")
		return reconcile.Result{RequeueAfter: requeueInterval}, nil
	}

	// Operator installation completed
	klog.Info("Network Observability Operator installation completed, proceeding to FlowCollector creation")

	// Check if FlowCollector already exists
	flowCollectorExists, err := r.isFlowCollectorExists(ctx)
	if err != nil {
		klog.Warningf("Failed to check if FlowCollector exists: %v. Will retry in %v.", err, requeueInterval)
		return reconcile.Result{RequeueAfter: requeueInterval}, nil
	}

	if !flowCollectorExists {
		// Create FlowCollector
		if err := r.applyManifest(ctx, FlowCollectorYAML, "FlowCollector"); err != nil {
			klog.Warningf("Failed to create FlowCollector: %v. Will retry in %v.", err, requeueInterval)
			// Mark deployment as failed
			_ = r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, "DeploymentFailed", fmt.Sprintf("Failed to create FlowCollector: %v", err))
			return reconcile.Result{RequeueAfter: requeueInterval}, nil
		}
		klog.Info("FlowCollector created successfully")
	}

	// Mark as deployed to track deployment status
	if err := r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionTrue, "DeploymentComplete", "Network Observability has been deployed"); err != nil {
		klog.Warningf("Failed to mark Network Observability as deployed: %v. Will retry in %v.", err, requeueInterval)
		return reconcile.Result{RequeueAfter: requeueInterval}, nil
	}

	klog.V(4).Info("Network Observability is deployed")
	return reconcile.Result{}, nil
}

// isFeatureGateEnabled checks if the NetworkObservabilityInstall feature gate is enabled.
// If featureGate is nil (e.g., in tests), returns false to default to disabled.
// If the feature gate is not registered yet (older cluster versions), returns false.
func (r *ReconcileObservability) isFeatureGateEnabled() bool {
	if r.featureGate == nil {
		return false // Default to disabled in tests
	}

	featureGateName := configv1.FeatureGateName("NetworkObservabilityInstall")

	return r.featureGate.Enabled(featureGateName)
}

// wasNetworkObservabilityDeployed checks if the NetworkObservabilityDeployed condition
// is set to True in the network.operator.openshift.io Network CR status
func (r *ReconcileObservability) wasNetworkObservabilityDeployed(ctx context.Context) (bool, error) {
	network := &operatorv1.Network{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: NetworkCRName}, network); err != nil {
		return false, err
	}

	for _, condition := range network.Status.Conditions {
		if condition.Type == NetworkObservabilityDeployed {
			return condition.Status == operatorv1.ConditionTrue, nil
		}
	}

	return false, nil
}

// hasInstallTimedOut returns true when the installation should be abandoned. On
// day 0, the install clock only starts once the cluster is available. A backstop
// from our own condition timestamp bounds the wait even if that read fails.
func (r *ReconcileObservability) hasInstallTimedOut(ctx context.Context) bool {
	network := &operatorv1.Network{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: NetworkCRName}, network); err != nil {
		return false
	}

	condition := operatorv1helpers.FindOperatorCondition(network.Status.Conditions, NetworkObservabilityDeployed)
	if condition == nil || condition.Status != operatorv1.ConditionFalse {
		return false
	}
	if condition.Reason == "DeploymentTimedOut" {
		return true
	}

	// Absolute backstop, independent of any external read.
	if time.Since(condition.LastTransitionTime.Time) > clusterAvailableTimeout {
		return true
	}

	// Until the cluster is Available, keep waiting.
	available, availableSince := r.clusterVersionAvailableSince(ctx)
	if !available {
		return false
	}

	// Start the install clock at the later of install attempt and cluster
	// availability, so an already-up cluster does not consume the install budget.
	installStart := condition.LastTransitionTime.Time
	if availableSince.After(installStart) {
		installStart = availableSince
	}
	return time.Since(installStart) > installTimeout
}

// clusterVersionAvailableSince reports whether ClusterVersion is Available and,
// if so, when it became Available. A read error is treated as unknown, not down.
func (r *ReconcileObservability) clusterVersionAvailableSince(ctx context.Context) (bool, time.Time) {
	cv := &configv1.ClusterVersion{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: "version"}, cv); err != nil {
		klog.Warningf("Failed to read ClusterVersion, treating cluster availability as unknown: %v", err)
		return false, time.Time{}
	}
	for _, c := range cv.Status.Conditions {
		if c.Type == configv1.OperatorAvailable {
			return c.Status == configv1.ConditionTrue, c.LastTransitionTime.Time
		}
	}
	return false, time.Time{}
}

// handleInstallTimeout gives up on the installation. It rolls back the OLM
// resources CNO created and only then marks the installation as terminally timed
// out. If rollback fails, it returns an error so the caller requeues and retries.
func (r *ReconcileObservability) handleInstallTimeout(ctx context.Context) error {
	// DeploymentTimedOut is only set after rollback succeeds, so its presence means
	// there is nothing left to do.
	network := &operatorv1.Network{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: NetworkCRName}, network); err == nil {
		if condition := operatorv1helpers.FindOperatorCondition(network.Status.Conditions, NetworkObservabilityDeployed); condition != nil &&
			condition.Reason == "DeploymentTimedOut" {
			return nil
		}
	}

	klog.Warning("Network Observability installation timed out, giving up")

	// Roll back before latching the terminal condition, so a failed teardown is
	// retried on the next reconcile.
	if err := r.teardownNetObservOperator(ctx); err != nil {
		_ = r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, "RollbackPending",
			fmt.Sprintf("Network Observability install timed out, rolling back: %v", err))
		return fmt.Errorf("failed to roll back Network Observability OLM resources: %w", err)
	}
	klog.Info("Rolled back Network Observability OLM resources")

	message := fmt.Sprintf("Network Observability Operator failed to install within %v", installTimeout)
	return r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, "DeploymentTimedOut", message)
}

// teardownNetObservOperator deletes the OLM resources CNO created for Network
// Observability, which are the Subscription, OperatorGroup, any
// ClusterServiceVersion, and the operator namespace.
func (r *ReconcileObservability) teardownNetObservOperator(ctx context.Context) error {
	// Bound the teardown so a stalled API request cannot block the
	// reconciliation worker indefinitely.
	ctx, cancel := context.WithTimeout(ctx, teardownTimeout)
	defer cancel()

	subscription := &unstructured.Unstructured{}
	subscription.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "operators.coreos.com",
		Version: "v1alpha1",
		Kind:    "Subscription",
	})
	subscription.SetName(OperatorNamespace)
	subscription.SetNamespace(OperatorNamespace)
	if err := r.client.Delete(ctx, subscription); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete Subscription: %w", err)
	}

	operatorGroup := &unstructured.Unstructured{}
	operatorGroup.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "operators.coreos.com",
		Version: "v1",
		Kind:    "OperatorGroup",
	})
	operatorGroup.SetName(OperatorNamespace)
	operatorGroup.SetNamespace(OperatorNamespace)
	if err := r.client.Delete(ctx, operatorGroup); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete OperatorGroup: %w", err)
	}

	csvList := &unstructured.UnstructuredList{}
	csvList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "operators.coreos.com",
		Version: "v1alpha1",
		Kind:    "ClusterServiceVersion",
	})
	if err := r.client.List(ctx, csvList, crclient.InNamespace(OperatorNamespace)); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to list ClusterServiceVersions: %w", err)
	}
	for i := range csvList.Items {
		csv := &csvList.Items[i]
		if !strings.HasPrefix(csv.GetName(), "network-observability-operator") {
			continue
		}
		if err := r.client.Delete(ctx, csv); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("failed to delete ClusterServiceVersion %s: %w", csv.GetName(), err)
		}
	}

	// Only delete the namespace when CNO created it. Deleting a pre-existing
	// namespace would cascade-delete independently created resources.
	namespace := &corev1.Namespace{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: OperatorNamespace}, namespace); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get namespace: %w", err)
	}
	if namespace.GetAnnotations()[createdByCNOAnnotation] != "true" {
		klog.Infof("Namespace %s was not created by CNO; leaving it in place", OperatorNamespace)
		return nil
	}
	if err := r.client.Delete(ctx, namespace); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete namespace: %w", err)
	}

	return nil
}

// setNetworkObservabilityCondition sets the NetworkObservabilityDeployed condition
// in the network.operator.openshift.io Network CR status
func (r *ReconcileObservability) setNetworkObservabilityCondition(ctx context.Context, status operatorv1.ConditionStatus, reason, message string) error {
	// Get the operator Network CR
	network := &operatorv1.Network{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: NetworkCRName}, network); err != nil {
		return fmt.Errorf("failed to get operator Network CR: %w", err)
	}

	if condition := operatorv1helpers.FindOperatorCondition(network.Status.Conditions, NetworkObservabilityDeployed); condition != nil &&
		condition.Status == status &&
		condition.Reason == reason {
		// Already set with same status and reason, no need to update
		klog.V(4).Infof("Network Observability condition already set to %s with reason %s", status, reason)
		return nil
	}

	// Create the condition to add/update
	operatorv1helpers.SetOperatorCondition(&network.Status.Conditions, operatorv1.OperatorCondition{
		Type:    NetworkObservabilityDeployed,
		Status:  status,
		Reason:  reason,
		Message: message,
	})

	// Update the status using controller-runtime client
	if err := r.client.Status().Update(ctx, network); err != nil {
		return fmt.Errorf("failed to update operator Network status: %w", err)
	}

	klog.Infof("Set Network Observability condition to %s: %s", status, reason)
	return nil
}

// shouldInstallNetworkObservability returns true if Network Observability should be installed.
// Valid values of network.Spec.NetworkObservability.InstallationPolicy: "", "InstallAndEnable", "NoAction"
// "NoAction": skip installation (user opted out)
// "InstallAndEnable": install Network Observability once (even on SNO clusters), do not reinstall if already deployed
// "": install Network Observability once (opt-out model), except for SNO clusters, do not reinstall if already deployed
// SNO (Single Node OpenShift) clusters: skip installation by default unless explicitly set to "InstallAndEnable"
func (r *ReconcileObservability) shouldInstallNetworkObservability(ctx context.Context) (bool, error) {
	// Get Network CR information
	var network configv1.Network
	if err := r.client.Get(ctx, types.NamespacedName{Name: NetworkCRName}, &network); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	value := network.Spec.NetworkObservability.InstallationPolicy

	// Explicit disable
	if value == configv1.NetworkObservabilityNoAction {
		return false, nil
	}

	// For both InstallAndEnable and default (empty): install once, do not reinstall
	// Check if already deployed
	deployed, err := r.wasNetworkObservabilityDeployed(ctx)
	if err != nil {
		return false, err
	}
	if deployed {
		// Already deployed, do not reinstall
		policyName := "default"
		if value == configv1.NetworkObservabilityInstallAndEnable {
			policyName = "InstallAndEnable"
		}
		klog.V(4).Infof("Network Observability already deployed (%s policy), skipping reinstallation", policyName)
		return false, nil
	}

	// Not yet deployed - determine if we should install based on policy and topology
	// InstallAndEnable: install regardless of topology
	if value == configv1.NetworkObservabilityInstallAndEnable {
		return true, nil
	}

	// Default behavior (empty string): install unless on SNO
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

// isNetObservOperatorInstalled returns true once the Network Observability
// Operator is installed, detected by the presence of the FlowCollector CRD.
func (r *ReconcileObservability) isNetObservOperatorInstalled(ctx context.Context) (bool, error) {
	crd := &unstructured.Unstructured{}
	crd.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "apiextensions.k8s.io",
		Version: "v1",
		Kind:    "CustomResourceDefinition",
	})

	if err := r.client.Get(ctx, types.NamespacedName{Name: "flowcollectors.flows.netobserv.io"}, crd); err != nil {
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
		r.markNamespaceIfCNOOwned(ctx, obj)

		// Marshal object to JSON for RawPatch
		data, err := obj.MarshalJSON()
		if err != nil {
			return fmt.Errorf("failed to marshal %s %s: %w", obj.GetKind(), obj.GetName(), err)
		}

		// Use RawPatch with ApplyPatchType to avoid deprecated crclient.Apply
		patch := crclient.RawPatch(types.ApplyPatchType, data)
		if err := r.client.Patch(ctx, obj, patch, &crclient.PatchOptions{
			FieldManager: "cno-observability-controller",
		}); err != nil {
			return fmt.Errorf("failed to apply %s %s: %w", obj.GetKind(), obj.GetName(), err)
		}
		klog.V(4).Infof("Applied %s %s", description, obj.GetName())
	}
	klog.V(4).Infof("Successfully applied %s", description)
	return nil
}

func (r *ReconcileObservability) installNetObservOperator(ctx context.Context) error {
	return r.applyManifest(ctx, OperatorYAML, "Network Observability Operator")
}

// markNamespaceIfCNOOwned adds the CNO ownership marker to a Namespace object
// before it is applied, but only when CNO is creating the namespace (it does not
// yet exist) or already owns it (the marker is already present). A namespace that
// pre-existed the installation without the marker is left unmarked so teardown
// will not delete it and cascade-delete unrelated resources.
func (r *ReconcileObservability) markNamespaceIfCNOOwned(ctx context.Context, obj *unstructured.Unstructured) {
	if obj.GetKind() != "Namespace" {
		return
	}

	existing := &corev1.Namespace{}
	err := r.client.Get(ctx, types.NamespacedName{Name: obj.GetName()}, existing)
	switch {
	case errors.IsNotFound(err):
		// CNO is creating the namespace. Mark it as CNO-created.
	case err != nil:
		// Ownership cannot be determined so do not claim it.
		klog.Warningf("Failed to check namespace %s ownership, not marking it as CNO-created: %v", obj.GetName(), err)
		return
	case existing.GetAnnotations()[createdByCNOAnnotation] != "true":
		// Namespace pre-existed the installation and CNO does not own it. Leave it
		// unmarked so teardown will not delete it.
		return
	}

	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[createdByCNOAnnotation] = "true"
	obj.SetAnnotations(annotations)
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
