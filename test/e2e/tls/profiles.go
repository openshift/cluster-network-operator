package tls

import (
	"context"
	"fmt"
	osexec "os/exec"

	testclient "github.com/openshift/cluster-network-operator/test/e2e/client"
)

// patchAPIServer applies a JSON patch to the cluster apiserver object
// This helper consolidates duplicate oc patch commands across profile functions
func patchAPIServer(patchJSON string) error {
	cmd := osexec.Command("oc", "patch", "apiserver", "cluster", "--type=merge", "-p", patchJSON)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to patch apiserver: %s", string(output))
	}
	return nil
}

// getAPIServerField retrieves a field from the cluster apiserver object
// This helper consolidates duplicate oc get commands across profile check functions
func getAPIServerField(jsonPath string) (string, error) {
	cmd := osexec.Command("oc", "get", "apiserver", "cluster", "-o", fmt.Sprintf("jsonpath={%s}", jsonPath))
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get apiserver field %s: %s", jsonPath, string(output))
	}
	return string(output), nil
}

// ApplyModernProfileLegacy applies Modern TLS profile with LegacyAdheringComponentsOnly
func ApplyModernProfileLegacy() error {
	return patchAPIServer(`{"spec":{"tlsSecurityProfile":{"type":"Modern","modern":{}},"tlsAdherence":"LegacyAdheringComponentsOnly"}}`)
}

// UpdateAdherenceToStrict updates tlsAdherence to StrictAllComponents (assumes Modern profile already applied)
func UpdateAdherenceToStrict() error {
	return patchAPIServer(`{"spec":{"tlsAdherence":"StrictAllComponents"}}`)
}

// ApplyCustomProfile applies a custom TLS profile to the cluster
func ApplyCustomProfile() error {
	// Custom profile with VersionTLS12 and custom ciphers
	// Note: Must include HTTP/2-compatible ciphers (ECDHE-RSA-AES128-GCM-SHA256 or ECDHE-ECDSA-AES128-GCM-SHA256)
	// to satisfy OpenShift API Server validation requirements
	return patchAPIServer(`{
		"spec": {
			"tlsSecurityProfile": {
				"type": "Custom",
				"custom": {
					"ciphers": [
						"TLS_AES_128_GCM_SHA256",
						"ECDHE-RSA-AES128-GCM-SHA256"
					],
					"minTLSVersion": "VersionTLS12"
				}
			}
		}
	}`)
}

// IsBaselineProfile checks if the cluster is already in baseline configuration
// Returns true if tlsSecurityProfile is null/empty and tlsAdherence is LegacyAdheringComponentsOnly
func IsBaselineProfile() (bool, error) {
	profileType, err := getAPIServerField(".spec.tlsSecurityProfile.type")
	if err != nil {
		return false, err
	}

	adherence, err := getAPIServerField(".spec.tlsAdherence")
	if err != nil {
		return false, err
	}

	// Baseline = no explicit profile type (null/empty) + LegacyAdheringComponentsOnly
	return (profileType == "" || profileType == "Intermediate") && adherence == "LegacyAdheringComponentsOnly", nil
}

// IsModernLegacyProfile checks if the cluster is in Modern + LegacyAdheringComponentsOnly configuration
func IsModernLegacyProfile() (bool, error) {
	profileType, err := getAPIServerField(".spec.tlsSecurityProfile.type")
	if err != nil {
		return false, err
	}

	adherence, err := getAPIServerField(".spec.tlsAdherence")
	if err != nil {
		return false, err
	}

	// Modern + LegacyAdheringComponentsOnly = Modern profile + LegacyAdheringComponentsOnly
	return profileType == "Modern" && adherence == "LegacyAdheringComponentsOnly", nil
}

// RestoreBaselineProfile restores the baseline TLS profile and sets adherence to LegacyAdheringComponentsOnly
func RestoreBaselineProfile() error {
	// Note: tlsAdherence cannot be set to null once it's been set (API validation error).
	// Restore tlsSecurityProfile to null (defaults to Intermediate) and set tlsAdherence to LegacyAdheringComponentsOnly.
	return patchAPIServer(`{"spec":{"tlsSecurityProfile":null,"tlsAdherence":"LegacyAdheringComponentsOnly"}}`)
}

// EnsureTLSAdherenceEnabled checks if TLSAdherence featuregate is enabled,
// enables it if not, and waits for all nodes to reboot and become ready
func EnsureTLSAdherenceEnabled(cs *testclient.ClientSet, ctx context.Context) error {
	LogStep("Checking if TLSAdherence featuregate is enabled...")

	// Check if TLSAdherence is already enabled
	cmd := osexec.Command("oc", "get", "featuregate", "cluster", "-o", "jsonpath={.spec.customNoUpgrade.enabled[*]}")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to get featuregate status: %v", err)
	}

	enabledGates := string(output)
	if containsFeatureGate(enabledGates, "TLSAdherence") {
		LogStep("TLSAdherence featuregate is already enabled, proceeding with tests")
		return nil
	}

	LogStep("TLSAdherence featuregate is NOT enabled, enabling it now...")

	// Enable TLSAdherence featuregate
	patchCmd := osexec.Command("oc", "patch", "featuregate", "cluster", "--type=merge", "-p",
		`{"spec":{"featureSet":"CustomNoUpgrade","customNoUpgrade":{"enabled":["TLSAdherence"]}}}`)
	patchOutput, err := patchCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to enable TLSAdherence featuregate: %s", string(patchOutput))
	}

	LogStep("TLSAdherence featuregate enabled, waiting for nodes to reboot...")

	// Wait for Machine Config Pools to update (nodes will reboot)
	if err := WaitForMachineConfigPoolsReady(cs, ctx); err != nil {
		return fmt.Errorf("failed waiting for MCPs after enabling featuregate: %v", err)
	}

	// Wait for all nodes to be Ready and schedulable
	if err := WaitForAllNodesReady(cs, ctx); err != nil {
		return fmt.Errorf("failed waiting for nodes after enabling featuregate: %v", err)
	}

	LogStep("TLSAdherence featuregate enabled and all nodes ready")
	return nil
}
