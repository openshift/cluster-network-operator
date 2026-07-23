// Package tls provides TLS profile compliance testing utilities for the
// Ingress Node Firewall operator. It includes endpoint scanning, profile
// verification, and integration test helpers for validating TLS configurations
// across different cluster TLS profiles (Baseline, Modern, Custom).
package tls

import (
	"context"
	"encoding/json"
	"fmt"
	osexec "os/exec"
	"strings"
	"time"

	testclient "github.com/openshift/cluster-network-operator/test/e2e/client"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

// TLS Scanner result structures
type TLSScanResult struct {
	ScanTime  string         `json:"scanTime"`
	IPResults []IPScanResult `json:"ip_results"`
	Summary   ScanSummary    `json:"summary"`
}

type IPScanResult struct {
	IP          string           `json:"ip"`
	Hostname    string           `json:"hostname"`
	Namespace   string           `json:"namespace"`
	PodName     string           `json:"pod_name"`
	PortResults []PortScanResult `json:"port_results"`
}

type PortScanResult struct {
	Port          int      `json:"port"`
	ListenAddress string   `json:"listen_address"`
	Service       string   `json:"service"`
	TLSVersions   []string `json:"tls_versions"`
	TLSCiphers    []string `json:"tls_ciphers"`
	Status        string   `json:"status"`
}

type ScanSummary struct {
	TotalIPs         int     `json:"total_ips"`
	TotalPorts       int     `json:"total_ports"`
	PortsOK          int     `json:"ports_ok"`
	PortsSkipped     int     `json:"ports_skipped"`
	TotalDurationSec float64 `json:"total_duration_sec"`
}

// Helper Functions

// RetryWithLogging retries a condition check with progress logging
// This consolidates 16+ duplicate polling patterns across TLS test files
//
// The conditionFn should return:
//   - done: true if condition is met, false otherwise
//   - reason: string describing current state (logged if not done, empty to skip logging)
//   - err: error if check failed (stops polling), nil otherwise
//
// Example usage:
//
//	err := RetryWithLogging(ctx, "Waiting for deployment to be ready",
//	    5*time.Second, 2*time.Minute,
//	    func() (bool, string, error) {
//	        dep, err := cs.Get(...)
//	        if err != nil {
//	            return false, "", err
//	        }
//	        if dep.Status.ReadyReplicas != *dep.Spec.Replicas {
//	            return false, fmt.Sprintf("Ready %d/%d", dep.Status.ReadyReplicas, *dep.Spec.Replicas), nil
//	        }
//	        return true, "", nil
//	    })
func RetryWithLogging(
	ctx context.Context,
	description string,
	interval time.Duration,
	timeout time.Duration,
	conditionFn func() (done bool, reason string, err error),
) error {
	startTime := time.Now()

	err := wait.PollUntilContextTimeout(ctx, interval, timeout, true, func(ctx context.Context) (bool, error) {
		done, reason, err := conditionFn()
		if err != nil {
			return false, err
		}
		if !done {
			if reason != "" {
				elapsed := time.Since(startTime).Round(time.Second)
				LogStep(fmt.Sprintf("  %s (elapsed: %s)", reason, elapsed))
			}
			return false, nil
		}
		return true, nil
	})

	if err != nil {
		return fmt.Errorf("%s failed after %s: %v", description, time.Since(startTime).Round(time.Second), err)
	}

	return nil
}

// getPodLogs retrieves logs from a pod with optional tail limit
func getPodLogs(cs *testclient.ClientSet, ctx context.Context, namespace, podName string, tailLines *int64) (string, error) {
	opts := &corev1.PodLogOptions{}
	if tailLines != nil {
		opts.TailLines = tailLines
	}
	logs, err := cs.CoreV1Interface.Pods(namespace).GetLogs(podName, opts).DoRaw(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get pod logs: %v", err)
	}
	return string(logs), nil
}

// getDaemonSetPodIP retrieves the IP of the first DaemonSet pod
func getDaemonSetPodIP(cs *testclient.ClientSet, ctx context.Context, operatorNS string) (string, error) {
	pods, err := cs.CoreV1Interface.Pods(operatorNS).List(ctx, metav1.ListOptions{
		LabelSelector: DaemonSetLabelSelector,
	})
	if err != nil || len(pods.Items) == 0 {
		return "", fmt.Errorf("failed to find DaemonSet pods: %v", err)
	}

	podIP := pods.Items[0].Status.PodIP
	if podIP == "" {
		return "", fmt.Errorf("DaemonSet pod has no IP")
	}

	return podIP, nil
}

// analyzeTLSScanResults parses scanner JSON output and checks if TLS version is supported
// on ALL discovered TLS endpoints. The scanner dynamically discovers all pods and ports in
// the namespace, so we test whatever it finds without hardcoded port expectations.
func analyzeTLSScanResults(output []byte, tlsVersion string, namespace string) (bool, string, error) {
	var results TLSScanResult
	if err := json.Unmarshal(output, &results); err != nil {
		return false, "", fmt.Errorf("failed to parse scan results: %v", err)
	}

	targetVersion := formatTLSVersion(tlsVersion)
	foundEndpoints := 0
	passedEndpoints := 0
	var details []string

	// Check TLS version support on ALL discovered endpoints
	// Scanner already filtered to only TLS endpoints (--all-pods discovers everything)
	for _, ipResult := range results.IPResults {
		for _, portResult := range ipResult.PortResults {
			foundEndpoints++

			// Check if target TLS version is in the supported list
			supported := false
			for _, version := range portResult.TLSVersions {
				if version == targetVersion {
					supported = true
					passedEndpoints++
					break
				}
			}

			status := "FAIL"
			if supported {
				status = "PASS"
			}
			details = append(details,
				fmt.Sprintf("  %s - Pod %s Port %d - Versions: %v",
					status, ipResult.PodName, portResult.Port, portResult.TLSVersions))
		}
	}

	// If no TLS endpoints discovered, that's an error
	if foundEndpoints == 0 {
		return false, "", fmt.Errorf("scanner found no TLS endpoints in namespace %s (scanned %d IPs, %d total ports)",
			namespace, results.Summary.TotalIPs, results.Summary.TotalPorts)
	}

	// Return overall result with detailed per-pod, per-port report
	if passedEndpoints == foundEndpoints {
		return true, fmt.Sprintf("TLS %s supported on all %d endpoints in %s:\n%s",
			tlsVersion, foundEndpoints, namespace, strings.Join(details, "\n")), nil
	} else {
		return false, fmt.Sprintf("TLS %s only supported on %d/%d endpoints in %s:\n%s",
			tlsVersion, passedEndpoints, foundEndpoints, namespace, strings.Join(details, "\n")), nil
	}
}

// grantScannerPrivileges grants cluster-admin and privileged SCC to scanner namespace
func grantScannerPrivileges(namespace string) error {
	cmd := osexec.Command("oc", "adm", "policy", "add-cluster-role-to-user",
		"cluster-admin", "-z", "default", "-n", namespace)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to grant cluster-admin: %v, output: %s", err, string(output))
	}

	cmd = osexec.Command("oc", "adm", "policy", "add-scc-to-user",
		"privileged", "-z", "default", "-n", namespace)
	output, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to grant privileged SCC: %v, output: %s", err, string(output))
	}

	return nil
}

