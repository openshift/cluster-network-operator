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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	NetObservNamespace   = "netobserv"
	OperatorNamespace    = "netobserv-operator"
	FlowCollectorVersion = "v1beta2"
	FlowCollectorName    = "cluster"
	NetworkCRName        = "cluster"

	NetworkObservabilityDeployed = "NetworkObservabilityDeployed"

	checkInterval        = 10 * time.Second
	checkTimeout         = 10 * time.Minute
	requeueAfterOLM      = 5 * time.Minute  // Requeue interval for OLM operations (install/wait)
	requeueAfterStandard = 30 * time.Second // Requeue interval for standard operations
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
		klog.Warningf("Failed to determine if Network Observability should be installed: %v. Will retry in %v.", err, requeueAfterStandard)
		return reconcile.Result{RequeueAfter: requeueAfterStandard}, nil
	}
	if !shouldInstall {
		return reconcile.Result{}, nil
	}

	// Proceed with installation/reinstallation
	installed, ceExists, err := r.isNetObservOperatorInstalled(ctx)
	if err != nil {
		klog.Warningf("Failed to check if Network Observability Operator is installed: %v. Will retry in %v.", err, requeueAfterStandard)
		// Mark deployment as failed with the error details
		_ = r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, "DeploymentFailed", fmt.Sprintf("Failed to check Network Observability Operator status: %v", err))
		return reconcile.Result{RequeueAfter: requeueAfterStandard}, nil
	}
	if !installed {
		if !ceExists {
			// ClusterExtension doesn't exist yet, create it
			if err := r.installNetObservOperator(ctx); err != nil {
				klog.Warningf("Failed to install Network Observability Operator: %v. Will retry in %v.", err, requeueAfterOLM)
				_ = r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, "DeploymentFailed", fmt.Sprintf("Failed to install Network Observability Operator: %v", err))
				return reconcile.Result{RequeueAfter: requeueAfterOLM}, nil
			}
			klog.Infof("Created ClusterExtension netobserv-operator, will check installation status in %v", requeueAfterOLM)
			return reconcile.Result{RequeueAfter: requeueAfterOLM}, nil
		}

		// ClusterExtension exists but installation not complete yet
		klog.Infof("ClusterExtension netobserv-operator installation in progress, will recheck in %v", requeueAfterOLM)
		return reconcile.Result{RequeueAfter: requeueAfterOLM}, nil
	}

	// Operator installation completed
	klog.Info("ClusterExtension netobserv-operator installation completed, proceeding to FlowCollector creation")

	// Check if FlowCollector already exists
	flowCollectorExists, err := r.isFlowCollectorExists(ctx)
	if err != nil {
		klog.Warningf("Failed to check if FlowCollector exists: %v. Will retry in %v.", err, requeueAfterStandard)
		return reconcile.Result{RequeueAfter: requeueAfterStandard}, nil
	}

	if !flowCollectorExists {
		// Create FlowCollector
		if err := r.createFlowCollector(ctx); err != nil {
			klog.Warningf("Failed to create FlowCollector: %v. Will retry in %v.", err, requeueAfterStandard)
			// Mark deployment as failed
			_ = r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, "DeploymentFailed", fmt.Sprintf("Failed to create FlowCollector: %v", err))
			return reconcile.Result{RequeueAfter: requeueAfterStandard}, nil
		}
		klog.Info("FlowCollector created successfully")
	}

	// Mark as deployed to track deployment status
	if err := r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionTrue, "DeploymentComplete", "Network Observability has been deployed"); err != nil {
		klog.Warningf("Failed to mark Network Observability as deployed: %v. Will retry in %v.", err, requeueAfterStandard)
		return reconcile.Result{RequeueAfter: requeueAfterStandard}, nil
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

// isNetObservOperatorInstalled checks if the Network Observability Operator is installed
// by verifying both the FlowCollector CRD existence and the installation status via OLM.
// It checks both OLMv1 (ClusterExtension) and OLMv0 (ClusterServiceVersion) to determine
// installation status.
// Returns three values:
// - installed: true if the operator is fully installed
// - clusterExtensionExists: true if a ClusterExtension resource exists (relevant for OLMv1)
// - err: error if there was a problem checking the installation
func (r *ReconcileObservability) isNetObservOperatorInstalled(ctx context.Context) (installed bool, clusterExtensionExists bool, err error) {
	// Check if the FlowCollector CRD exists
	crd := &unstructured.Unstructured{}
	crd.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "apiextensions.k8s.io",
		Version: "v1",
		Kind:    "CustomResourceDefinition",
	})

	err = r.client.Get(ctx, types.NamespacedName{
		Name: "flowcollectors.flows.netobserv.io",
	}, crd)

	crdExists := true
	if err != nil {
		if errors.IsNotFound(err) {
			crdExists = false
		} else {
			return false, false, err
		}
	}

	// Check OLMv1 (ClusterExtension) installation status
	olmv1Installed, olmv1CEExists, olmv1Err := r.checkOLMv1Installation(ctx)
	if olmv1Err != nil {
		// Installation error from OLMv1
		return false, olmv1CEExists, fmt.Errorf("OLMv1 installation error: %w", olmv1Err)
	}

	// Check OLMv0 (ClusterServiceVersion/Subscription) installation status
	olmv0Installed, olmv0Err := r.checkOLMv0Installation(ctx)
	if olmv0Err != nil && !errors.IsNotFound(olmv0Err) {
		// Installation error from OLMv0
		return false, olmv1CEExists, fmt.Errorf("OLMv0 installation error: %w", olmv0Err)
	}

	// If CRD doesn't exist but either OLM installation is present, this is an error condition
	if !crdExists {
		if olmv0Installed || olmv1Installed {
			olmVersion := "OLMv0"
			if olmv1Installed {
				olmVersion = "OLMv1"
			}
			return false, olmv1CEExists, fmt.Errorf("network Observability Operator was deployed via %s but FlowCollector CRD is missing (manually removed)", olmVersion)
		}
		// If CRD doesn't exist and no OLM installation, operator is not installed
		return false, olmv1CEExists, nil
	}

	if olmv1Installed {
		klog.V(4).Info("Network Observability Operator installed via OLMv1 (ClusterExtension)")
		return true, true, nil
	}

	if olmv0Installed {
		klog.V(4).Info("Network Observability Operator installed via OLMv0 (ClusterServiceVersion)")
		return true, false, nil
	}

	// CRD exists but neither OLMv0 nor OLMv1 shows a successful installation
	return false, olmv1CEExists, fmt.Errorf("FlowCollector CRD is present but could not identify how Network Observability Operator was installed (neither OLMv1 ClusterExtension nor OLMv0 ClusterServiceVersion found)")
}

