package observability

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
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

	checkInterval = 10 * time.Second
	checkTimeout  = 10 * time.Minute
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

type ReconcileObservability struct {
	client crclient.Client
	status *statusmanager.StatusManager
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
	enabled := network.Spec.ObservabilityEnabled
	if !enabled {
		return ctrl.Result{}, nil
	}

	// Now enable Network Observability
	installed, err := r.isNetObservOperatorInstalled(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !installed {
		// Create namespace if it doesn't exist
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: OperatorNamespace}}
		if err := r.client.Get(ctx, types.NamespacedName{Name: OperatorNamespace}, ns); err != nil {
			if errors.IsNotFound(err) {
				if err := r.client.Create(ctx, ns); err != nil {
					return ctrl.Result{}, err
				}
			} else {
				return ctrl.Result{}, err
			}
		}

		// Install Network Observability Operator
		if err := r.installNetObservOperator(ctx); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Wait for Network Observability Operator to be ready (whether just installed or already present)
	klog.Info("Wait for Network Observability to be ready")
	if err := r.waitForNetObservOperator(ctx); err != nil {
		if err == context.DeadlineExceeded {
			klog.Errorf("Timed out waiting for Network Observability Operator to be ready after %v. Stopping reconciliation.", checkTimeout)
			return ctrl.Result{RequeueAfter: 0}, nil // Don't requeue
		}
		return ctrl.Result{}, err
	}

	// Wait for OpenShift web console to be available
	klog.Info("Wait for OpenShift web console to be available")
	if err := r.waitForConsole(ctx); err != nil {
		if err == context.DeadlineExceeded {
			klog.Errorf("Timed out waiting for OpenShift web console to be available after %v. Stopping reconciliation.", checkTimeout)
			return ctrl.Result{RequeueAfter: 0}, nil // Don't requeue
		}
		return ctrl.Result{}, err
	}

	// Check if FlowCollector exists
	exists, err := r.isFlowCollectorExists(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	if exists {
		return ctrl.Result{}, nil
	}

	// Create FlowCollector
	if err := r.createFlowCollector(ctx); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// findObjectsByLabel returns all objects of a specific Group, Kind, and Namespace with the given label selector.
// Accepts one or more versions to try in order
func (r *ReconcileObservability) findObjectsByLabel(ctx context.Context, group, kind, namespace, labelKey, labelValue string, versions ...string) (*unstructured.UnstructuredList, error) {
	objects := &unstructured.UnstructuredList{}

	// Try each version until one matches
	for _, version := range versions {
		objects.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   group,
			Version: version,
			Kind:    kind,
		})

		listOptions := []crclient.ListOption{}
		if namespace != "" {
			listOptions = append(listOptions, crclient.InNamespace(namespace))
		}
		if labelKey != "" && labelValue != "" {
			listOptions = append(listOptions, crclient.MatchingLabels(map[string]string{labelKey: labelValue}))
		}

		if err := r.client.List(ctx, objects, listOptions...); err == nil {
			break // Success! This version matches
		}
	}

	return objects, nil
}

// listNetObservOperatorDeployments returns all deployments with the "app=netobserv-operator" label
func (r *ReconcileObservability) listNetObservOperatorDeployments(ctx context.Context) (*unstructured.UnstructuredList, error) {
	return r.findObjectsByLabel(ctx, "apps", "Deployment", "", "app", "netobserv-operator", "v1")
}

// isNetObservOperatorInstalled returns true if there exists a deployment with the "app=netobserv-operator" label
func (r *ReconcileObservability) isNetObservOperatorInstalled(ctx context.Context) (bool, error) {
	deployments, err := r.listNetObservOperatorDeployments(ctx)
	if err != nil {
		return false, err
	}

	return len(deployments.Items) > 0, nil
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
		deployments, err := r.listNetObservOperatorDeployments(ctx)
		if err != nil {
			return false, err
		}

		// Check if any deployment has available replicas
		for _, item := range deployments.Items {
			availableReplicas, found, err := unstructured.NestedInt64(item.Object, "status", "availableReplicas")
			if err != nil {
				return false, err
			}
			if !found {
				return false, nil
			}

			if availableReplicas > 0 {
				return true, nil
			}
		}
		return false, nil
	}
	return wait.PollUntilContextTimeout(ctx, checkInterval, checkTimeout, true, condition)
}

func (r *ReconcileObservability) waitForConsole(ctx context.Context) error {
	condition := func(ctx context.Context) (bool, error) {
		co := &unstructured.Unstructured{}
		co.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "config.openshift.io",
			Version: "v1",
			Kind:    "ClusterOperator", // used for health status
		})

		if err := r.client.Get(ctx, types.NamespacedName{Name: "console"}, co); err != nil {
			return false, crclient.IgnoreNotFound(err)
		}

		// Check if the object is valid before accessing its fields
		if co.Object == nil {
			return false, nil
		}

		conds, found, _ := unstructured.NestedSlice(co.Object, "status", "conditions")
		if !found {
			return false, nil
		}
		for _, c := range conds {
			if m, ok := c.(map[string]interface{}); ok {
				if m["type"] == "Available" && m["status"] == "True" {
					return true, nil
				}
			}
		}
		return false, nil
	}
	return wait.PollUntilContextTimeout(ctx, checkInterval, checkTimeout, true, condition)
}

// isFlowCollectorExists returns true if a FlowCollector instance exists
func (r *ReconcileObservability) isFlowCollectorExists(ctx context.Context) (bool, error) {
	flowCollectors, err := r.findObjectsByLabel(ctx, "flows.netobserv.io", "FlowCollector", "", "", "", FlowCollectorVersion)
	if err != nil {
		return false, err
	}

	return len(flowCollectors.Items) > 0, nil
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
