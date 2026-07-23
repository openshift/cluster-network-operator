package e2e

import (
	"context"
	"fmt"
	"testing"

	testclient "github.com/openshift/cluster-network-operator/test/e2e/client"
	tlstest "github.com/openshift/cluster-network-operator/test/e2e/tls"

	. "github.com/onsi/gomega"
)

func TestNetworkingTLSProfileCompliance(t *testing.T) {
	g := NewWithT(t)

	var (
		cs         = testclient.Client
		ctx        = context.Background()
		operatorNS = "openshift-multus"
	)

	// Ensure TLSAdherence featuregate is enabled before running tests
	t.Log("Ensuring TLSAdherence featuregate is enabled")
	err := tlstest.EnsureTLSAdherenceEnabled(cs, ctx)
	g.Expect(err).ToNot(HaveOccurred(), "Failed to ensure TLSAdherence featuregate is enabled")

	// Ensure cleanup at the end
	defer func() {
		t.Log("Cleaning up scanner namespace")
		tlstest.CleanupScannerNamespace(cs, ctx)
		t.Log("Restoring Baseline TLS Profile")
		if err := tlstest.RestoreBaselineProfile(); err != nil {
			t.Errorf("restoring Baseline TLS Profile: %v", err)
		}
	}()

	var testFailures []string

	// Scenario 1: Baseline TLS Configuration
	// NOTE: Scanner pod is created INSIDE the scenario, after infrastructure is stable
	t.Log("Scenario 1: Verify baseline TLS configuration for networking components")
	err = tlstest.VerifyBaselineConfiguration(cs, ctx, operatorNS)
	if err != nil {
		testFailures = append(testFailures, fmt.Sprintf("Scenario 1 (Baseline): %v", err))
	}

	// Scenario 2: Modern TLS Profile with LegacyAdheringComponentsOnly
	t.Log("Scenario 2: Verify Modern TLS Profile with LegacyAdheringComponentsOnly")
	modernErr := tlstest.VerifyModernLegacyAdherence(cs, ctx, operatorNS)
	if modernErr != nil {
		testFailures = append(testFailures, fmt.Sprintf("Scenario 2 (Modern with Legacy): %v", modernErr))
	}

	// Scenario 3: Update tlsAdherence to StrictAllComponents
	// Only run if Modern was successfully established (prerequisite)
	if modernErr == nil {
		t.Log("Scenario 3: Update tlsAdherence to StrictAllComponents")
		err = tlstest.VerifyStrictAdherence(cs, ctx, operatorNS)
		if err != nil {
			testFailures = append(testFailures, fmt.Sprintf("Scenario 3 (Strict Adherence): %v", err))
		}
	} else {
		t.Log("Scenario 3: Skipping Strict verification because Modern prerequisite failed")
		testFailures = append(testFailures, "Scenario 3 (Strict Adherence): Skipped due to Modern prerequisite failure")
	}

	// Scenario 4: Custom TLS Profile
	t.Log("Scenario 4: Verify Custom TLS Profile configuration for networking components")
	err = tlstest.VerifyCustomConfiguration(cs, ctx, operatorNS)
	if err != nil {
		testFailures = append(testFailures, fmt.Sprintf("Scenario 4 (Custom): %v", err))
	}

	// Report all failures at the end
	if len(testFailures) > 0 {
		t.Fatalf("TLS Profile Compliance test completed with %d failure(s): %v", len(testFailures), testFailures)
	}
}
