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
	operatorv1helpers "github.com/openshift/library-go/pkg/operator/v1helpers"
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

	NetworkObservabilityInstalledByCNO = "NetworkObservabilityInstalledByCNO"

	requeueInterval      = 30 * time.Second
	installTimeout       = 10 * time.Minute
)

var terminalReasons = map[string]bool{
	"Installed":   true,
	"PreExisting": true,
	"Failed":      true,
}

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
	// This ensures that status condition changes trigger reconciliation.
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
		return reconcile.Result{}, nil
	}

	shouldInstall, err := r.shouldInstallNetworkObservability(ctx)
	if err != nil {
		klog.Warningf("Failed to determine if Network Observability should be installed: %v. Will retry in %v.", err, requeueInterval)
		return reconcile.Result{RequeueAfter: requeueInterval}, nil
	}
	if !shouldInstall {
		return reconcile.Result{}, nil
	}

	crdExists, err := r.doesFlowCollectorCRDExist(ctx)
	if err != nil {
		klog.Warningf("Failed to check FlowCollector CRD: %v. Will retry in %v.", err, requeueInterval)
		return reconcile.Result{RequeueAfter: requeueInterval}, nil
	}

	if !crdExists {
		// Reapply the operator manifest until it applies cleanly. Once it does,
		// move to WaitingForOperator so we stop reapplying and just wait for the
		// FlowCollector CRD to appear.
		if !r.isWaitingForOperator(ctx) {
			// Set the status before applying so it reflects what is happening.
			_ = r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, "InstallationInProgress", "Installing Network Observability Operator")
			if err := r.applyManifest(ctx, OperatorYAML, "Network Observability Operator"); err != nil {
				klog.Warningf("Failed to install Network Observability Operator: %v. Will retry in %v.", err, requeueInterval)
				return reconcile.Result{RequeueAfter: requeueInterval}, nil
			}
			_ = r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, "WaitingForOperator", "Waiting until Network Observability Operator is complete")
		}
		klog.V(4).Infof("Waiting for FlowCollector CRD, will check in %v", requeueInterval)
		return reconcile.Result{RequeueAfter: requeueInterval}, nil
	}

	flowCollectorExists, err := r.doesFlowCollectorExist(ctx)
	if err != nil {
		klog.Warningf("Failed to check FlowCollector: %v. Will retry in %v.", err, requeueInterval)
		return reconcile.Result{RequeueAfter: requeueInterval}, nil
	}

	if !flowCollectorExists {
		// Set the status before applying so it reflects what is happening.
		_ = r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, "CreatingFlowCollector", "Creating FlowCollector")
		if err := r.applyManifest(ctx, FlowCollectorYAML, "FlowCollector"); err != nil {
			klog.Warningf("Failed to create FlowCollector: %v. Will retry in %v.", err, requeueInterval)
			return reconcile.Result{RequeueAfter: requeueInterval}, nil
		}
	}

	_ = r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionTrue, "Installed", "Network Observability has been installed. Will not manage.")
	klog.Info("Network Observability has been installed")
	return reconcile.Result{}, nil
}

// isFeatureGateEnabled checks if the NetworkObservabilityInstall feature gate is enabled.
// If featureGate is nil (e.g., in tests), returns false to default to disabled.
func (r *ReconcileObservability) isFeatureGateEnabled() bool {
	if r.featureGate == nil {
		return false
	}
	return r.featureGate.Enabled(configv1.FeatureGateName("NetworkObservabilityInstall"))
}

// shouldInstallNetworkObservability determines whether Network Observability should be installed.
// Sets the NetworkObservabilityInstalledByCNO condition for every outcome.
// Terminal states (Installed, PreExisting, Failed) are permanent.
// Re-evaluatable states (FeatureGateDisabled, NoAction, SNO) and the in-progress
// states (InstallationInProgress, WaitingForOperator, CreatingFlowCollector) are
// checked each reconcile.
func (r *ReconcileObservability) shouldInstallNetworkObservability(ctx context.Context) (bool, error) {
	operatorNetwork := &operatorv1.Network{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: NetworkCRName}, operatorNetwork); err != nil {
		return false, err
	}

	condition := operatorv1helpers.FindOperatorCondition(operatorNetwork.Status.Conditions, NetworkObservabilityInstalledByCNO)
	if condition != nil {
		if terminalReasons[condition.Reason] {
			return false, nil
		}
		switch condition.Reason {
		case "InstallationInProgress":
			// Applying the operator manifest; keep retrying until it succeeds.
			return true, nil
		case "WaitingForOperator":
			// Operator applied; waiting for the FlowCollector CRD to appear.
			if time.Since(condition.LastTransitionTime.Time) > installTimeout {
				_ = r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, "Failed",
					"Failed to install Network Observability Operator after 10 minutes. Will not retry.")
				return false, nil
			}
			return true, nil
		case "CreatingFlowCollector":
			// Creating the FlowCollector CR; retrying until it succeeds.
			if time.Since(condition.LastTransitionTime.Time) > installTimeout {
				_ = r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, "Failed",
					"Failed to create FlowCollector after 10 minutes. Will not retry.")
				return false, nil
			}
			return true, nil
		}
	}

	// Feature gate check must be here (after terminal/in-progress checks), not
	// in Reconcile(). There's no way to abort, so disabling the gate midway
	// would overwrite the correct result (e.g. Installed and Status: True)
	// with FeatureGateDisabled and Status: False.
	if !r.isFeatureGateEnabled() {
		_ = r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, "FeatureGateDisabled", "NetworkObservabilityInstall feature gate is not enabled")
		return false, nil
	}

	var network configv1.Network
	if err := r.client.Get(ctx, types.NamespacedName{Name: NetworkCRName}, &network); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	policy := network.Spec.NetworkObservability.InstallationPolicy

	if policy == configv1.NetworkObservabilityNoAction {
		_ = r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, "NoAction", "Installation policy is set to NoAction")
		return false, nil
	}

	crdExists, err := r.doesFlowCollectorCRDExist(ctx)
	if err != nil {
		return false, err
	}
	if crdExists {
		_ = r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, "PreExisting", "FlowCollector CRD already exists. Will not manage.")
		return false, nil
	}

	isSNO, err := r.isSingleNodeCluster(ctx)
	if err != nil {
		return false, err
	}
	if isSNO && policy != configv1.NetworkObservabilityInstallAndEnable {
		_ = r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, "SNO", "Single control plane cluster. Will not install.")
		return false, nil
	}

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

