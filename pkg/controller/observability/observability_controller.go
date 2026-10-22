package observability

import (
	"bytes"
	"context"
	"errors"
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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/klog/v2"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// OperatorYAML is the operator manifest path. It is a var (not a const) only so
// tests can point it at a temporary manifest.
var OperatorYAML = "bindata/observability/07-observability-operator.yaml"

const (
	FlowCollectorYAML    = "bindata/observability/08-flowcollector.yaml"
	OperatorNamespace    = "netobserv-operator"
	FlowCollectorVersion = "v1beta2"
	FlowCollectorName    = "cluster"
	NetworkCRName        = "cluster"

	// Requeue interval for install/wait and standard operations
	requeueInterval = 60 * time.Second
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

// NetworkObservabilityInstalledByCNO is the operator Network status condition
// reporting whether CNO enabled and installed Network Observability and, if
// not, why not. Its Reason is one of the reason* consts defined below, grouped
// by lifecycle; each group's comment gives its Status and semantics.
const NetworkObservabilityInstalledByCNO = "NetworkObservabilityInstalledByCNO"

// Reasons for the NetworkObservabilityInstalledByCNO condition
const (
	// Re-evaluatable (Status False; re-checked every reconcile, can still install)
	reasonFeatureGateDisabled = "FeatureGateDisabled"
	reasonNoAction            = "NoAction"
	reasonSNO                 = "SNO"

	// In-progress (Status False; the install is underway)
	reasonWaitingForCluster     = "WaitingForCluster"
	reasonInstallingOperator    = "InstallingOperator"
	reasonCreatingFlowCollector = "CreatingFlowCollector"

	// Transient (Status False; a step failed and is being retried. These reasons are in
	// installAttemptReasons, so the 20-minute install clock keeps running across the retry.)
	reasonOperatorApplyFailed      = "OperatorApplyFailed"
	reasonFlowCollectorApplyFailed = "FlowCollectorApplyFailed"
	reasonRollbackFailed           = "RollbackFailed"

	// Terminal (CNO stops acting; see terminalReasons. Installed is Status True; the rest are Status False)
	reasonInstalled                 = "Installed"
	reasonPreExisting               = "PreExisting"
	reasonClusterNotAvailable       = "ClusterNotAvailable"
	reasonOperatorInstallFailed     = "OperatorInstallFailed"
	reasonFlowCollectorCreateFailed = "FlowCollectorCreateFailed"
)

// installFailureReasons are the terminal reasons CNO sets after abandoning and
// rolling back an install that timed out, named for the phase that failed.
var installFailureReasons = sets.New(
	reasonClusterNotAvailable,
	reasonOperatorInstallFailed,
	reasonFlowCollectorCreateFailed,
)

// terminalReasons are the condition reasons after which CNO stops acting on
// Network Observability. Installed means CNO completed the install; PreExisting
// means something else owns the FlowCollector CRD; the install-failure reasons
// mean the install was abandoned and rolled back.
//
// SNO is deliberately not here: it latches only against a topology change under the
// default policy (adding control-plane nodes later must not silently start an
// install the user never requested), but an explicit InstallAndEnable still
// overrides it, so SNO must be re-evaluated against the policy each reconcile.
var terminalReasons = sets.New(reasonInstalled, reasonPreExisting).Union(installFailureReasons)

// installAttemptReasons are the condition reasons that represent an in-progress
// install attempt. The install-timeout clock (hasInstallTimedOut) applies only to
// these; the other Status-False reasons (FeatureGateDisabled, NoAction, SNO) must
// be excluded, as their condition timestamp is unrelated to any install.
var installAttemptReasons = sets.New(
	reasonWaitingForCluster,
	reasonInstallingOperator,
	reasonOperatorApplyFailed,
	reasonCreatingFlowCollector,
	reasonFlowCollectorApplyFailed,
	reasonRollbackFailed,
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

	// When it returns false, shouldInstallAndSetCondition has set the condition
	// explaining why (feature gate disabled, NoAction, SNO, pre-existing CRD, or a
	// latched terminal state). The feature gate check lives there, not here, so that
	// disabling the gate mid-install or after a successful install cannot overwrite a
	// correct result.
	shouldInstall, err := r.shouldInstallAndSetCondition(ctx)
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
		return reconcile.Result{RequeueAfter: requeueInterval}, nil
	}
	if !installed {
		// Don't apply the Subscription until the cluster is available.
		if available, _ := r.clusterVersionAvailableSince(ctx); !available {
			klog.Infof("Cluster not available yet, will recheck in %v", clusterAvailableRequeueInterval)
			if err := r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, reasonWaitingForCluster, "Waiting for the cluster to be available"); err != nil {
				klog.Warningf("Failed to set WaitingForCluster condition: %v. Will retry in %v.", err, requeueInterval)
				return reconcile.Result{RequeueAfter: requeueInterval}, nil
			}
			return reconcile.Result{RequeueAfter: clusterAvailableRequeueInterval}, nil
		}

		// Persist InstallingOperator before applying the manifest. If this write
		// fails we must not apply, otherwise the manifest could create the FlowCollector
		// CRD while the condition is still absent, and the next reconcile would
		// misclassify that CNO-created CRD as PreExisting.
		if err := r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, reasonInstallingOperator, "Installing Network Observability Operator..."); err != nil {
			klog.Warningf("Failed to set InstallingOperator condition: %v. Will retry in %v.", err, requeueInterval)
			return reconcile.Result{RequeueAfter: requeueInterval}, nil
		}
		// Reapply the operator manifest every reconcile until the operator appears.
		// The apply is Server-Side Apply (idempotent), so this no-ops when nothing
		// changed and recreates the OLM resources (namespace/Subscription/OperatorGroup)
		// if they are deleted out from under us while we wait.
		if err := r.installNetObservOperator(ctx); err != nil {
			msg := fmt.Sprintf("Failed to install Network Observability Operator: %v. Will retry in %v.", err, requeueInterval)
			klog.Warning(msg)
			// Surface the apply failure (with its cause) in the status. This is transient:
			// the next reconcile re-applies and flips back to InstallingOperator. Both
			// reasons are install attempts, so the install clock keeps running across the flip.
			if err := r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, reasonOperatorApplyFailed, msg); err != nil {
				klog.Warningf("Failed to set OperatorApplyFailed condition: %v. Will retry in %v.", err, requeueInterval)
			}
			return reconcile.Result{RequeueAfter: requeueInterval}, nil
		}
		klog.Infof("Waiting for Network Observability Operator, will check in %v", requeueInterval)
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
		if err := r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, reasonCreatingFlowCollector, "Creating FlowCollector instance..."); err != nil {
			klog.Warningf("Failed to set CreatingFlowCollector condition: %v. Will retry in %v.", err, requeueInterval)
			return reconcile.Result{RequeueAfter: requeueInterval}, nil
		}
		// Create FlowCollector
		if err := r.applyManifest(ctx, FlowCollectorYAML, "FlowCollector"); err != nil {
			msg := fmt.Sprintf("Failed to create FlowCollector: %v. Will retry in %v.", err, requeueInterval)
			klog.Warning(msg)
			// Surface the apply failure (with its cause) in the status. This is transient:
			// the next reconcile re-applies and flips back to CreatingFlowCollector. Both
			// reasons are install attempts, so the install clock keeps running across the flip.
			if err := r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, reasonFlowCollectorApplyFailed, msg); err != nil {
				klog.Warningf("Failed to set FlowCollectorApplyFailed condition: %v. Will retry in %v.", err, requeueInterval)
			}
			return reconcile.Result{RequeueAfter: requeueInterval}, nil
		}
		klog.Info("FlowCollector created successfully")
	}

	// Mark as installed to track installation status
	if err := r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionTrue, reasonInstalled, "Network Observability has been installed. Will not be managed by CNO."); err != nil {
		klog.Warningf("Failed to mark Network Observability as installed: %v. Will retry in %v.", err, requeueInterval)
		return reconcile.Result{RequeueAfter: requeueInterval}, nil
	}

	klog.V(4).Info("Network Observability has been installed")
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

