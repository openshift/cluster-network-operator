// Package tls - OpenShift-style cluster stability checking utilities
// Based on battle-tested patterns from openshift/origin test/e2e/upgrade
package tls

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	testclient "github.com/openshift/cluster-network-operator/test/e2e/client"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

// WaitForAllNodesReadyAndSchedulable waits for all nodes to be Ready AND Schedulable
// This prevents race conditions where MCO cordons nodes mid-operation
// Based on openshift/origin test/e2e/upgrade patterns
func WaitForAllNodesReadyAndSchedulable(cs *testclient.ClientSet, ctx context.Context, timeout time.Duration) error {
	LogStep("Waiting for all nodes to be Ready and schedulable...")

	return wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		nodes, err := cs.CoreV1Interface.Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			LogStep(fmt.Sprintf("  Error listing nodes: %v (retrying)", err))
			return false, nil // Retry on error
		}

		if len(nodes.Items) == 0 {
			LogStep("  No nodes found (retrying)")
			return false, nil
		}

		var notReadyNodes []string
		var unschedulableNodes []string
		readyAndSchedulableCount := 0

		for _, node := range nodes.Items {
			// Check 1: Node must be Ready
			ready := false
			for _, condition := range node.Status.Conditions {
				if condition.Type == corev1.NodeReady {
					ready = (condition.Status == corev1.ConditionTrue)
					break
				}
			}

			if !ready {
				notReadyNodes = append(notReadyNodes, node.Name)
				continue
			}

			// Check 2: Node must be Schedulable (not cordoned)
			// OpenShift pattern: check spec.unschedulable and taints
			if node.Spec.Unschedulable {
				unschedulableNodes = append(unschedulableNodes, fmt.Sprintf("%s (cordoned)", node.Name))
				continue
			}

			// Check 3: No uninitialized taints (from openshift/origin PR #30377)
			// Taints like node.kubernetes.io/unschedulable indicate temporary node maintenance
			hasBlockingTaint := false
			for _, taint := range node.Spec.Taints {
				// Skip master taint - it's expected and permanent
				if taint.Key == "node-role.kubernetes.io/master" {
					continue
				}
				// Block on NoSchedule or NoExecute taints that indicate node is not ready
				if taint.Effect == corev1.TaintEffectNoSchedule || taint.Effect == corev1.TaintEffectNoExecute {
					if taint.Key == "node.kubernetes.io/unschedulable" ||
						taint.Key == "node.kubernetes.io/not-ready" ||
						taint.Key == "node.kubernetes.io/unreachable" ||
						taint.Key == "node.kubernetes.io/disk-pressure" ||
						taint.Key == "node.kubernetes.io/memory-pressure" ||
						taint.Key == "node.kubernetes.io/pid-pressure" ||
						taint.Key == "node.kubernetes.io/network-unavailable" {
						hasBlockingTaint = true
						unschedulableNodes = append(unschedulableNodes, fmt.Sprintf("%s (taint:%s)", node.Name, taint.Key))
						break
					}
				}
			}

			if hasBlockingTaint {
				continue
			}

			// Node passed all checks
			readyAndSchedulableCount++
		}

		totalNodes := len(nodes.Items)

		// Log progress
		if readyAndSchedulableCount < totalNodes {
			var reasons []string
			if len(notReadyNodes) > 0 {
				reasons = append(reasons, fmt.Sprintf("%d not ready: %v", len(notReadyNodes), notReadyNodes))
			}
			if len(unschedulableNodes) > 0 {
				reasons = append(reasons, fmt.Sprintf("%d unschedulable: %v", len(unschedulableNodes), unschedulableNodes))
			}
			LogStep(fmt.Sprintf("  Nodes: %d total, %d ready+schedulable (%s)",
				totalNodes, readyAndSchedulableCount, reasons))
			return false, nil
		}

		// All nodes are ready and schedulable
		LogStep(fmt.Sprintf("  All %d nodes are Ready and schedulable", totalNodes))
		return true, nil
	})
}

// WaitForClusterOperatorsStable waits for all cluster operators to stop progressing
// Based on openshift/origin upgrade monitoring patterns
// Uses oc CLI since ConfigV1Interface is not available in the test client
func WaitForClusterOperatorsStable(cs *testclient.ClientSet, ctx context.Context, timeout time.Duration) error {
	LogStep("Waiting for all cluster operators to stop progressing...")

	return wait.PollUntilContextTimeout(ctx, 30*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		// Get cluster operators with Progressing != False
		// Based on openshift/origin patterns: check column 4 (Progressing)
		cmd := exec.Command("bash", "-c", "oc get co --no-headers | awk '{if ($4 != \"False\") print $0}'")
		progressingOutput, err := cmd.CombinedOutput()
		if err != nil {
			LogStep(fmt.Sprintf("  Error checking cluster operators: %v (retrying)", err))
			return false, nil // Retry on error
		}

		// Parse output to get list of progressing operators
		progressingList := strings.TrimSpace(string(progressingOutput))

		if progressingList != "" {
			// Count how many operators are progressing
			lines := strings.Split(progressingList, "\n")
			LogStep(fmt.Sprintf("  %d cluster operators still progressing:", len(lines)))

			// Log details (limit to first 10 for readability)
			maxDisplay := 10
			for i, line := range lines {
				if i >= maxDisplay {
					LogStep(fmt.Sprintf("    ... and %d more", len(lines)-maxDisplay))
					break
				}
				// Parse operator name (first column)
				fields := strings.Fields(line)
				if len(fields) > 0 {
					LogStep(fmt.Sprintf("    - %s", fields[0]))
				}
			}
			return false, nil
		}

		// All operators have Progressing=False
		LogStep("  All cluster operators stopped progressing")
		return true, nil
	})
}

// WaitForClusterStability waits for both cluster operators AND nodes to be stable
// This is the comprehensive check to use before critical operations like pod restarts
// Based on openshift/origin upgrade patterns: operators → nodes
func WaitForClusterStability(cs *testclient.ClientSet, ctx context.Context, operatorTimeout, nodeTimeout time.Duration) error {
	LogStep("Waiting for cluster stability (operators + nodes)...")

	// Step 1: Wait for cluster operators to stop progressing
	if err := WaitForClusterOperatorsStable(cs, ctx, operatorTimeout); err != nil {
		return fmt.Errorf("cluster operators did not stabilize: %v", err)
	}

	// Step 2: Wait for all nodes to be ready and schedulable
	// This catches MCO starting a second update wave after operators finish
	if err := WaitForAllNodesReadyAndSchedulable(cs, ctx, nodeTimeout); err != nil {
		return fmt.Errorf("nodes not ready/schedulable after operators stabilized: %v", err)
	}

	LogStep("Cluster is stable (operators + nodes)")
	return nil
}