// TestTLSConnection tests TLS connectivity to the DaemonSet metrics endpoint
// This is the PRIMARY TLS testing function used by all test scenarios
// Uses tls-scanner tool (https://github.com/richardsonnick/tls-scanner) for automated CI scanning
// This function expects the shared scanner to already be set up via SetupSharedScanner()
func TestTLSConnection(cs *testclient.ClientSet, ctx context.Context, operatorNS string, tlsVersion string) (bool, string) {
	LogStep(fmt.Sprintf("  Testing TLS %s connection using tls-scanner", tlsVersion))
	success, output := RunScanWithSharedScanner(cs, ctx, operatorNS, tlsVersion)
	LogStep(fmt.Sprintf("  Scanner returned: success=%v", success))
	return success, output
}

// TestTLSConnectionOVN tests TLS connectivity to OVN-Kubernetes components
// Uses tls-scanner tool to scan all pods in the openshift-ovn-kubernetes namespace
// This function expects the shared scanner to already be set up via SetupSharedScanner()
func TestTLSConnectionOVN(cs *testclient.ClientSet, ctx context.Context, tlsVersion string) (bool, string) {
	LogStep(fmt.Sprintf("  Testing OVN-Kubernetes TLS %s connection using tls-scanner", tlsVersion))
	success, output := RunScanWithSharedScanner(cs, ctx, OVNKubernetesNamespace, tlsVersion)
	LogStep(fmt.Sprintf("  OVN Scanner returned: success=%v", success))
	return success, output
}