// hasInstallTimedOut returns true when the installation should be abandoned. On
// day 0, the install clock only starts once the cluster is available. A backstop
// from our own condition timestamp bounds the wait even if that read fails.
func (r *ReconcileObservability) hasInstallTimedOut(ctx context.Context) bool {
	network := &operatorv1.Network{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: NetworkCRName}, network); err != nil {
		return false
	}

	condition := operatorv1helpers.FindOperatorCondition(network.Status.Conditions, NetworkObservabilityInstalledByCNO)
	if condition == nil || condition.Status != operatorv1.ConditionFalse {
		return false
	}
	// Only an in-progress installation can time out. Re-evaluatable states that
	// are not an install attempt (FeatureGateDisabled, NoAction, SNO) must not,
	// as their condition timestamp is unrelated to the install clock.
	if installFailureReasons.Has(condition.Reason) {
		return true
	}
	if !installAttemptReasons.Has(condition.Reason) {
		return false
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
	// A terminal failure reason is only set after rollback succeeds, so its
	// presence means there is nothing left to do.
	network := &operatorv1.Network{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: NetworkCRName}, network); err == nil {
		if condition := operatorv1helpers.FindOperatorCondition(network.Status.Conditions, NetworkObservabilityInstalledByCNO); condition != nil &&
			installFailureReasons.Has(condition.Reason) {
			return nil
		}
	}

	klog.Warning("Network Observability installation timed out, giving up")

	// Name the terminal failure for the phase that did not complete. This is derived
	// from stable cluster facts, not the current condition reason, so it stays
	// correct across RollbackFailed retries (teardown does not delete the CRD).
	reason, message := r.installFailure(ctx)

	// Roll back before latching the terminal condition, so a failed teardown is
	// retried on the next reconcile.
	if err := r.teardownNetObservOperator(ctx); err != nil {
		// Best-effort status update: we return the teardown error below, so the
		// caller requeues and retries regardless of whether this write lands.
		_ = r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, reasonRollbackFailed,
			"Failed to roll back OLM resources. Retrying.")
		return fmt.Errorf("failed to roll back Network Observability OLM resources: %w", err)
	}
	klog.Info("Rolled back Network Observability OLM resources")

	return r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, reason, message)
}

