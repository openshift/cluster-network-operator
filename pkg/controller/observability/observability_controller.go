package observability

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
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
	NetObservNamespace   = "openshift-network-observability"
	OperatorNamespace    = "openshift-netobserv-operator"
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

	// Store controller and manager for dynamic watch setup later
	r.controller = c
	r.manager = mgr

	// Watch Network CR - this is the primary trigger
	return c.Watch(source.Kind(mgr.GetCache(), &configv1.Network{}, &handler.TypedEnqueueRequestForObject[*configv1.Network]{}))
}

var _ reconcile.Reconciler = &ReconcileObservability{}

type ReconcileObservability struct {
	client               crclient.Client
	featureGate          featuregates.FeatureGate
	controller           controller.Controller
	manager              manager.Manager
	flowCollectorWatched bool
	clusterExtWatched    bool
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
	installed, err := r.isNetObservOperatorInstalled(ctx)
	if err != nil {
		klog.Warningf("Failed to check if Network Observability Operator is installed: %v. Will retry in %v.", err, requeueAfterStandard)
		// Mark deployment as failed with the error details
		_ = r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, "DeploymentFailed", fmt.Sprintf("Failed to check Network Observability Operator status: %v", err))
		return reconcile.Result{RequeueAfter: requeueAfterStandard}, nil
	}
	if !installed {
		// Install Network Observability Operator
		if err := r.installNetObservOperator(ctx); err != nil {
			klog.Warningf("Failed to install Network Observability Operator: %v. Will retry in %v.", err, requeueAfterOLM)
			// Mark deployment as failed
			_ = r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, "DeploymentFailed", fmt.Sprintf("Failed to install Network Observability Operator: %v", err))
			return reconcile.Result{RequeueAfter: requeueAfterOLM}, nil
		}

		// Wait for Network Observability Operator to be ready
		klog.Info("Wait for Network Observability to be ready")
		if err := r.waitForNetObservOperator(ctx); err != nil {
			if err == context.DeadlineExceeded {
				klog.Warningf("Timed out waiting for Network Observability Operator to be ready after %v. Will retry in %v.", checkTimeout, requeueAfterOLM)
				// Mark deployment as failed due to timeout
				_ = r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, "DeploymentFailed", fmt.Sprintf("Timed out waiting for Network Observability Operator to be ready after %v", checkTimeout))
			} else {
				klog.Warningf("Failed waiting for Network Observability Operator: %v. Will retry in %v.", err, requeueAfterOLM)
				// Mark deployment as failed
				_ = r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, "DeploymentFailed", fmt.Sprintf("Failed waiting for Network Observability Operator: %v", err))
			}
			return reconcile.Result{RequeueAfter: requeueAfterOLM}, nil
		}
	}

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

	// Set up dynamic watches for ClusterExtension and FlowCollector now that they exist
	r.setupDynamicWatches(ctx)

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

	// Check if the feature gate is registered in the cluster's feature gate list
	// to avoid panic when the feature gate doesn't exist yet
	knownFeatures := r.featureGate.KnownFeatures()
	for _, known := range knownFeatures {
		if known == featureGateName {
			return r.featureGate.Enabled(featureGateName)
		}
	}

	// Feature gate not registered yet (older API version), default to disabled
	klog.V(4).Info("NetworkObservabilityInstall feature gate is not registered yet, defaulting to disabled")
	return false
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

	// Check if the condition already exists with the same status and reason
	for _, condition := range network.Status.Conditions {
		if condition.Type == NetworkObservabilityDeployed &&
			condition.Status == status &&
			condition.Reason == reason {
			// Already set with same status and reason, no need to update
			klog.V(4).Infof("Network Observability condition already set to %s with reason %s", status, reason)
			return nil
		}
	}

	// Create the condition to add/update
	now := metav1.Now()
	newCondition := operatorv1.OperatorCondition{
		Type:               NetworkObservabilityDeployed,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
	}

	// Update or append the condition in the status
	conditionFound := false
	for i := range network.Status.Conditions {
		if network.Status.Conditions[i].Type == NetworkObservabilityDeployed {
			network.Status.Conditions[i] = newCondition
			conditionFound = true
			break
		}
	}
	if !conditionFound {
		network.Status.Conditions = append(network.Status.Conditions, newCondition)
	}

	// Update the status using controller-runtime client
	if err := r.client.Status().Update(ctx, network); err != nil {
		return fmt.Errorf("failed to update operator Network status: %w", err)
	}

	klog.Infof("Set Network Observability condition to %s: %s", status, reason)
	return nil
}

