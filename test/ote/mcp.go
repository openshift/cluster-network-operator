package ote

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/gomega"

	machineconfigv1 "github.com/openshift/api/machineconfiguration/v1"
	machineconfigclient "github.com/openshift/client-go/machineconfiguration/clientset/versioned"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

// AwaitMCPRolloutStart waits for any MachineConfigPool to start updating
func AwaitMCPRolloutStart(ctx context.Context, client machineconfigclient.Interface, timeout time.Duration) {
	e2e.Logf("Waiting for MCP rollout to start (timeout: %v)", timeout)

	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	Eventually(func(g Gomega, ctx context.Context) {
		mcps, err := client.MachineconfigurationV1().MachineConfigPools().List(ctx, metav1.ListOptions{})
		g.Expect(err).NotTo(HaveOccurred())

		updating := false
		for _, mcp := range mcps.Items {
			for _, cond := range mcp.Status.Conditions {
				if cond.Type == "Updating" && cond.Status == corev1.ConditionTrue {
					updating = true
					e2e.Logf("MCP %s has started updating", mcp.Name)
					break
				}
			}
			if updating {
				break
			}
		}

		g.Expect(updating).To(BeTrue(), "no MCP has started updating yet")
	}).WithContext(pollCtx).WithPolling(10 * time.Second).Should(Succeed())
}

// AwaitAllMCPsComplete waits for all MachineConfigPools to finish updating
func AwaitAllMCPsComplete(ctx context.Context, client machineconfigclient.Interface, timeout time.Duration) {
	e2e.Logf("Waiting for MCP rollout to complete (timeout: %v)", timeout)

	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	Eventually(func(g Gomega, ctx context.Context) {
		mcps, err := client.MachineconfigurationV1().MachineConfigPools().List(ctx, metav1.ListOptions{})
		g.Expect(err).NotTo(HaveOccurred())

		for _, mcp := range mcps.Items {
			if mcp.Status.MachineCount == 0 {
				e2e.Logf("Skipping MCP %s (no machines)", mcp.Name)
				continue
			}

			updated := false
			updating := false
			for _, cond := range mcp.Status.Conditions {
				if cond.Type == "Updated" && cond.Status == corev1.ConditionTrue {
					updated = true
				}
				if cond.Type == "Updating" && cond.Status == corev1.ConditionTrue {
					updating = true
				}
			}

			g.Expect(updated && !updating).To(BeTrue(), "MCP %s not ready: Updated=%v Updating=%v (%d/%d machines)",
				mcp.Name, updated, updating, mcp.Status.ReadyMachineCount, mcp.Status.MachineCount)
		}
	}).WithContext(pollCtx).WithPolling(10 * time.Second).Should(Succeed())

	e2e.Logf("All MCPs completed successfully")
}

// GetMCPUpdatedTimestamps returns a map of MCP name to the LastTransitionTime of its Updated condition.
// Used as a baseline before making a configuration change to detect when the update completes.
func GetMCPUpdatedTimestamps(ctx context.Context, client machineconfigclient.Interface) (map[string]time.Time, error) {
	mcps, err := client.MachineconfigurationV1().MachineConfigPools().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list MCPs: %w", err)
	}

	timestamps := make(map[string]time.Time)
	for _, mcp := range mcps.Items {
		for _, cond := range mcp.Status.Conditions {
			if cond.Type == machineconfigv1.MachineConfigPoolUpdated && cond.Status == corev1.ConditionTrue {
				timestamps[mcp.Name] = cond.LastTransitionTime.Time
				break
			}
		}
	}
	return timestamps, nil
}

// AwaitAllMCPsUpdatedSince waits for all pools to report Updated=True with LastTransitionTime after the baseline.
// This avoids the race condition where a rollout completes before we start polling for Updating=True.
func AwaitAllMCPsUpdatedSince(ctx context.Context, client machineconfigclient.Interface, baselines map[string]time.Time, timeout time.Duration) {
	e2e.Logf("Waiting for MCP rollout to complete with newer timestamp (timeout: %v)", timeout)

	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	Eventually(func(g Gomega, ctx context.Context) {
		mcps, err := client.MachineconfigurationV1().MachineConfigPools().List(ctx, metav1.ListOptions{})
		g.Expect(err).NotTo(HaveOccurred(), "failed to list MCPs")

		for _, mcp := range mcps.Items {
			// Skip MCPs with no machines
			if mcp.Status.MachineCount == 0 {
				e2e.Logf("Skipping MCP %s (no machines)", mcp.Name)
				continue
			}

			baseTime, ok := baselines[mcp.Name]
			if !ok {
				continue
			}

			var updatedCond *machineconfigv1.MachineConfigPoolCondition
			var degradedCond *machineconfigv1.MachineConfigPoolCondition
			for i := range mcp.Status.Conditions {
				cond := &mcp.Status.Conditions[i]
				if cond.Type == machineconfigv1.MachineConfigPoolUpdated {
					updatedCond = cond
				}
				if cond.Type == machineconfigv1.MachineConfigPoolNodeDegraded {
					degradedCond = cond
				}
			}

			if degradedCond != nil {
				g.Expect(degradedCond.Status).NotTo(Equal(corev1.ConditionTrue),
					"MCP %s is degraded: %s", mcp.Name, degradedCond.Message)
			}

			g.Expect(updatedCond).NotTo(BeNil(), "MCP %s missing Updated condition", mcp.Name)
			g.Expect(updatedCond.Status).To(Equal(corev1.ConditionTrue), "MCP %s not yet updated", mcp.Name)
			g.Expect(updatedCond.LastTransitionTime.Time.After(baseTime)).To(BeTrue(),
				"MCP %s Updated timestamp %v is not after baseline %v", mcp.Name, updatedCond.LastTransitionTime.Time, baseTime)
		}
	}).WithContext(pollCtx).WithPolling(10 * time.Second).Should(Succeed())

	e2e.Logf("All MCPs completed successfully with newer timestamps")
}