// installFailure returns the terminal reason and message for an abandoned install,
// named for the phase that did not complete. If the cluster never became available
// the install never started; otherwise the FlowCollector CRD's presence tells
// whether the operator install or the FlowCollector creation was the phase that
// timed out.
func (r *ReconcileObservability) installFailure(ctx context.Context) (string, string) {
	if available, _ := r.clusterVersionAvailableSince(ctx); !available {
		return reasonClusterNotAvailable, "Cluster did not become available within 90 minutes. Will not retry."
	}
	if installed, err := r.isNetObservOperatorInstalled(ctx); err == nil && installed {
		return reasonFlowCollectorCreateFailed, "Failed to create FlowCollector within 20 minutes. Rolled back. Will not retry."
	}
	return reasonOperatorInstallFailed, "Failed to install Network Observability Operator within 20 minutes. Rolled back. Will not retry."
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
	if err := r.client.Delete(ctx, subscription); err != nil && !apierrors.IsNotFound(err) {
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
	if err := r.client.Delete(ctx, operatorGroup); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete OperatorGroup: %w", err)
	}

	csvList := &unstructured.UnstructuredList{}
	csvList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "operators.coreos.com",
		Version: "v1alpha1",
		Kind:    "ClusterServiceVersion",
	})
	if err := r.client.List(ctx, csvList, crclient.InNamespace(OperatorNamespace)); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to list ClusterServiceVersions: %w", err)
	}
	for i := range csvList.Items {
		csv := &csvList.Items[i]
		if !strings.HasPrefix(csv.GetName(), "network-observability-operator") {
			continue
		}
		if err := r.client.Delete(ctx, csv); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete ClusterServiceVersion %s: %w", csv.GetName(), err)
		}
	}

	// Only delete the namespace when CNO created it. Deleting a pre-existing
	// namespace would cascade-delete independently created resources.
	namespace := &corev1.Namespace{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: OperatorNamespace}, namespace); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get namespace: %w", err)
	}
	if namespace.GetAnnotations()[createdByCNOAnnotation] != "true" {
		klog.Infof("Namespace %s was not created by CNO; leaving it in place", OperatorNamespace)
		return nil
	}
	if err := r.client.Delete(ctx, namespace); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete namespace: %w", err)
	}

	return nil
}