// shouldInstallNetworkObservability returns true if Network Observability should be installed.
// Valid values: "", "InstallAndEnable", "NoAction"
// "NoAction": skip installation (user opted out)
// "InstallAndEnable": install Network Observability (even on SNO clusters), always reinstall if missing
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

	// Explicit enable - install regardless of topology, always reinstall if missing
	if value == configv1.NetworkObservabilityInstallAndEnable {
		return true, nil
	}

	// Default behavior (empty string): install once, do not reinstall
	// Check if already deployed
	deployed, err := r.wasNetworkObservabilityDeployed(ctx)
	if err != nil {
		return false, err
	}
	if deployed {
		// Already deployed, do not reinstall
		klog.V(4).Info("Network Observability already deployed (default policy), skipping reinstallation")
		return false, nil
	}

	// Check if this is a SNO cluster
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
// It checks both OLMv1 (ClusterExtension) and OLMv0 (CSV) to determine installation status.
func (r *ReconcileObservability) isNetObservOperatorInstalled(ctx context.Context) (bool, error) {
	// Check if the FlowCollector CRD exists
	crd := &unstructured.Unstructured{}
	crd.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "apiextensions.k8s.io",
		Version: "v1",
		Kind:    "CustomResourceDefinition",
	})

	err := r.client.Get(ctx, types.NamespacedName{
		Name: "flowcollectors.flows.netobserv.io",
	}, crd)

	crdExists := true
	if err != nil {
		if errors.IsNotFound(err) {
			crdExists = false
		} else {
			return false, err
		}
	}

	// Check OLMv1 (ClusterExtension) installation status
	olmv1Installed, olmv1Err := r.checkOLMv1Installation(ctx)
	if olmv1Err != nil && !errors.IsNotFound(olmv1Err) {
		// Installation error from OLMv1
		return false, fmt.Errorf("OLMv1 installation error: %w", olmv1Err)
	}

	// Check OLMv0 (CSV/Subscription) installation status
	olmv0Installed, olmv0Err := r.checkOLMv0Installation(ctx)
	if olmv0Err != nil && !errors.IsNotFound(olmv0Err) {
		// Installation error from OLMv0
		return false, fmt.Errorf("OLMv0 installation error: %w", olmv0Err)
	}

	// If CRD doesn't exist but either OLM installation is present, this is an error condition
	if !crdExists {
		if olmv0Installed || olmv1Installed {
			olmVersion := "OLMv0"
			if olmv1Installed {
				olmVersion = "OLMv1"
			}
			return false, fmt.Errorf("network Observability Operator was deployed via %s but FlowCollector CRD is missing (manually removed)", olmVersion)
		}
		// If CRD doesn't exist and no OLM installation, operator is not installed
		return false, nil
	}

	if olmv1Installed {
		klog.V(4).Info("Network Observability Operator installed via OLMv1 (ClusterExtension)")
		return true, nil
	}

	if olmv0Installed {
		klog.V(4).Info("Network Observability Operator installed via OLMv0 (CSV)")
		return true, nil
	}

	// CRD exists but neither OLMv0 nor OLMv1 shows a successful installation
	return false, fmt.Errorf("FlowCollector CRD is present but could not identify how Network Observability Operator was installed (neither OLMv1 ClusterExtension nor OLMv0 CSV found)")
}

// checkOLMv1Installation checks if the operator is installed via OLMv1 (ClusterExtension)
func (r *ReconcileObservability) checkOLMv1Installation(ctx context.Context) (bool, error) {
	clusterExtension := &unstructured.Unstructured{}
	clusterExtension.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "olm.operatorframework.io",
		Version: "v1",
		Kind:    "ClusterExtension",
	})

	if err := r.client.Get(ctx, types.NamespacedName{Name: "netobserv-operator"}, clusterExtension); err != nil {
		return false, err
	}

	// ClusterExtension exists, check its status conditions
	conditions, found, err := unstructured.NestedSlice(clusterExtension.Object, "status", "conditions")
	if err != nil {
		return false, fmt.Errorf("failed to get ClusterExtension status conditions: %w", err)
	}
	if !found {
		return false, fmt.Errorf("ClusterExtension exists but has no status conditions")
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
				return true, nil
			case "False":
				return false, fmt.Errorf("ClusterExtension installation failed: %s - %s", condReason, condMessage)
			default:
				// Status is "Unknown" or other - not yet installed
				return false, nil
			}
		}
	}

	// ClusterExtension exists but no "Installed" condition found
	return false, nil
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
		return false, err
	}

	// Look for netobserv operator CSV
	for _, item := range csvList.Items {
		name := item.GetName()
		// CSV names typically follow pattern: netobserv-operator.v1.2.3
		if len(name) >= len("netobserv-operator") && name[:len("netobserv-operator")] == "netobserv-operator" {
			// Check the CSV phase
			phase, found, err := unstructured.NestedString(item.Object, "status", "phase")
			if err != nil {
				return false, fmt.Errorf("failed to get CSV status phase: %w", err)
			}
			if !found {
				return false, fmt.Errorf("CSV exists but has no status phase")
			}

			switch phase {
			case "Succeeded":
				return true, nil
			case "Failed":
				reason, _, _ := unstructured.NestedString(item.Object, "status", "reason")
				message, _, _ := unstructured.NestedString(item.Object, "status", "message")
				return false, fmt.Errorf("CSV installation failed: %s - %s", reason, message)
			default:
				// Other phases (Installing, Pending, Replacing, Deleting, etc.) - not yet installed
				return false, nil
			}
		}
	}

	// No CSV found
	return false, errors.NewNotFound(schema.GroupResource{Group: "operators.coreos.com", Resource: "clusterserviceversions"}, "netobserv-operator")
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