// verifyContainerArgs verifies that a container in a pod template has expected/missing args
// Refactored (Round 3): Consolidates 220 lines of duplicate code from 4 arg verification functions:
// - VerifyDaemonSetArgs (52 lines) → 90% duplicate
// - VerifyDeploymentArgs (52 lines) → 90% duplicate
// - VerifyOVNKubeNodeDaemonSetArgs (60 lines) → 90% duplicate
// - VerifyOVNKubeControlPlaneDeploymentArgs (60 lines) → 90% duplicate
// All had identical logic for finding containers and checking args - now use this single generic function.
func verifyContainerArgs(
	containers []corev1.Container,
	containerName string,
	resourceType string,
	resourceName string,
	expectedArgs []string,
	missingArgs []string,
) error {
	if len(containers) == 0 {
		return fmt.Errorf("no containers found in %s %s", resourceType, resourceName)
	}

	// Find the specified container and get its command/args
	var commandStr string
	var found bool
	for _, container := range containers {
		if container.Name == containerName {
			found = true
			// Check both Command and Args fields (kube-rbac-proxy typically uses Command)
			commandStr = strings.Join(container.Command, " ")
			if commandStr == "" {
				// If Command is empty, check Args instead
				commandStr = strings.Join(container.Args, " ")
			}
			break
		}
	}

	if !found {
		return fmt.Errorf("%s container not found in %s %s", containerName, resourceType, resourceName)
	}

	if commandStr == "" {
		return fmt.Errorf("%s container has no command or args in %s %s", containerName, resourceType, resourceName)
	}

	// Check expected args are present
	for _, expectedArg := range expectedArgs {
		// Use simple substring match for expected args (they may already include '=' or other delimiters)
		if !strings.Contains(commandStr, expectedArg) {
			return fmt.Errorf("expected arg '%s' not found in %s command", expectedArg, containerName)
		}
	}

	// Check that args that should be missing are indeed missing
	for _, missingArg := range missingArgs {
		// Use exact match to prevent false positives like "--tls-min-version" matching "--tls-private-key-file"
		if containsExactArg(commandStr, missingArg) {
			return fmt.Errorf("unexpected arg '%s' found in %s command when it should be missing", missingArg, containerName)
		}
	}

	return nil
}

// VerifyDaemonSetArgs verifies that the kube-rbac-proxy container command in the DaemonSet
// contains expected TLS flags. The cluster-network-operator (multus) implements TLS profile
// compliance by configuring the kube-rbac-proxy sidecar container in network-metrics-daemon DaemonSet.
// Refactored (Round 3): Uses verifyContainerArgs to eliminate 90%+ duplicate code (52 → 9 lines)
func VerifyDaemonSetArgs(cs *testclient.ClientSet, ctx context.Context, namespace, daemonSetName string, expectedArgs []string, missingArgs []string) error {
	daemonSet, err := cs.AppsV1Interface.DaemonSets(namespace).Get(ctx, daemonSetName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get daemonset %s: %v", daemonSetName, err)
	}

	return verifyContainerArgs(
		daemonSet.Spec.Template.Spec.Containers,
		"kube-rbac-proxy",
		"daemonset",
		daemonSetName,
		expectedArgs,
		missingArgs,
	)
}