// setNetworkObservabilityCondition sets the NetworkObservabilityInstalledByCNO condition
// in the network.operator.openshift.io Network CR status
func (r *ReconcileObservability) setNetworkObservabilityCondition(ctx context.Context, status operatorv1.ConditionStatus, reason, message string) error {
	// Get the operator Network CR
	network := &operatorv1.Network{}
	if err := r.client.Get(ctx, types.NamespacedName{Name: NetworkCRName}, network); err != nil {
		return fmt.Errorf("failed to get operator Network CR: %w", err)
	}

	existing := operatorv1helpers.FindOperatorCondition(network.Status.Conditions, NetworkObservabilityInstalledByCNO)
	if existing != nil && existing.Status == status && existing.Reason == reason && existing.Message == message {
		// Already set with same status, reason, and message, no need to update
		klog.V(4).Infof("Network Observability condition already set to %s with reason %s", status, reason)
		return nil
	}

	// Starting an install attempt from a non-install state keeps Status False, so
	// SetOperatorCondition would preserve the stale LastTransitionTime and trip the
	// timeout backstop. Stamp it now so the install clock starts with the attempt.
	if existing != nil && installAttemptReasons.Has(reason) && !installAttemptReasons.Has(existing.Reason) {
		existing.LastTransitionTime = metav1.NewTime(time.Now())
	}

	// Create the condition to add/update
	operatorv1helpers.SetOperatorCondition(&network.Status.Conditions, operatorv1.OperatorCondition{
		Type:    NetworkObservabilityInstalledByCNO,
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

// shouldInstallAndSetCondition determines whether Network Observability should be
// installed. As a side effect, when it returns false it sets the
// NetworkObservabilityInstalledByCNO condition explaining why (FeatureGateDisabled,
// NoAction, SNO, or PreExisting); when it returns true the install is left to
// Reconcile, which sets the in-progress condition.
//
// Terminal states (Installed, PreExisting, and the install-failure reasons) are permanent.
// Re-evaluatable states (FeatureGateDisabled, NoAction, SNO) and the in-progress states
// (InstallingOperator, CreatingFlowCollector, RollbackFailed) are checked each reconcile.
// SNO is latched against topology changes under the default policy but is still overridden
// by InstallAndEnable.
//
// network.Spec.NetworkObservability.InstallationPolicy values:
//
//	"InstallAndEnable": install Network Observability once (even on SNO clusters)
//	"NoAction":         skip installation (user opted out)
//	undefined (unset):  install Network Observability once (opt-out model), except for SNO clusters
func (r *ReconcileObservability) shouldInstallAndSetCondition(ctx context.Context) (bool, error) {
	// Terminal and in-progress states are decided from our own condition, before
	// anything else, so the install is not re-evaluated once it has concluded or
	// while it is already underway.
	operatorNetwork := &operatorv1.Network{}
	var currentReason string
	if err := r.client.Get(ctx, types.NamespacedName{Name: NetworkCRName}, operatorNetwork); err != nil {
		if !apierrors.IsNotFound(err) {
			return false, err
		}
	} else if condition := operatorv1helpers.FindOperatorCondition(operatorNetwork.Status.Conditions, NetworkObservabilityInstalledByCNO); condition != nil {
		currentReason = condition.Reason
		if terminalReasons.Has(condition.Reason) {
			return false, nil
		}
		switch condition.Reason {
		case reasonInstallingOperator, reasonOperatorApplyFailed, reasonCreatingFlowCollector, reasonFlowCollectorApplyFailed, reasonRollbackFailed:
			// Install is underway (or being rolled back); keep driving it from
			// Reconcile. This also prevents a CRD that CNO itself installed from
			// being mistaken for a pre-existing one below.
			return true, nil
		}
	}

	// The feature gate check must be here (after the terminal/in-progress checks),
	// not in Reconcile(). There is no way to abort, so disabling the gate midway
	// would overwrite a correct result (e.g. Installed, Status True) with
	// FeatureGateDisabled, Status False.
	if !r.isFeatureGateEnabled() {
		return false, r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, reasonFeatureGateDisabled, "NetworkObservabilityInstall feature gate is not enabled")
	}

	// Get Network CR information
	var network configv1.Network
	if err := r.client.Get(ctx, types.NamespacedName{Name: NetworkCRName}, &network); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	value := network.Spec.NetworkObservability.InstallationPolicy

	// Explicit disable
	if value == configv1.NetworkObservabilityNoAction {
		return false, r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, reasonNoAction, "Installation policy is set to NoAction so it will not install Network Observability.")
	}

	// If the FlowCollector CRD already exists and CNO did not install it (the
	// in-progress check above excluded that case), something else owns it.
	crdExists, err := r.isNetObservOperatorInstalled(ctx)
	if err != nil {
		return false, err
	}
	if crdExists {
		return false, r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, reasonPreExisting, "FlowCollector CRD already exists. Will not be managed by CNO.")
	}

	// InstallAndEnable installs regardless of topology, including overriding a
	// previously recorded SNO decision.
	if value == configv1.NetworkObservabilityInstallAndEnable {
		return true, nil
	}

	// Under the default policy, once SNO has been recorded, keep it latched.
	// Adding control-plane nodes later will not trigger an install, but an
	// explicit InstallAndEnable (handled above) will.
	if currentReason == reasonSNO {
		return false, nil
	}

	isSNO, err := r.isSingleNodeCluster(ctx)
	if err != nil {
		return false, err
	}
	if isSNO {
		return false, r.setNetworkObservabilityCondition(ctx, operatorv1.ConditionFalse, reasonSNO, "Single control-plane cluster. Network Observability will not be installed, even if control-plane nodes are added later.")
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
		if apierrors.IsNotFound(err) {
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
			if errors.Is(err, io.EOF) {
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
	case apierrors.IsNotFound(err):
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
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	return true, nil
}
