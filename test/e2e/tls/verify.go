package tls

import (
	"context"
	"fmt"
	"time"

	testclient "github.com/openshift/cluster-network-operator/test/e2e/client"
)

// Assertion Helpers

// retryAssertionWithBackoff retries an assertion function with fixed delay
// Refactored (Round 2): Consolidates 4 duplicate retry patterns (assertTLSSupportedWithRetry,
// assertTLSNotSupportedWithRetry, assertOVNTLSSupportedWithRetry, assertOVNTLSNotSupportedWithRetry)
// All 4 functions had identical retry logic - now use this single generic wrapper.
func retryAssertionWithBackoff(
	description string,
	maxRetries int,
	retryDelay time.Duration,
	assertFunc func() error,
) error {
	var err error
	for i := 0; i < maxRetries; i++ {
		err = assertFunc()
		if err == nil {
			return nil
		}
		if i < maxRetries-1 {
			LogStep(fmt.Sprintf("  %s failed, retrying in %v... (attempt %d/%d)",
				description, retryDelay, i+1, maxRetries))
			time.Sleep(retryDelay)
		}
	}
	return err
}

// assertTLSSupported verifies that TLS connection succeeds and returns error if not
func assertTLSSupported(cs *testclient.ClientSet, ctx context.Context, operatorNS, tlsVersion string) error {
	success, output := TestTLSConnection(cs, ctx, operatorNS, tlsVersion)
	if !success {
		return fmt.Errorf("TLS %s connection failed when it should succeed. Output: %s", tlsVersion, output)
	}
	LogStep(fmt.Sprintf("  TLS %s connection succeeded as expected", tlsVersion))
	return nil
}

// assertTLSNotSupported verifies that TLS connection fails and returns error if it succeeds
func assertTLSNotSupported(cs *testclient.ClientSet, ctx context.Context, operatorNS, tlsVersion string) error {
	success, output := TestTLSConnection(cs, ctx, operatorNS, tlsVersion)
	if success {
		return fmt.Errorf("TLS %s connection succeeded when it should fail. Output: %s", tlsVersion, output)
	}
	LogStep(fmt.Sprintf("  TLS %s connection failed as expected", tlsVersion))
	return nil
}

// assertTLSSupportedWithRetry verifies TLS connection succeeds with retry logic
// Refactored (Round 2): Uses retryAssertionWithBackoff to eliminate duplicate retry logic
func assertTLSSupportedWithRetry(cs *testclient.ClientSet, ctx context.Context, operatorNS, tlsVersion string) error {
	return retryAssertionWithBackoff(
		fmt.Sprintf("TLS %s connection", tlsVersion),
		5, 10*time.Second,
		func() error { return assertTLSSupported(cs, ctx, operatorNS, tlsVersion) },
	)
}

// assertTLSNotSupportedWithRetry verifies TLS connection fails with retry logic
// Refactored (Round 2): Uses retryAssertionWithBackoff to eliminate duplicate retry logic
func assertTLSNotSupportedWithRetry(cs *testclient.ClientSet, ctx context.Context, operatorNS, tlsVersion string) error {
	return retryAssertionWithBackoff(
		fmt.Sprintf("TLS %s rejection test", tlsVersion),
		5, 10*time.Second,
		func() error { return assertTLSNotSupported(cs, ctx, operatorNS, tlsVersion) },
	)
}

// OVN-Kubernetes TLS Connection Testing Functions

// assertOVNTLSSupported verifies that OVN-Kubernetes TLS connection succeeds
func assertOVNTLSSupported(cs *testclient.ClientSet, ctx context.Context, tlsVersion string) error {
	success, output := TestTLSConnectionOVN(cs, ctx, tlsVersion)
	if !success {
		return fmt.Errorf("OVN-Kubernetes TLS %s connection failed when it should succeed. Output: %s", tlsVersion, output)
	}
	LogStep(fmt.Sprintf("  OVN-Kubernetes TLS %s connection succeeded as expected", tlsVersion))
	return nil
}