// checkOLMv1Installation checks if the operator is installed via OLMv1 (ClusterExtension)
// Returns three values:
// - installed: true if the operator is fully installed via OLMv1
// - clusterExtensionExists: true if the ClusterExtension resource exists (regardless of status)
// - err: error if there was a problem checking the installation
func (r *ReconcileObservability) checkOLMv1Installation(ctx context.Context) (installed bool, clusterExtensionExists bool, err error) {
	clusterExtension := &unstructured.Unstructured{}
	clusterExtension.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "olm.operatorframework.io",
		Version: "v1",
		Kind:    "ClusterExtension",
	})

	if err := r.client.Get(ctx, types.NamespacedName{Name: "netobserv-operator"}, clusterExtension); err != nil {
		if errors.IsNotFound(err) {
			return false, false, nil
		}
		return false, false, err
	}

	// ClusterExtension exists
	// Check its status conditions
	conditions, found, err := unstructured.NestedSlice(clusterExtension.Object, "status", "conditions")
	if err != nil {
		return false, true, fmt.Errorf("failed to get ClusterExtension status conditions: %w", err)
	}
	if !found {
		return false, true, fmt.Errorf("ClusterExtension exists but has no status conditions")
	}

	// Check for "Installed" condition
	for _, cond := range conditions {
		condMap, ok := cond.(map[string]interface{})
		if !ok {
			continue
		}
		condType, _, _ := unstructured.NestedString(condMap, "type")
		condStatus, _, _ := unstructured.NestedString(condMap, "status")
		condReason, _, _ := unstructured.NestedString(condMap, "reason")
		condMessage, _, _ := unstructured.NestedString(condMap, "message")

		if condType == "Installed" {
			switch condStatus {
			case "True":
				return true, true, nil
			case "False":
				return false, true, fmt.Errorf("ClusterExtension installation failed: %s - %s", condReason, condMessage)
			default:
				// Status is "Unknown" or other - not yet installed
				return false, true, nil
			}
		}
	}

	// ClusterExtension exists but no "Installed" condition found
	return false, true, nil
}

// checkOLMv0Installation checks if the operator is installed via OLMv0 (CSV)
func (r *ReconcileObservability) checkOLMv0Installation(ctx context.Context) (bool, error) {
	csvList := &unstructured.UnstructuredList{}
	csvList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "operators.coreos.com",
		Version: "v1alpha1",
		Kind:    "ClusterServiceVersion",
	})

	if err := r.client.List(ctx, csvList, crclient.InNamespace(OperatorNamespace)); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	// Look for netobserv operator CSV
	for _, item := range csvList.Items {
		name := item.GetName()
		// CSV names typically follow pattern: netobserv-operator.v1.2.3
		if strings.HasPrefix(name, "netobserv-operator") {
			// Check the CSV phase
			phase, found, err := unstructured.NestedString(item.Object, "status", "phase")
			if err != nil {
				return false, fmt.Errorf("failed to get ClusterServiceVersion status phase: %w", err)
			}
			if !found {
				return false, fmt.Errorf("ClusterServiceVersion exists but has no status phase")
			}

			switch phase {
			case "Succeeded":
				return true, nil
			case "Failed":
				reason, _, _ := unstructured.NestedString(item.Object, "status", "reason")
				message, _, _ := unstructured.NestedString(item.Object, "status", "message")
				return false, fmt.Errorf("ClusterServiceVersion installation failed: %s - %s", reason, message)
			default:
				// Other phases (Installing, Pending, Replacing, Deleting, etc.) - not yet installed
				return false, nil
			}
		}
	}

	// No CSV found
	return false, nil
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
		klog.Infof("Applied %s %s", description, obj.GetName())
	}
	klog.Infof("Successfully applied %s", description)
	return nil
}

func (r *ReconcileObservability) installNetObservOperator(ctx context.Context) error {
	return r.applyManifest(ctx, OperatorYAML, "Network Observability Operator")
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