func (r *ReconcileObservability) doesFlowCollectorCRDExist(ctx context.Context) (bool, error) {
	crd := &unstructured.Unstructured{}
	crd.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "apiextensions.k8s.io",
		Version: "v1",
		Kind:    "CustomResourceDefinition",
	})

	err := r.client.Get(ctx, types.NamespacedName{
		Name: "flowcollectors.flows.netobserv.io",
	}, crd)
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

// doesFlowCollectorExist returns true if a FlowCollector instance exists.
// FlowCollector is a cluster-scoped singleton resource named "cluster".
func (r *ReconcileObservability) doesFlowCollectorExist(ctx context.Context) (bool, error) {
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

// isWaitingForOperator reports whether the operator manifest has already been
// applied successfully and we are now waiting for the FlowCollector CRD to appear.
// It gates reapplying the operator manifest: while false, the manifest is
// reapplied each reconcile (retrying failed applies); once true, we only wait.
func (r *ReconcileObservability) isWaitingForOperator(ctx context.Context) bool {
	network := &operatorv1.Network{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: NetworkCRName}, network); err != nil {
		return false
	}
	condition := operatorv1helpers.FindOperatorCondition(network.Status.Conditions, NetworkObservabilityInstalledByCNO)
	return condition != nil && condition.Reason == "WaitingForOperator"
}

// setNetworkObservabilityCondition sets the NetworkObservabilityInstalledByCNO condition
// in the network.operator.openshift.io Network CR status.
func (r *ReconcileObservability) setNetworkObservabilityCondition(ctx context.Context, status operatorv1.ConditionStatus, reason, message string) error {
	network := &operatorv1.Network{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: NetworkCRName}, network); err != nil {
		return fmt.Errorf("failed to get operator Network CR: %w", err)
	}

	if condition := operatorv1helpers.FindOperatorCondition(network.Status.Conditions, NetworkObservabilityInstalledByCNO); condition != nil &&
		condition.Status == status &&
		condition.Reason == reason {
		klog.V(4).Infof("Network Observability condition already set to %s with reason %s", status, reason)
		return nil
	}

	// Remove existing condition so SetOperatorCondition creates a fresh one
	// with LastTransitionTime set to now. Without this, LastTransitionTime
	// only updates on Status changes (True↔False), not Reason changes
	// within the same Status.
	operatorv1helpers.RemoveOperatorCondition(&network.Status.Conditions, NetworkObservabilityInstalledByCNO)

	operatorv1helpers.SetOperatorCondition(&network.Status.Conditions, operatorv1.OperatorCondition{
		Type:    NetworkObservabilityInstalledByCNO,
		Status:  status,
		Reason:  reason,
		Message: message,
	})

	if err := r.client.Status().Update(ctx, network); err != nil {
		return fmt.Errorf("failed to update operator Network status: %w", err)
	}

	klog.Infof("Set Network Observability condition to %s: %s", status, reason)
	return nil
}

// applyManifest reads a YAML file and applies all resources using server-side apply.
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

		data, err := obj.MarshalJSON()
		if err != nil {
			return fmt.Errorf("failed to marshal %s %s: %w", obj.GetKind(), obj.GetName(), err)
		}

		patch := crclient.RawPatch(types.ApplyPatchType, data)
		if err := r.client.Patch(ctx, obj, patch, &crclient.PatchOptions{
			FieldManager: "cno-observability-controller",
		}); err != nil {
			return fmt.Errorf("failed to apply %s %s: %w", obj.GetKind(), obj.GetName(), err)
		}
		klog.V(4).Infof("Applied %s %s", description, obj.GetName())
	}
	klog.Infof("Successfully applied %s", description)
	return nil
}