// VerifyDeploymentArgs verifies that the kube-rbac-proxy container command in the Deployment
// contains expected TLS flags. The multus-admission-controller Deployment also has kube-rbac-proxy.
// Refactored (Round 3): Uses verifyContainerArgs to eliminate 90%+ duplicate code (52 → 9 lines)
func VerifyDeploymentArgs(cs *testclient.ClientSet, ctx context.Context, namespace, deploymentName string, expectedArgs []string, missingArgs []string) error {
	deployment, err := cs.AppsV1Interface.Deployments(namespace).Get(ctx, deploymentName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get deployment %s: %v", deploymentName, err)
	}

	return verifyContainerArgs(
		deployment.Spec.Template.Spec.Containers,
		"kube-rbac-proxy",
		"deployment",
		deploymentName,
		expectedArgs,
		missingArgs,
	)
}

// VerifyOVNKubeNodeDaemonSetArgs verifies that both kube-rbac-proxy containers in ovnkube-node DaemonSet
// contain expected TLS flags. The ovnkube-node DaemonSet has TWO kube-rbac-proxy containers:
// 1. kube-rbac-proxy-node (for ovn-node-metrics)
// 2. kube-rbac-proxy-ovn-metrics (for ovn-metrics, not in dpu-host mode)
// Refactored (Round 3): Uses verifyContainerArgs to eliminate duplicate code (60 → 23 lines)
func VerifyOVNKubeNodeDaemonSetArgs(cs *testclient.ClientSet, ctx context.Context, expectedArgs []string, missingArgs []string) error {
	daemonSet, err := cs.AppsV1Interface.DaemonSets(OVNKubernetesNamespace).Get(ctx, OVNKubeNodeDaemonSetName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get daemonset %s: %v", OVNKubeNodeDaemonSetName, err)
	}

	// Verify kube-rbac-proxy-node container (required)
	if err := verifyContainerArgs(
		daemonSet.Spec.Template.Spec.Containers,
		OVNKubeRBACProxyNodeContainer,
		"daemonset",
		OVNKubeNodeDaemonSetName,
		expectedArgs,
		missingArgs,
	); err != nil {
		return err
	}

	// Verify kube-rbac-proxy-ovn-metrics container (optional - may not exist in dpu-host mode)
	if err := verifyContainerArgs(
		daemonSet.Spec.Template.Spec.Containers,
		OVNKubeRBACProxyOVNContainer,
		"daemonset",
		OVNKubeNodeDaemonSetName,
		expectedArgs,
		missingArgs,
	); err != nil {
		// Note: kube-rbac-proxy-ovn-metrics may not exist in dpu-host mode, so just log a warning
		LogStep(fmt.Sprintf("  Note: %s container not found (may be in dpu-host mode)", OVNKubeRBACProxyOVNContainer))
	}

	return nil
}

// VerifyOVNKubeControlPlaneDeploymentArgs verifies that the kube-rbac-proxy container in
// ovnkube-control-plane Deployment contains expected TLS flags
// Refactored (Round 3): Uses verifyContainerArgs to eliminate duplicate code (60 → 14 lines)
func VerifyOVNKubeControlPlaneDeploymentArgs(cs *testclient.ClientSet, ctx context.Context, expectedArgs []string, missingArgs []string) error {
	deployment, err := cs.AppsV1Interface.Deployments(OVNKubernetesNamespace).Get(ctx, OVNKubeControlPlaneDeployment, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get deployment %s: %v", OVNKubeControlPlaneDeployment, err)
	}

	return verifyContainerArgs(
		deployment.Spec.Template.Spec.Containers,
		OVNKubeRBACProxyControlContainer,
		"deployment",
		OVNKubeControlPlaneDeployment,
		expectedArgs,
		missingArgs,
	)
}

// containsExactArg checks if a command string contains an exact argument match
// This prevents false positives like "--tls-min-version" matching "--tls-private-key-file"
// Only used for missingArgs validation
func containsExactArg(commandStr, arg string) bool {
	// Check for arg with equals sign (e.g., "--tls-min-version=")
	if strings.Contains(commandStr, arg+"=") {
		return true
	}
	// Check for arg with space after it (e.g., "--tls-min-version ")
	if strings.Contains(commandStr, arg+" ") {
		return true
	}
	// Check for arg at end of line with backslash (e.g., "--tls-min-version \\\n")
	if strings.Contains(commandStr, arg+" \\") {
		return true
	}
	return false
}
