package ote

import (
	"context"
	"time"

	. "github.com/onsi/gomega"

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
