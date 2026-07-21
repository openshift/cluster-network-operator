// Package tls - OVN Database TLS verification functions
package tls

import (
	"context"
	"fmt"
	osexec "os/exec"
	"strings"
	"time"

	testclient "github.com/openshift/cluster-network-operator/test/e2e/client"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// VerifyOVNDatabaseTLS verifies NB and SB database TLS configuration
// This function queries the OVSDB SSL table to verify TLS protocol compliance
// Note: OVN databases store TLS config in the database, not as CLI args
func VerifyOVNDatabaseTLS(cs *testclient.ClientSet, ctx context.Context, profileType string) error {
	var expectedProtocol string
	var shouldVerify bool

	switch profileType {
	case "baseline", "modern-legacy":
		// For LegacyAdheringComponentsOnly, OVN databases may not enforce TLS profiles
		// Skip verification for legacy adherence modes
		LogStep("  Skipping OVN database TLS verification (LegacyAdheringComponentsOnly)")
		return nil

	case "strict":
		expectedProtocol = "TLSv1.3"
		shouldVerify = true

	case "custom":
		expectedProtocol = "TLSv1.2"
		shouldVerify = true

	default:
		return fmt.Errorf("unknown profile type: %s", profileType)
	}

	if !shouldVerify {
		return nil
	}

	LogStep(fmt.Sprintf("  Verifying OVN database TLS configuration (expected: %s)", expectedProtocol))

	// Get a running ovnkube-node pod
	podName, err := getRunningOVNNodePod(cs, ctx)
	if err != nil {
		return fmt.Errorf("failed to get running ovnkube-node pod: %v", err)
	}

	// Verify NB database
	LogStep(fmt.Sprintf("    Checking NB database TLS protocol..."))
	nbProtocol, err := queryOVNDBProtocol(podName, "nbdb", "ovn-nbctl")
	if err != nil {
		return fmt.Errorf("NB database TLS query failed: %v", err)
	}

	if !verifyProtocolMatch(nbProtocol, expectedProtocol) {
		return fmt.Errorf("NB database TLS protocol mismatch: expected %s, got %s", expectedProtocol, nbProtocol)
	}
	LogStep(fmt.Sprintf("    NB database TLS protocol verified: %s", nbProtocol))

	// Verify SB database
	LogStep(fmt.Sprintf("    Checking SB database TLS protocol..."))
	sbProtocol, err := queryOVNDBProtocol(podName, "sbdb", "ovn-sbctl")
	if err != nil {
		return fmt.Errorf("SB database TLS query failed: %v", err)
	}

	if !verifyProtocolMatch(sbProtocol, expectedProtocol) {
		return fmt.Errorf("SB database TLS protocol mismatch: expected %s, got %s", expectedProtocol, sbProtocol)
	}
	LogStep(fmt.Sprintf("    SB database TLS protocol verified: %s", sbProtocol))

	LogStep("  OVN database TLS configuration verified successfully")
	return nil
}

// getRunningOVNNodePod finds a running ovnkube-node pod
func getRunningOVNNodePod(cs *testclient.ClientSet, ctx context.Context) (string, error) {
	pods, err := cs.CoreV1Interface.Pods(OVNKubernetesNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app=ovnkube-node",
	})
	if err != nil {
		return "", fmt.Errorf("failed to list ovnkube-node pods: %v", err)
	}

	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no ovnkube-node pods found in namespace %s", OVNKubernetesNamespace)
	}

	// Find first running pod
	for _, pod := range pods.Items {
		if pod.Status.Phase == "Running" {
			return pod.Name, nil
		}
	}

	return "", fmt.Errorf("no running ovnkube-node pods found")
}

// queryOVNDBProtocol queries the OVSDB SSL table for TLS protocol configuration
func queryOVNDBProtocol(podName, containerName, ctlCmd string) (string, error) {
	// Execute ovn-nbctl/ovn-sbctl command to get SSL protocols
	cmd := osexec.Command("oc", "exec", "-n", OVNKubernetesNamespace,
		podName, "-c", containerName, "--",
		ctlCmd, "get", "SSL", ".", "ssl_protocols")

	output, err := cmd.CombinedOutput()
	if err != nil {
		// Sanitize output to avoid exposing internal endpoints in logs
		sanitized := strings.TrimSpace(string(output))
		if len(sanitized) > 100 {
			sanitized = sanitized[:100] + "...(truncated)"
		}
		return "", fmt.Errorf("failed to query %s SSL config in container %s: %v (output: %s)", containerName, containerName, err, sanitized)
	}

	// Parse output - format is typically: "TLSv1.3" or ""
	protocol := strings.TrimSpace(string(output))
	// Remove quotes if present
	protocol = strings.Trim(protocol, "\"")

	if protocol == "" {
		return "", fmt.Errorf("%s SSL protocols not configured (empty)", containerName)
	}

	return protocol, nil
}

// VerifyOVNDatabaseTLSWithRetry verifies OVN database TLS with retry logic
// Databases may take time to update after TLS profile changes
func VerifyOVNDatabaseTLSWithRetry(cs *testclient.ClientSet, ctx context.Context, profileType string) error {
	var expectedProtocol string
	var shouldVerify bool

	switch profileType {
	case "baseline", "modern-legacy":
		// Skip for legacy adherence
		return VerifyOVNDatabaseTLS(cs, ctx, profileType)

	case "strict":
		expectedProtocol = "TLSv1.3"
		shouldVerify = true

	case "custom":
		expectedProtocol = "TLSv1.2"
		shouldVerify = true

	default:
		return fmt.Errorf("unknown profile type: %s", profileType)
	}

	if !shouldVerify {
		return nil
	}

	LogStep(fmt.Sprintf("  Verifying OVN database TLS with retry (expected: %s)", expectedProtocol))

	// Retry up to 6 times with 10s sleep
	var verifyErr error
	for i := 0; i < 6; i++ {
		verifyErr = VerifyOVNDatabaseTLS(cs, ctx, profileType)
		if verifyErr == nil {
			return nil
		}
		if i < 5 {
			LogStep(fmt.Sprintf("    OVN database TLS not yet configured, retrying in 10s... (attempt %d/6)", i+1))
			time.Sleep(10 * time.Second)
		}
	}

	return verifyErr
}

// verifyProtocolMatch parses comma/space-delimited protocol strings and verifies exact match
func verifyProtocolMatch(actual, expected string) bool {
	// Parse actual protocols (comma or space delimited)
	actualProtocols := parseProtocols(actual)
	expectedProtocols := parseProtocols(expected)

	// Check if sets match exactly
	if len(actualProtocols) != len(expectedProtocols) {
		return false
	}

	for protocol := range expectedProtocols {
		if !actualProtocols[protocol] {
			return false
		}
	}

	return true
}

// parseProtocols splits a comma/space-delimited protocol string into a set
func parseProtocols(s string) map[string]bool {
	protocols := make(map[string]bool)
	s = strings.TrimSpace(s)

	// Split by comma or space
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' '
	})

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			protocols[part] = true
		}
	}

	return protocols
}
