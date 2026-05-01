package observability

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	applyconfigv1 "github.com/openshift/client-go/config/applyconfigurations/config/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
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
	applyconfigmetav1 "k8s.io/client-go/applyconfigurations/meta/v1"
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
	OperatorYAML         = "bindata/observability/07-observability-operator.yaml"
	FlowCollectorYAML    = "bindata/observability/08-flowcollector.yaml"
	NetObservNamespace   = "openshift-network-observability"
	OperatorNamespace    = "openshift-netobserv-operator"
	FlowCollectorVersion = "v1beta2"
	FlowCollectorName    = "cluster"

	NetworkObservabilityDeployed = "NetworkObservabilityDeployed"

	checkInterval        = 10 * time.Second
	checkTimeout         = 10 * time.Minute
	requeueAfterOLM      = 5 * time.Minute  // Requeue interval for OLM operations (install/wait)
	requeueAfterStandard = 30 * time.Second // Requeue interval for standard operations
)

// Add creates a new controller. Referenced in add_networkconfig.go.
func Add(mgr manager.Manager, status *statusmanager.StatusManager, cnoClient cnoclient.Client, featureGate featuregates.FeatureGate) error {
	klog.Info("Add Network Observability Operator to manager")
	configClient, err := configv1client.NewForConfig(cnoClient.Default().Config())
	if err != nil {
		return fmt.Errorf("failed to create config client: %w", err)
	}
	return add(mgr, newReconciler(mgr.GetClient(), status, configClient, featureGate))
}

func newReconciler(client crclient.Client, status *statusmanager.StatusManager, configClient configv1client.ConfigV1Interface, featureGate featuregates.FeatureGate) *ReconcileObservability {
	return &ReconcileObservability{
		client:       client,
		status:       status,
		configClient: configClient,
		featureGate:  featureGate,
	}
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
	client       crclient.Client
	status       StatusReporter
	configClient configv1client.ConfigV1Interface
	featureGate  featuregates.FeatureGate
}

// Reconcile reacts to changes in Network CR
func (r *ReconcileObservability) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	klog.Info("Reconcile Network Observability")

	if req.Name != FlowCollectorName {
		return ctrl.Result{}, nil // only reconcile the singleton Network object
	}

	// Check if NetworkObservabilityInstall feature gate is enabled
	if !r.isFeatureGateEnabled() {
		klog.V(4).Info("NetworkObservabilityInstall feature gate is disabled, skipping Network Observability management")
		// Clear any degraded status
		r.status.SetNotDegraded(statusmanager.ObservabilityConfig)
		return ctrl.Result{}, nil
	}

	// Get Network CR information
	var network configv1.Network
	if err := r.client.Get(ctx, types.NamespacedName{Name: FlowCollectorName}, &network); err != nil {
		return ctrl.Result{}, crclient.IgnoreNotFound(err)
	}

	// Check if Network Observability should be enabled
	shouldInstall, err := r.shouldInstallNetworkObservability(ctx, &network)
	if err != nil {
		klog.Warningf("Failed to determine if Network Observability should be installed: %v. Will retry in %v.", err, requeueAfterStandard)
		return ctrl.Result{RequeueAfter: requeueAfterStandard}, nil
	}
	if !shouldInstall {
		r.status.SetNotDegraded(statusmanager.ObservabilityConfig)
		return ctrl.Result{}, nil
	}

	// Proceed with installation/reinstallation
	installed, err := r.isNetObservOperatorInstalled(ctx)
	if err != nil {
		klog.Warningf("Failed to check if Network Observability Operator is installed: %v. Will retry in %v.", err, requeueAfterStandard)
		return ctrl.Result{RequeueAfter: requeueAfterStandard}, nil
	}
	if !installed {
		// Install Network Observability Operator
		if err := r.installNetObservOperator(ctx); err != nil {
			klog.Warningf("Failed to install Network Observability Operator: %v. Will retry in %v.", err, requeueAfterOLM)
			return ctrl.Result{RequeueAfter: requeueAfterOLM}, nil
		}

		// Wait for Network Observability Operator to be ready
		klog.Info("Wait for Network Observability to be ready")
		if err := r.waitForNetObservOperator(ctx); err != nil {
			if err == context.DeadlineExceeded {
				klog.Warningf("Timed out waiting for Network Observability Operator to be ready after %v. Will retry in %v.", checkTimeout, requeueAfterOLM)
			} else {
				klog.Warningf("Failed waiting for Network Observability Operator: %v. Will retry in %v.", err, requeueAfterOLM)
			}
			return ctrl.Result{RequeueAfter: requeueAfterOLM}, nil
		}
	}

	// Check if FlowCollector already exists
	flowCollectorExists, err := r.isFlowCollectorExists(ctx)
	if err != nil {
		klog.Warningf("Failed to check if FlowCollector exists: %v. Will retry in %v.", err, requeueAfterStandard)
		return ctrl.Result{RequeueAfter: requeueAfterStandard}, nil
	}

	if !flowCollectorExists {
		// Create FlowCollector
		if err := r.createFlowCollector(ctx); err != nil {
			klog.Warningf("Failed to create FlowCollector: %v. Will retry in %v.", err, requeueAfterStandard)
			return ctrl.Result{RequeueAfter: requeueAfterStandard}, nil
		}
		klog.Info("FlowCollector created successfully")
	}

	// Mark as deployed to track deployment status
	// Skip this if configClient is nil (e.g., in tests)
	if r.configClient != nil {
		if err := r.markNetworkObservabilityDeployed(ctx); err != nil {
			klog.Warningf("Failed to mark Network Observability as deployed: %v. Will retry in %v.", err, requeueAfterStandard)
			return ctrl.Result{RequeueAfter: requeueAfterStandard}, nil
		}
	}

	klog.V(4).Info("Network Observability is deployed")
	r.status.SetNotDegraded(statusmanager.ObservabilityConfig)
	return ctrl.Result{}, nil
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