func (r *ReconcileObservability) waitForNetObservOperator(ctx context.Context) error {
	condition := func(ctx context.Context) (bool, error) {
		// Get the ClusterExtension resource
		clusterExtension := &unstructured.Unstructured{}
		clusterExtension.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "olm.operatorframework.io",
			Version: "v1",
			Kind:    "ClusterExtension",
		})

		if err := r.client.Get(ctx, types.NamespacedName{Name: "netobserv-operator"}, clusterExtension); err != nil {
			if errors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}

		// Check the status conditions for "Installed" condition with status True
		conditions, found, err := unstructured.NestedSlice(clusterExtension.Object, "status", "conditions")
		if err != nil {
			return false, err
		}
		if !found {
			return false, nil
		}

		for _, cond := range conditions {
			condMap, ok := cond.(map[string]interface{})
			if !ok {
				continue
			}
			condType, _, _ := unstructured.NestedString(condMap, "type")
			condStatus, _, _ := unstructured.NestedString(condMap, "status")

			// Check for "Installed" condition with status "True"
			if condType == "Installed" && condStatus == "True" {
				return true, nil
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

// setupDynamicWatches sets up watches for ClusterExtension and FlowCollector
// after they are created. This is done dynamically because these CRDs may not
// exist when the controller starts.
func (r *ReconcileObservability) setupDynamicWatches(ctx context.Context) {
	// Skip if controller or manager is not set (e.g., in tests)
	if r.controller == nil || r.manager == nil {
		return
	}

	// Try to set up FlowCollector watch if not already done
	if !r.flowCollectorWatched {
		flowCollector := &unstructured.Unstructured{}
		flowCollector.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "flows.netobserv.io",
			Version: FlowCollectorVersion,
			Kind:    "FlowCollector",
		})
		if err := r.controller.Watch(source.Kind[crclient.Object](r.manager.GetCache(), flowCollector, handler.EnqueueRequestsFromMapFunc(
			func(ctx context.Context, obj crclient.Object) []reconcile.Request {
				// Only trigger reconcile for the cluster FlowCollector
				if obj.GetName() == FlowCollectorName {
					return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: NetworkCRName}}}
				}
				return nil
			},
		))); err != nil {
			klog.V(4).Infof("FlowCollector watch not yet available (CRD may not exist): %v", err)
		} else {
			r.flowCollectorWatched = true
			klog.Info("Successfully set up FlowCollector watch")
		}
	}

	// Try to set up ClusterExtension watch if not already done
	if !r.clusterExtWatched {
		clusterExtension := &unstructured.Unstructured{}
		clusterExtension.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "olm.operatorframework.io",
			Version: "v1",
			Kind:    "ClusterExtension",
		})
		if err := r.controller.Watch(source.Kind[crclient.Object](r.manager.GetCache(), clusterExtension, handler.EnqueueRequestsFromMapFunc(
			func(ctx context.Context, obj crclient.Object) []reconcile.Request {
				// Only trigger reconcile for the netobserv-operator ClusterExtension
				if obj.GetName() == "netobserv-operator" {
					return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: NetworkCRName}}}
				}
				return nil
			},
		))); err != nil {
			klog.V(4).Infof("ClusterExtension watch not yet available (OLMv1 may not be available): %v", err)
		} else {
			r.clusterExtWatched = true
			klog.Info("Successfully set up ClusterExtension watch")
		}
	}
}