// assertOVNTLSNotSupported verifies that OVN-Kubernetes TLS connection fails
func assertOVNTLSNotSupported(cs *testclient.ClientSet, ctx context.Context, tlsVersion string) error {
	success, output := TestTLSConnectionOVN(cs, ctx, tlsVersion)
	if success {
		return fmt.Errorf("OVN-Kubernetes TLS %s connection succeeded when it should fail. Output: %s", tlsVersion, output)
	}
	LogStep(fmt.Sprintf("  OVN-Kubernetes TLS %s connection failed as expected", tlsVersion))
	return nil
}

// assertOVNTLSSupportedWithRetry verifies OVN-Kubernetes TLS connection succeeds with retry logic
// Refactored (Round 2): Uses retryAssertionWithBackoff to eliminate duplicate retry logic
func assertOVNTLSSupportedWithRetry(cs *testclient.ClientSet, ctx context.Context, tlsVersion string) error {
	return retryAssertionWithBackoff(
		fmt.Sprintf("OVN-Kubernetes TLS %s connection", tlsVersion),
		5, 10*time.Second,
		func() error { return assertOVNTLSSupported(cs, ctx, tlsVersion) },
	)
}

// assertOVNTLSNotSupportedWithRetry verifies OVN-Kubernetes TLS connection fails with retry logic
// Refactored (Round 2): Uses retryAssertionWithBackoff to eliminate duplicate retry logic
func assertOVNTLSNotSupportedWithRetry(cs *testclient.ClientSet, ctx context.Context, tlsVersion string) error {
	return retryAssertionWithBackoff(
		fmt.Sprintf("OVN-Kubernetes TLS %s rejection test", tlsVersion),
		5, 10*time.Second,
		func() error { return assertOVNTLSNotSupported(cs, ctx, tlsVersion) },
	)
}

// verifyDaemonSetTLSArgs verifies network-metrics-daemon DaemonSet kube-rbac-proxy TLS args
func verifyDaemonSetTLSArgs(cs *testclient.ClientSet, ctx context.Context, operatorNS string, expectedArgs, missingArgs []string) error {
	if err := VerifyDaemonSetArgs(cs, ctx, operatorNS, NetworkMetricsDaemonSetName, expectedArgs, missingArgs); err != nil {
		return fmt.Errorf("network-metrics-daemon DaemonSet TLS args verification failed: %v", err)
	}
	LogStep("  network-metrics-daemon DaemonSet TLS args verified")
	return nil
}

// verifyDeploymentTLSArgs verifies multus-admission-controller Deployment kube-rbac-proxy TLS args
func verifyDeploymentTLSArgs(cs *testclient.ClientSet, ctx context.Context, operatorNS string, expectedArgs, missingArgs []string) error {
	if err := VerifyDeploymentArgs(cs, ctx, operatorNS, DeploymentName, expectedArgs, missingArgs); err != nil {
		return fmt.Errorf("multus-admission-controller Deployment TLS args verification failed: %v", err)
	}
	LogStep("  multus-admission-controller Deployment TLS args verified")
	return nil
}

// verifyBothComponentsTLSArgs verifies TLS args for both network-metrics-daemon and multus-admission-controller
func verifyBothComponentsTLSArgs(cs *testclient.ClientSet, ctx context.Context, operatorNS string, expectedArgs, missingArgs []string) error {
	// Verify network-metrics-daemon DaemonSet
	if err := verifyDaemonSetTLSArgs(cs, ctx, operatorNS, expectedArgs, missingArgs); err != nil {
		return err
	}

	// Verify multus-admission-controller Deployment
	if err := verifyDeploymentTLSArgs(cs, ctx, operatorNS, expectedArgs, missingArgs); err != nil {
		return err
	}

	return nil
}

