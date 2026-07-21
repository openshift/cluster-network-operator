// Package tls - Wait and polling functions for TLS profile compliance testing
package tls

import (
	"context"
	"fmt"
	"net"
	osexec "os/exec"
	"strings"
	"time"

	testclient "github.com/openshift/cluster-network-operator/test/e2e/client"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

// Deployment and DaemonSet helper functions
// These were originally in separate packages but consolidated here to follow the "minimal changes" golden rule

// GetDeploymentWithRetry retrieves a deployment with retry logic
// Refactored: Added ctx parameter for proper context propagation (was using context.Background())
func GetDeploymentWithRetry(cs *testclient.ClientSet, ctx context.Context, namespace, name string, interval, timeout time.Duration) (*appsv1.Deployment, error) {
	var dep *appsv1.Deployment
	err := wait.PollUntilContextTimeout(ctx, interval, timeout, true, func(ctx context.Context) (bool, error) {
		var err error
		dep, err = cs.AppsV1Interface.Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
	return dep, err
}

// WaitForDeploymentSetReady waits for a deployment to be ready (all replicas ready)
// Refactored: Added ctx parameter for proper context propagation (was using context.Background())
func WaitForDeploymentSetReady(cs *testclient.ClientSet, ctx context.Context, dep *appsv1.Deployment, interval, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, interval, timeout, true, func(ctx context.Context) (bool, error) {
		current, err := cs.AppsV1Interface.Deployments(dep.Namespace).Get(ctx, dep.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if current.Status.ReadyReplicas == *current.Spec.Replicas {
			return true, nil
		}
		return false, nil
	})
}

// GetDaemonSetWithRetry retrieves a daemonset with retry logic
// Refactored: Added ctx parameter for proper context propagation (was using context.Background())
func GetDaemonSetWithRetry(cs *testclient.ClientSet, ctx context.Context, namespace, name string, interval, timeout time.Duration) (*appsv1.DaemonSet, error) {
	var ds *appsv1.DaemonSet
	err := wait.PollUntilContextTimeout(ctx, interval, timeout, true, func(ctx context.Context) (bool, error) {
		var err error
		ds, err = cs.AppsV1Interface.DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
	return ds, err
}

// WaitForDaemonSetReady waits for a daemonset to be ready (all desired pods ready)
// Refactored: Added ctx parameter for proper context propagation (was using context.Background())
func WaitForDaemonSetReady(cs *testclient.ClientSet, ctx context.Context, ds *appsv1.DaemonSet, interval, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, interval, timeout, true, func(ctx context.Context) (bool, error) {
		current, err := cs.AppsV1Interface.DaemonSets(ds.Namespace).Get(ctx, ds.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if current.Status.NumberReady == current.Status.DesiredNumberScheduled {
			return true, nil
		}
		return false, nil
	})
}

// getMCPConditionStatus retrieves a specific condition status from a Machine Config Pool
func getMCPConditionStatus(poolName, conditionType string) (string, error) {
	cmd := osexec.Command("bash", "-c",
		fmt.Sprintf("oc get mcp %s -o jsonpath='{.status.conditions[?(@.type==\"%s\")].status}'",
			poolName, conditionType))
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// WaitForMachineConfigPoolsReady waits for all Machine Config Pools to complete updates
// This is critical after TLS profile changes as it triggers node reboots
func WaitForMachineConfigPoolsReady(cs *testclient.ClientSet, ctx context.Context) error {
	return RetryWithLogging(ctx, "Waiting for Machine Config Pools to update (nodes may reboot)",
		30*time.Second, MCPReadyTimeout,
		func() (bool, string, error) {
			// First, try to get MCPs to see if they exist
			cmd := osexec.Command("oc", "get", "mcp", "-o", "json")
			_, err := cmd.CombinedOutput()
			if err != nil {
				return false, fmt.Sprintf("Cannot get MCPs: %v", err), nil
			}

			// Get status for both master and worker pools
			masterUpdating, _ := getMCPConditionStatus("master", "Updating")
			masterUpdated, _ := getMCPConditionStatus("master", "Updated")
			workerUpdating, _ := getMCPConditionStatus("worker", "Updating")
			workerUpdated, _ := getMCPConditionStatus("worker", "Updated")

			masterUpdateBool := masterUpdating == "True"
			workerUpdateBool := workerUpdating == "True"
			masterUpdatedBool := masterUpdated == "True"
			workerUpdatedBool := workerUpdated == "True"

			isUpdating := masterUpdateBool || workerUpdateBool

			// Both MCPs should be Updated and not Updating
			if !isUpdating && masterUpdatedBool && workerUpdatedBool {
				return true, "Machine Config Pools are ready", nil
			}

			return false, fmt.Sprintf("Master: Updating=%v, Updated=%v | Worker: Updating=%v, Updated=%v",
				masterUpdateBool, masterUpdatedBool, workerUpdateBool, workerUpdatedBool), nil
		})
}

// WaitForAllNodesReady waits for all nodes to be Ready and schedulable (not SchedulingDisabled)
// DEPRECATED: Use WaitForAllNodesReadyAndSchedulable from stability.go instead
// That function includes OpenShift-pattern taint checking and better node state validation
// Keeping this for backwards compatibility, but redirects to the new implementation
func WaitForAllNodesReady(cs *testclient.ClientSet, ctx context.Context) error {
	return WaitForAllNodesReadyAndSchedulable(cs, ctx, 25*time.Minute)
}

// WaitForOperatorRestart waits for the operator to restart after TLS profile change
// This includes waiting for MCO updates, node reboots, and operator pod restarts
func WaitForOperatorRestart(cs *testclient.ClientSet, ctx context.Context, operatorNS string) error {
	// Step 1: Wait for Machine Config Pools to update (this triggers node reboots)
	if err := WaitForMachineConfigPoolsReady(cs, ctx); err != nil {
		return fmt.Errorf("MCP readiness check failed: %v", err)
	}

	// Step 2: Wait for operator deployment to be ready with ObservedGeneration check
	LogStep("Waiting for operator deployment to be ready with spec changes applied...")
	dep, err := GetDeploymentWithRetry(cs, ctx, operatorNS, DeploymentName, 5*time.Second, OperatorReadyTimeout)
	if err != nil {
		return fmt.Errorf("failed to get deployment: %v", err)
	}

	// Wait for deployment ready + ObservedGeneration check (ensures spec changes are applied)
	err = RetryWithLogging(ctx, "Waiting for deployment ObservedGeneration and replicas ready",
		5*time.Second, OperatorReadyTimeout,
		func() (bool, string, error) {
			err := cs.Get(ctx, types.NamespacedName{Name: dep.Name, Namespace: dep.Namespace}, dep)
			if err != nil {
				if errors.IsNotFound(err) {
					return false, "Deployment not found", nil
				}
				return false, "", err
			}

			// CRITICAL: Check ObservedGeneration to ensure spec changes are applied
			if dep.Status.ObservedGeneration != dep.Generation {
				return false, fmt.Sprintf("ObservedGeneration=%d, Generation=%d (spec not applied yet)",
					dep.Status.ObservedGeneration, dep.Generation), nil
			}

			if *dep.Spec.Replicas != dep.Status.ReadyReplicas {
				return false, fmt.Sprintf("Ready replicas: %d/%d", dep.Status.ReadyReplicas, *dep.Spec.Replicas), nil
			}

			return true, "", nil
		})
	if err != nil {
		return fmt.Errorf("deployment did not become ready: %v", err)
	}
	LogStep("  Deployment is ready with spec changes applied")

	// Step 3: Wait for DaemonSet pods to be ready with ObservedGeneration check
	LogStep("Waiting for DaemonSet to be ready with spec changes applied...")
	ds, err := GetDaemonSetWithRetry(cs, ctx, operatorNS, NetworkMetricsDaemonSetName, 5*time.Second, OperatorReadyTimeout)
	if err != nil {
		return fmt.Errorf("failed to get daemonset: %v", err)
	}

	// Wait for daemonset ready + ObservedGeneration check (ensures spec changes are applied)
	err = RetryWithLogging(ctx, "Waiting for DaemonSet ObservedGeneration and pods ready",
		5*time.Second, OperatorReadyTimeout,
		func() (bool, string, error) {
			err := cs.Get(ctx, types.NamespacedName{Name: ds.Name, Namespace: ds.Namespace}, ds)
			if err != nil {
				if errors.IsNotFound(err) {
					return false, "DaemonSet not found", nil
				}
				return false, "", err
			}

			// CRITICAL: Check ObservedGeneration to ensure spec changes are applied
			if ds.Status.ObservedGeneration != ds.Generation {
				return false, fmt.Sprintf("ObservedGeneration=%d, Generation=%d (spec not applied yet)",
					ds.Status.ObservedGeneration, ds.Generation), nil
			}

			if ds.Status.DesiredNumberScheduled != ds.Status.NumberReady {
				return false, fmt.Sprintf("Ready pods: %d/%d", ds.Status.NumberReady, ds.Status.DesiredNumberScheduled), nil
			}

			return true, "", nil
		})
	if err != nil {
		return fmt.Errorf("DaemonSet did not become ready: %v", err)
	}
	LogStep("  DaemonSet is ready with spec changes applied")

	// Step 4: Verify all pods in namespace are healthy (no stuck pods)
	LogStep("Verifying all pods are healthy...")
	if err := VerifyPodsHealthy(cs, ctx, operatorNS); err != nil {
		return fmt.Errorf("pod health verification failed: %v", err)
	}

	// Step 5 & 6: Wait for full cluster stability (operators + nodes)
	// Using OpenShift-pattern stability checking from stability.go
	// This prevents race conditions where MCO cordons nodes after operators finish
	if err := WaitForClusterStability(cs, ctx, ClusterOperatorReadyTimeout, 5*time.Minute); err != nil {
		return fmt.Errorf("cluster stability check failed: %v", err)
	}

	// Note: We no longer pre-check TLS endpoints with openssl
	// The scanner (tls-scanner tool) handles retries and endpoint readiness checks automatically

	LogStep("Operator restart complete - all components ready")
	return nil
}

// ForceOperatorPodRestart deletes all pods in the operator namespace to force fresh restart
// This eliminates accumulated restart counts and ensures we're testing against stable pods
func ForceOperatorPodRestart(cs *testclient.ClientSet, ctx context.Context, operatorNS string) error {
	// CRITICAL: Re-verify cluster stability before deleting pods
	// MCO can start additional update waves after cluster operators finish
	// If we delete pods while MCO is cordoning nodes, pods may fail to restart
	// Using OpenShift-pattern node checking from stability.go
	LogStep("  Pre-flight check: Verifying all nodes still schedulable before pod restart...")
	if err := WaitForAllNodesReadyAndSchedulable(cs, ctx, 5*time.Minute); err != nil {
		return fmt.Errorf("nodes became unschedulable before pod restart (likely MCO started another update wave): %v", err)
	}

	LogStep("  Deleting all pods in operator namespace to force fresh restart...")

	// Get all pods in the operator namespace
	pods, err := cs.CoreV1Interface.Pods(operatorNS).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list pods: %v", err)
	}

	if len(pods.Items) == 0 {
		LogStep("  No pods found to delete")
		return nil
	}

	// Delete all pods
	deleteCount := 0
	for _, pod := range pods.Items {
		LogStep(fmt.Sprintf("  Deleting pod: %s", pod.Name))
		err := cs.CoreV1Interface.Pods(operatorNS).Delete(ctx, pod.Name, metav1.DeleteOptions{})
		if err != nil {
			LogStep(fmt.Sprintf("  Warning: Failed to delete pod %s: %v", pod.Name, err))
		} else {
			deleteCount++
		}
	}

	LogStep(fmt.Sprintf("  Deleted %d pods, waiting for fresh pods to be created...", deleteCount))

	// Wait for all pods to be deleted
	err = RetryWithLogging(ctx, "Waiting for old pods to terminate",
		2*time.Second, 2*time.Minute,
		func() (bool, string, error) {
			pods, err := cs.CoreV1Interface.Pods(operatorNS).List(ctx, metav1.ListOptions{})
			if err != nil {
				return false, fmt.Sprintf("Failed to list pods: %v", err), nil
			}

			// Check if any of the old pods still exist
			terminatingCount := 0
			for _, pod := range pods.Items {
				if pod.DeletionTimestamp != nil {
					terminatingCount++
				}
			}

			if terminatingCount > 0 {
				return false, fmt.Sprintf("%d pod(s) still terminating", terminatingCount), nil
			}

			return true, "All old pods terminated", nil
		})

	if err != nil {
		return fmt.Errorf("pods did not terminate: %v", err)
	}

	// Wait for new pods to be created and ready
	LogStep("  Waiting for new pods to be created and ready...")

	// Wait for deployment to be ready (using standard ready check, no need for generation check on restart)
	dep, err := GetDeploymentWithRetry(cs, ctx, operatorNS, DeploymentName, 5*time.Second, OperatorReadyTimeout)
	if err != nil {
		return fmt.Errorf("failed to get deployment after restart: %v", err)
	}

	err = WaitForDeploymentSetReady(cs, ctx, dep, 5*time.Second, OperatorReadyTimeout)
	if err != nil {
		return fmt.Errorf("deployment did not become ready after pod restart: %v", err)
	}
	LogStep("  Deployment is ready")

	// Wait for DaemonSet to be ready (using standard ready check, no need for generation check on restart)
	ds, err := GetDaemonSetWithRetry(cs, ctx, operatorNS, NetworkMetricsDaemonSetName, 5*time.Second, OperatorReadyTimeout)
	if err != nil {
		return fmt.Errorf("failed to get daemonset after restart: %v", err)
	}

	err = WaitForDaemonSetReady(cs, ctx, ds, 5*time.Second, OperatorReadyTimeout)
	if err != nil {
		return fmt.Errorf("DaemonSet did not become ready after pod restart: %v", err)
	}
	LogStep("  DaemonSet is ready")

	// Verify all pods have 0 restarts (fresh start)
	LogStep("  Verifying all pods have fresh start (0 restarts)...")
	pods, err = cs.CoreV1Interface.Pods(operatorNS).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list pods after restart: %v", err)
	}

	for _, pod := range pods.Items {
		restartCount := getPodRestartCount(&pod)
		LogStep(fmt.Sprintf("  Pod %s: %d restarts", pod.Name, restartCount))
	}

	LogStep("  All pods restarted successfully with fresh state")
	return nil
}

// VerifyPodsHealthy verifies that pods in the operator namespace are healthy and running.
// This detects stuck pods (ContainerCreating > 5min).
// We rely on observedGeneration checks to ensure specs were applied, and TLS scanner to verify correct config.
func VerifyPodsHealthy(cs *testclient.ClientSet, ctx context.Context, operatorNS string) error {
	// Get all pods in the operator namespace
	pods, err := cs.CoreV1Interface.Pods(operatorNS).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list pods: %v", err)
	}

	if len(pods.Items) == 0 {
		return fmt.Errorf("no pods found in namespace %s", operatorNS)
	}

	var errMsgs []string
	var stuckPods []string

	for _, pod := range pods.Items {
		podAge := time.Since(pod.CreationTimestamp.Time)

		// Check: Detect pods stuck in ContainerCreating for more than 5 minutes
		if pod.Status.Phase == "Pending" {
			// Check if pod is scheduled but containers not created
			scheduled := false
			for _, condition := range pod.Status.Conditions {
				if condition.Type == "PodScheduled" && condition.Status == "True" {
					scheduled = true
					break
				}
			}
			if scheduled && podAge > 5*time.Minute {
				stuckPods = append(stuckPods, fmt.Sprintf("  - %s: stuck in %s for %s (restarts: %d)",
					pod.Name, pod.Status.Phase, podAge.Round(time.Second), getPodRestartCount(&pod)))
			}
		}
	}

	// Report findings
	if len(stuckPods) > 0 {
		LogStep(fmt.Sprintf("  ERROR: Found %d pods stuck in ContainerCreating:", len(stuckPods)))
		for _, msg := range stuckPods {
			LogStep(msg)
		}
		errMsgs = append(errMsgs, fmt.Sprintf("%d pods stuck in ContainerCreating", len(stuckPods)))
	}

	if len(errMsgs) > 0 {
		return fmt.Errorf("pod health verification failed: %s", strings.Join(errMsgs, "; "))
	}

	LogStep(fmt.Sprintf("  All %d pods are healthy and running", len(pods.Items)))
	return nil
}

// getPodRestartCount returns the total restart count across all containers in a pod
func getPodRestartCount(pod *corev1.Pod) int32 {
	var totalRestarts int32
	for _, containerStatus := range pod.Status.ContainerStatuses {
		totalRestarts += containerStatus.RestartCount
	}
	return totalRestarts
}

// VerifyMetricsEndpointReady verifies that the DaemonSet metrics endpoint is actually accessible
// This ensures kube-rbac-proxy has finished binding to port 9301 before scanning begins
func VerifyMetricsEndpointReady(cs *testclient.ClientSet, ctx context.Context, operatorNS string, timeout time.Duration) error {
	return RetryWithLogging(ctx, "Verifying DaemonSet metrics endpoint is accessible",
		2*time.Second, timeout,
		func() (bool, string, error) {
			// Get first DaemonSet pod IP
			pods, err := cs.CoreV1Interface.Pods(operatorNS).List(ctx, metav1.ListOptions{
				LabelSelector: DaemonSetLabelSelector,
			})
			if err != nil || len(pods.Items) == 0 {
				return false, "No DaemonSet pods found", nil
			}

			podIP := pods.Items[0].Status.PodIP
			if podIP == "" {
				return false, "Pod has no IP yet", nil
			}

			// Try to connect to port 9301 with TLS 1.2
			// This verifies kube-rbac-proxy is listening and accepting TLS connections
			endpoint := net.JoinHostPort(podIP, "9301")
			cmd := osexec.Command("timeout", "5", "bash", "-c",
				fmt.Sprintf("echo | openssl s_client -connect %s -tls1_2 2>&1 | grep -q CONNECTED", endpoint))
			err = cmd.Run()
			if err != nil {
				return false, fmt.Sprintf("Endpoint %s not ready (kube-rbac-proxy still starting)", endpoint), nil
			}

			return true, fmt.Sprintf("Metrics endpoint %s is accessible", endpoint), nil
		})
}

// WaitForOperatorPodsOnly waits for operator deployment and daemonset pods only
// Used for scenarios that only change operator-level settings (not cluster-wide)
// Does NOT wait for cluster operators or nodes - only the operator's own components
func WaitForOperatorPodsOnly(cs *testclient.ClientSet, ctx context.Context, operatorNS string) error {
	LogStep("Step 1: Waiting for operator pods to apply configuration changes")

	// Step 1: Wait for operator deployment to be ready with ObservedGeneration check
	LogStep("  Waiting for operator deployment to be ready with spec changes applied...")
	dep, err := GetDeploymentWithRetry(cs, ctx, operatorNS, DeploymentName, 10*time.Second, OperatorReadyTimeout)
	if err != nil {
		return fmt.Errorf("failed to get deployment: %v", err)
	}

	err = RetryWithLogging(ctx, "Waiting for deployment ObservedGeneration and replicas ready",
		10*time.Second, OperatorReadyTimeout,
		func() (bool, string, error) {
			err := cs.Get(ctx, types.NamespacedName{Name: dep.Name, Namespace: dep.Namespace}, dep)
			if err != nil {
				if errors.IsNotFound(err) {
					return false, "Deployment not found", nil
				}
				return false, "", err
			}

			// CRITICAL: Check ObservedGeneration to ensure spec changes are applied
			if dep.Status.ObservedGeneration != dep.Generation {
				return false, fmt.Sprintf("ObservedGeneration=%d, Generation=%d",
					dep.Status.ObservedGeneration, dep.Generation), nil
			}

			if *dep.Spec.Replicas != dep.Status.ReadyReplicas {
				return false, fmt.Sprintf("Ready: %d/%d", dep.Status.ReadyReplicas, *dep.Spec.Replicas), nil
			}

			return true, "", nil
		})
	if err != nil {
		return fmt.Errorf("operator deployment did not become ready: %v", err)
	}
	LogStep("  Operator deployment is ready")

	// Step 2: Wait for DaemonSet to be ready with ObservedGeneration check
	LogStep("  Waiting for DaemonSet to be ready with spec changes applied...")
	ds, err := GetDaemonSetWithRetry(cs, ctx, operatorNS, NetworkMetricsDaemonSetName, 10*time.Second, OperatorReadyTimeout)
	if err != nil {
		return fmt.Errorf("failed to get daemonset: %v", err)
	}

	err = RetryWithLogging(ctx, "Waiting for DaemonSet ObservedGeneration and pods ready",
		10*time.Second, OperatorReadyTimeout,
		func() (bool, string, error) {
			err := cs.Get(ctx, types.NamespacedName{Name: ds.Name, Namespace: ds.Namespace}, ds)
			if err != nil {
				if errors.IsNotFound(err) {
					return false, "DaemonSet not found", nil
				}
				return false, "", err
			}

			// CRITICAL: Check ObservedGeneration to ensure spec changes are applied
			if ds.Status.ObservedGeneration != ds.Generation {
				return false, fmt.Sprintf("ObservedGeneration=%d, Generation=%d",
					ds.Status.ObservedGeneration, ds.Generation), nil
			}

			if ds.Status.DesiredNumberScheduled != ds.Status.NumberReady {
				return false, fmt.Sprintf("Ready: %d/%d", ds.Status.NumberReady, ds.Status.DesiredNumberScheduled), nil
			}

			return true, "", nil
		})
	if err != nil {
		return fmt.Errorf("DaemonSet did not become ready: %v", err)
	}
	LogStep("  DaemonSet is ready")

	// Step 3: Wait for all pods to be healthy
	err = RetryWithLogging(ctx, "Waiting for all operator pods to be healthy",
		10*time.Second, OperatorReadyTimeout,
		func() (bool, string, error) {
			pods, err := cs.CoreV1Interface.Pods(operatorNS).List(ctx, metav1.ListOptions{})
			if err != nil {
				return false, fmt.Sprintf("Failed to list pods: %v", err), nil
			}

			totalPods := len(pods.Items)
			healthyPods := 0
			for _, pod := range pods.Items {
				if pod.Status.Phase == corev1.PodRunning {
					healthyPods++
				}
			}

			if healthyPods == totalPods && totalPods > 0 {
				return true, fmt.Sprintf("All operator pods healthy: %d/%d running", healthyPods, totalPods), nil
			}

			return false, fmt.Sprintf("Pods not all healthy: %d/%d running", healthyPods, totalPods), nil
		})
	if err != nil {
		return fmt.Errorf("operator pods did not become healthy: %v", err)
	}

	LogStep("Operator pods ready")
	return nil
}