// wasNetworkObservabilityDeployed checks if the NetworkObservabilityDeployed condition is set to True
func (r *ReconcileObservability) wasNetworkObservabilityDeployed(ctx context.Context) (bool, error) {
	network, err := r.configClient.Networks().Get(ctx, FlowCollectorName, metav1.GetOptions{})
	if err != nil {
		return false, err
	}

	for _, condition := range network.Status.Conditions {
		if condition.Type == NetworkObservabilityDeployed {
			return condition.Status == metav1.ConditionTrue, nil
		}
	}

	return false, nil
}

// markNetworkObservabilityDeployed sets the NetworkObservabilityDeployed condition to True
func (r *ReconcileObservability) markNetworkObservabilityDeployed(ctx context.Context) error {
	// Use server-side apply to set the condition
	networkApplyConfig := applyconfigv1.Network(FlowCollectorName).
		WithStatus(applyconfigv1.NetworkStatus().
			WithConditions(applyconfigmetav1.Condition().
				WithType(NetworkObservabilityDeployed).
				WithStatus(metav1.ConditionTrue).
				WithReason("DeploymentComplete").
				WithMessage("Network Observability has been deployed").
				WithLastTransitionTime(metav1.Now())))

	_, err := r.configClient.Networks().ApplyStatus(ctx, networkApplyConfig, metav1.ApplyOptions{
		FieldManager: "network-observability-controller",
		Force:        true,
	})

	if err != nil {
		return fmt.Errorf("failed to mark Network Observability as deployed: %w", err)
	}

	klog.Info("Marked Network Observability as deployed")
	return nil
}

// shouldInstallNetworkObservability returns true if Network Observability should be installed.
// Valid values: "", "InstallAndEnable", "DoNotInstall"
// "DoNotInstall": skip installation (user opted out)
// "InstallAndEnable": install Network Observability (even on SNO clusters), always reinstall if missing
// "" or nil: install Network Observability once (opt-out model), except for SNO clusters, do not reinstall if already deployed
// SNO (Single Node OpenShift) clusters: skip installation by default unless explicitly set to "InstallAndEnable"
func (r *ReconcileObservability) shouldInstallNetworkObservability(ctx context.Context, network *configv1.Network) (bool, error) {
	// Check explicit value
	if network.Spec.NetworkObservability.InstallationPolicy != nil {
		value := *network.Spec.NetworkObservability.InstallationPolicy

		// Explicit disable
		if value == configv1.NetworkObservabilityDoNotInstall {
			return false, nil
		}

		// Explicit enable - install regardless of topology, always reinstall if missing
		if value == configv1.NetworkObservabilityInstallAndEnable {
			return true, nil
		}

		// Empty string falls through to default behavior
	}

	// Default behavior (nil or ""): install once, do not reinstall
	// Check if already deployed (skip this check if configClient is nil, e.g., in tests)
	if r.configClient != nil {
		deployed, err := r.wasNetworkObservabilityDeployed(ctx)
		if err != nil {
			return false, err
		}
		if deployed {
			// Already deployed, do not reinstall
			klog.V(4).Info("Network Observability already deployed (default policy), skipping reinstallation")
			return false, nil
		}
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

// isNetObservOperatorInstalled returns true if the flowcollector CRD exists
func (r *ReconcileObservability) isNetObservOperatorInstalled(ctx context.Context) (bool, error) {
	// Check if the FlowCollector CRD exists to determine if the operator is installed
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