// verifyOVNKubernetesTLSArgs verifies TLS args for OVN-Kubernetes components
// This checks both ovnkube-node DaemonSet and ovnkube-control-plane Deployment
func verifyOVNKubernetesTLSArgs(cs *testclient.ClientSet, ctx context.Context, expectedArgs, missingArgs []string) error {
	LogStep("  Verifying OVN-Kubernetes components TLS args...")

	// Verify ovnkube-node DaemonSet (has 2 kube-rbac-proxy containers)
	if err := VerifyOVNKubeNodeDaemonSetArgs(cs, ctx, expectedArgs, missingArgs); err != nil {
		return fmt.Errorf("ovnkube-node DaemonSet TLS args verification failed: %v", err)
	}
	LogStep("    ovnkube-node DaemonSet TLS args verified")

	// Verify ovnkube-control-plane Deployment
	if err := VerifyOVNKubeControlPlaneDeploymentArgs(cs, ctx, expectedArgs, missingArgs); err != nil {
		return fmt.Errorf("ovnkube-control-plane Deployment TLS args verification failed: %v", err)
	}
	LogStep("    ovnkube-control-plane Deployment TLS args verified")

	return nil
}

// verifyAllComponentsTLSArgs verifies TLS args for ALL components:
// - openshift-multus namespace: network-metrics-daemon, multus-admission-controller
// - openshift-ovn-kubernetes namespace: ovnkube-node, ovnkube-control-plane
// - openshift-network-operator namespace: (no kube-rbac-proxy containers - nothing to verify)
func verifyAllComponentsTLSArgs(cs *testclient.ClientSet, ctx context.Context, operatorNS string, expectedArgs, missingArgs []string) error {
	// Verify openshift-multus components
	LogStep("  Verifying openshift-multus namespace components...")
	if err := verifyBothComponentsTLSArgs(cs, ctx, operatorNS, expectedArgs, missingArgs); err != nil {
		return err
	}

	// Verify openshift-ovn-kubernetes components
	// NOTE: OVN-Kubernetes has TLS args hardcoded in bash scripts, so this verification
	// may fail because args are not visible in container specs. This is expected behavior
	// documenting a product bug. Connection testing (Steps 4-7) will still validate runtime TLS.
	LogStep("  Verifying openshift-ovn-kubernetes namespace components...")
	if err := verifyOVNKubernetesTLSArgs(cs, ctx, expectedArgs, missingArgs); err != nil {
		return err
	}

	// NOTE: openshift-network-operator namespace has NO kube-rbac-proxy containers
	// Current workloads: network-operator deployment, iptables-alerter daemonset
	// Both have only single containers without kube-rbac-proxy
	// No TLS verification or connection testing needed for this namespace
	LogStep("  Note: openshift-network-operator has no kube-rbac-proxy containers - skipping")

	LogStep("  All components TLS args verified (openshift-multus + openshift-ovn-kubernetes)")
	return nil
}

// Scenario Helper Functions - Shared logic across all 4 test scenarios

// applyProfileChange checks if profile is already set, applies if needed
// Returns true if restart is needed (profile was changed)
func applyProfileChange(profileName string, checkFn func() (bool, error), applyFn func() error) (bool, error) {
	isAlreadySet, err := checkFn()
	if err != nil {
		return false, fmt.Errorf("failed to check current profile: %v", err)
	}

	if isAlreadySet {
		LogStep(fmt.Sprintf("Step 0: Cluster already in %s configuration, skipping", profileName))
		return false, nil
	}

	LogStep(fmt.Sprintf("Step 0: Applying %s profile", profileName))
	if err := applyFn(); err != nil {
		return false, fmt.Errorf("failed to apply %s profile: %v", profileName, err)
	}

	return true, nil
}

// waitForInfrastructure waits for infrastructure readiness
// clusterWide=true: waits for MCPs, operators, nodes (cluster-wide TLS changes)
// clusterWide=false: waits only for operator pods (operator-level changes)
func waitForInfrastructure(cs *testclient.ClientSet, ctx context.Context, operatorNS string, clusterWide bool) error {
	if clusterWide {
		LogStep("Step 1: Waiting for operator to apply configuration (cluster-wide)")
		return WaitForOperatorRestart(cs, ctx, operatorNS)
	}
	return WaitForOperatorPodsOnly(cs, ctx, operatorNS)
}

// runTLSTest tests TLS connection with assertion
func runTLSTest(cs *testclient.ClientSet, ctx context.Context, operatorNS, stepNum, tlsVersion string, shouldSucceed bool) error {
	expectation := "should succeed"
	if !shouldSucceed {
		expectation = "should FAIL"
	}

	LogStep(fmt.Sprintf("Step %s: Test TLS %s connection (%s)", stepNum, tlsVersion, expectation))

	if shouldSucceed {
		return assertTLSSupported(cs, ctx, operatorNS, tlsVersion)
	}
	return assertTLSNotSupported(cs, ctx, operatorNS, tlsVersion)
}

// setupScannerWithCleanup sets up the TLS scanner and returns a cleanup function
// This consolidates the 4 duplicate scanner setup+defer patterns across all test scenarios
func setupScannerWithCleanup(cs *testclient.ClientSet, ctx context.Context, operatorNS string) (func(), error) {
	LogStep("Setting up TLS scanner pod (after infrastructure stable)")
	if err := SetupSharedScanner(cs, ctx, operatorNS); err != nil {
		return nil, err
	}

	cleanup := func() {
		LogStep("Cleaning up scanner pod")
		CleanupSharedScanner(cs, ctx)
	}

	return cleanup, nil
}

// verifyTLSArgsForProfileWithRetry verifies DaemonSet TLS args based on profile type with retry
// This consolidates the 3 duplicate retry patterns (6 attempts, 10s sleep) across scenarios
func verifyTLSArgsForProfileWithRetry(cs *testclient.ClientSet, ctx context.Context, operatorNS, profileType string) error {
	var stepLabel string
	var expectedArgs, missingArgs []string

	switch profileType {
	case "baseline":
		stepLabel = "Verify --tls-min-version and --tls-cipher-suites are missing"
		expectedArgs = []string{}
		missingArgs = []string{"--tls-min-version", "--tls-cipher-suites"}

	case "modern-legacy":
		stepLabel = "Verify --tls-min-version and --tls-cipher-suites are missing"
		expectedArgs = []string{}
		missingArgs = []string{"--tls-min-version", "--tls-cipher-suites"}

	case "strict":
		stepLabel = "Verify --tls-min-version is set to VersionTLS13 (do not verify --tls-cipher-suites)"
		expectedArgs = []string{"--tls-min-version=VersionTLS13"}
		missingArgs = []string{}

	case "custom":
		stepLabel = "Verify --tls-min-version=VersionTLS12 and custom cipher suites"
		expectedArgs = []string{"--tls-min-version=VersionTLS12", "--tls-cipher-suites=TLS_AES_128_GCM_SHA256", "--tls-cipher-suites=ECDHE-RSA-AES128-GCM-SHA256"}
		missingArgs = []string{}

	default:
		return fmt.Errorf("unknown profile type: %s", profileType)
	}

	LogStep(fmt.Sprintf("Step 3: %s", stepLabel))

	// For baseline, no retry needed (profile not changed)
	if profileType == "baseline" {
		return verifyAllComponentsTLSArgs(cs, ctx, operatorNS, expectedArgs, missingArgs)
	}

	// For other profiles, retry up to 6 times with 10s sleep
	// DaemonSet/Deployment args may take time to update after profile changes
	LogStep("Waiting for all components to update with TLS arguments...")
	var verifyErr error
	for i := 0; i < 6; i++ {
		verifyErr = verifyAllComponentsTLSArgs(cs, ctx, operatorNS, expectedArgs, missingArgs)
		if verifyErr == nil {
			return nil
		}
		if i < 5 {
			LogStep(fmt.Sprintf("  TLS args not yet updated, retrying in 10s... (attempt %d/6)", i+1))
			time.Sleep(10 * time.Second)
		}
	}
	return verifyErr
}

// verifyTLSArgsForProfile verifies DaemonSet TLS args based on profile type (no retry)
// Use verifyTLSArgsForProfileWithRetry for scenarios where profile was just changed
func verifyTLSArgsForProfile(cs *testclient.ClientSet, ctx context.Context, operatorNS, profileType string) error {
	var stepLabel string
	var expectedArgs, missingArgs []string

	switch profileType {
	case "baseline":
		stepLabel = "Verify --tls-min-version and --tls-cipher-suites are missing"
		expectedArgs = []string{}
		missingArgs = []string{"--tls-min-version", "--tls-cipher-suites"}

	case "modern-legacy":
		stepLabel = "Verify --tls-min-version and --tls-cipher-suites are missing"
		expectedArgs = []string{}
		missingArgs = []string{"--tls-min-version", "--tls-cipher-suites"}

	case "strict":
		stepLabel = "Verify --tls-min-version is set to VersionTLS13 (do not verify --tls-cipher-suites)"
		expectedArgs = []string{"--tls-min-version=VersionTLS13"}
		missingArgs = []string{}

	case "custom":
		stepLabel = "Verify --tls-min-version=VersionTLS12 and custom cipher suites"
		expectedArgs = []string{"--tls-min-version=VersionTLS12", "--tls-cipher-suites=TLS_AES_128_GCM_SHA256", "--tls-cipher-suites=ECDHE-RSA-AES128-GCM-SHA256"}
		missingArgs = []string{}

	default:
		return fmt.Errorf("unknown profile type: %s", profileType)
	}

	LogStep(fmt.Sprintf("Step 3: %s", stepLabel))
	return verifyAllComponentsTLSArgs(cs, ctx, operatorNS, expectedArgs, missingArgs)
}

// Test Scenario Verification Functions

// VerifyBaselineConfiguration verifies baseline TLS configuration
// Requirements:
//   - Baseline (tlsAdherence: LegacyAdheringComponentsOnly or empty with no tlsSecurityProfile set)
//     1. Check if already in baseline, restore if needed (triggers MCP updates, node reboots)
//     2. Wait for operator restart (only if profile was changed)
//     2a. Force fresh pod restart (only if profile was changed)
//     3. Create scanner pod (AFTER infrastructure is stable)
//     4. Verify --tls-min-version and --tls-cipher-suites CLI args are missing from DaemonSet kube-rbac-proxy
//     5. Test: openssl s_client -tls1_2 → should succeed
//     6. Cleanup scanner pod
func VerifyBaselineConfiguration(cs *testclient.ClientSet, ctx context.Context, operatorNS string) error {
	LogStep("Scenario 1: Baseline TLS Configuration")

	// Step 0: Apply profile change (or skip if already set)
	needsRestart, err := applyProfileChange("baseline", IsBaselineProfile, RestoreBaselineProfile)
	if err != nil {
		return err
	}

	// Step 1: Wait for infrastructure (if changed)
	if needsRestart {
		if err := waitForInfrastructure(cs, ctx, operatorNS, true); err != nil {
			return err
		}
		LogStep("Step 1a: Deleting all pods to get fresh restart after baseline profile change")
		if err := ForceOperatorPodRestart(cs, ctx, operatorNS); err != nil {
			return err
		}
	} else {
		LogStep("Step 1: Skipping operator restart wait (profile unchanged)")
	}

	// Step 2: Setup scanner with cleanup
	LogStep("Step 2: Setting up TLS scanner pod")
	cleanup, err := setupScannerWithCleanup(cs, ctx, operatorNS)
	if err != nil {
		return err
	}
	defer cleanup()

	// Step 3: Verify args (no retry for baseline since profile wasn't changed)
	if err := verifyTLSArgsForProfile(cs, ctx, operatorNS, "baseline"); err != nil {
		return err
	}

	// Step 4: Verify OVN database TLS configuration
	LogStep("Step 4: Verifying OVN database TLS configuration")
	if err := VerifyOVNDatabaseTLS(cs, ctx, "baseline"); err != nil {
		return err
	}

	// Step 5: Test TLS 1.2 (should succeed)
	return runTLSTest(cs, ctx, operatorNS, "5", "1.2", true)
}

// VerifyModernLegacyAdherence verifies Modern TLS with LegacyAdheringComponentsOnly
// Requirements:
//   - Modern profile with LegacyAdheringComponentsOnly
//     1. Apply Modern profile with LegacyAdheringComponentsOnly
//     2. Wait for cluster operators to update
//     2a. Force fresh pod restart
//     3. Create scanner pod
//     4. Verify --tls-min-version and --tls-cipher-suites CLI args are MISSING for ovn, multus, CNO
//     5. Test TLS connections
func VerifyModernLegacyAdherence(cs *testclient.ClientSet, ctx context.Context, operatorNS string) error {
	LogStep("Scenario 2: Modern TLS Profile with LegacyAdheringComponentsOnly")

	// Step 0: Apply profile change (or skip if already set)
	needsRestart, err := applyProfileChange("Modern + LegacyAdheringComponentsOnly", IsModernLegacyProfile, ApplyModernProfileLegacy)
	if err != nil {
		return err
	}

	// Step 1: Wait for infrastructure (if changed)
	if needsRestart {
		if err := waitForInfrastructure(cs, ctx, operatorNS, true); err != nil {
			return err
		}
		LogStep("Step 1a: Deleting all pods to get fresh restart after TLS profile change")
		if err := ForceOperatorPodRestart(cs, ctx, operatorNS); err != nil {
			return err
		}
	} else {
		LogStep("Step 1: Skipping operator restart wait (profile unchanged)")
	}

	// Step 2: Setup scanner with cleanup
	LogStep("Step 2: Setting up TLS scanner pod")
	cleanup, err := setupScannerWithCleanup(cs, ctx, operatorNS)
	if err != nil {
		return err
	}
	defer cleanup()

	// Step 3: Verify args with retry
	if err := verifyTLSArgsForProfileWithRetry(cs, ctx, operatorNS, "modern-legacy"); err != nil {
		return err
	}

	// Step 4: Verify OVN database TLS configuration
	LogStep("Step 4: Verifying OVN database TLS configuration")
	if err := VerifyOVNDatabaseTLS(cs, ctx, "modern-legacy"); err != nil {
		return err
	}

	// Step 5: Test TLS 1.2 with retry
	LogStep("Step 5: Test TLS 1.2 connection (should succeed)")
	return assertTLSSupportedWithRetry(cs, ctx, operatorNS, "1.2")
}

// VerifyStrictAdherence verifies StrictAllComponents adherence
// Requirements:
//   - StrictAllComponents adherence (assumes Modern profile already applied)
//     1. Update tlsAdherence to StrictAllComponents
//     2. Wait for operator pods to update
//     2a. Force fresh pod restart
//     3. Create scanner pod
//     4. Verify --tls-min-version=VersionTLS13 is set for ovn, multus, CNO
//     (do NOT verify --tls-cipher-suites)
//     5. Test TLS connections (1.2 should fail, 1.3 should succeed)
func VerifyStrictAdherence(cs *testclient.ClientSet, ctx context.Context, operatorNS string) error {
	LogStep("Scenario 3: Update tlsAdherence to StrictAllComponents")

	// Step 0: Update adherence to strict (operator-level only, not cluster-wide)
	LogStep("Step 0: Updating tlsAdherence to StrictAllComponents")
	if err := UpdateAdherenceToStrict(); err != nil {
		return err
	}

	// Step 1: Wait for operator pods only (not cluster-wide)
	if err := waitForInfrastructure(cs, ctx, operatorNS, false); err != nil {
		return err
	}

	LogStep("Step 1a: Deleting all pods to get fresh restart after StrictAllComponents change")
	if err := ForceOperatorPodRestart(cs, ctx, operatorNS); err != nil {
		return err
	}

	// Step 2: Setup scanner with cleanup
	LogStep("Step 2: Setting up TLS scanner pod")
	cleanup, err := setupScannerWithCleanup(cs, ctx, operatorNS)
	if err != nil {
		return err
	}
	defer cleanup()

	// Step 3: Verify args with retry
	if err := verifyTLSArgsForProfileWithRetry(cs, ctx, operatorNS, "strict"); err != nil {
		return err
	}

	// Step 4: Verify OVN database TLS configuration with retry
	LogStep("Step 4: Verifying OVN database TLS configuration (TLS 1.3)")
	if err := VerifyOVNDatabaseTLSWithRetry(cs, ctx, "strict"); err != nil {
		return err
	}

	// Step 5: Test openshift-multus TLS 1.2 should FAIL with retry
	LogStep("Step 5: Test openshift-multus TLS 1.2 connection (should FAIL)")
	if err := assertTLSNotSupportedWithRetry(cs, ctx, operatorNS, "1.2"); err != nil {
		return err
	}

	// Step 6: Test openshift-multus TLS 1.3 should succeed with retry
	LogStep("Step 6: Test openshift-multus TLS 1.3 connection (should succeed)")
	if err := assertTLSSupportedWithRetry(cs, ctx, operatorNS, "1.3"); err != nil {
		return err
	}

	// Step 7: Test openshift-ovn-kubernetes TLS 1.2 should FAIL with retry
	// NOTE: We test TLS connections even though we skip args verification
	// Scanner tests actual TLS behavior regardless of how args are configured
	LogStep("Step 7: Test openshift-ovn-kubernetes TLS 1.2 connection (should FAIL)")
	if err := assertOVNTLSNotSupportedWithRetry(cs, ctx, "1.2"); err != nil {
		return err
	}

	// Step 8: Test openshift-ovn-kubernetes TLS 1.3 should succeed with retry
	LogStep("Step 8: Test openshift-ovn-kubernetes TLS 1.3 connection (should succeed)")
	if err := assertOVNTLSSupportedWithRetry(cs, ctx, "1.3"); err != nil {
		return err
	}

	// NOTE: openshift-network-operator namespace has no kube-rbac-proxy containers
	// No TLS connection tests needed for this namespace
	LogStep("Note: openshift-network-operator has no kube-rbac-proxy - no TLS connection tests")

	return nil
}

// VerifyCustomConfiguration verifies custom TLS profile
// Requirements:
//   - Custom TLS profile with minTLSVersion=VersionTLS12 and custom ciphers
//     ciphers=[TLS_AES_128_GCM_SHA256, ECDHE-RSA-AES128-GCM-SHA256]
//     1. Apply custom TLS profile
//     2. Wait for cluster operators to update
//     2a. Force fresh pod restart
//     3. Create scanner pod
//     4. Verify --tls-min-version=VersionTLS12 and both custom cipher suites
//     are set correctly for ovn, multus, CNO
//     5. Test TLS 1.2 connection (should succeed)
func VerifyCustomConfiguration(cs *testclient.ClientSet, ctx context.Context, operatorNS string) error {
	LogStep("Scenario 4: Custom TLS Profile")

	// Step 0: Apply custom TLS profile (cluster-wide change)
	LogStep("Step 0: Apply custom TLS profile (minTLSVersion=VersionTLS12, custom ciphers)")
	if err := ApplyCustomProfile(); err != nil {
		return err
	}

	// Step 1: Wait for cluster-wide infrastructure
	if err := waitForInfrastructure(cs, ctx, operatorNS, true); err != nil {
		return err
	}

	LogStep("Step 1a: Deleting all pods to get fresh restart after Custom profile change")
	if err := ForceOperatorPodRestart(cs, ctx, operatorNS); err != nil {
		return err
	}

	// Step 2: Setup scanner with cleanup
	LogStep("Step 2: Setting up TLS scanner pod")
	cleanup, err := setupScannerWithCleanup(cs, ctx, operatorNS)
	if err != nil {
		return err
	}
	defer cleanup()

	// Step 3: Verify args with retry
	if err := verifyTLSArgsForProfileWithRetry(cs, ctx, operatorNS, "custom"); err != nil {
		return err
	}

	// Step 4: Verify OVN database TLS configuration with retry
	LogStep("Step 4: Verifying OVN database TLS configuration (TLS 1.2)")
	if err := VerifyOVNDatabaseTLSWithRetry(cs, ctx, "custom"); err != nil {
		return err
	}

	// Step 5: Test TLS 1.2 should succeed with retry
	LogStep("Step 5: Test TLS 1.2 connection (should succeed)")
	return assertTLSSupportedWithRetry(cs, ctx, operatorNS, "1.2")
}
