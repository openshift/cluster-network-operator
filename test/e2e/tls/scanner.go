// Package tls - TLS scanner management for reusable scanner pod
package tls

import (
	"context"
	"fmt"
	osexec "os/exec"
	"time"

	testclient "github.com/openshift/cluster-network-operator/test/e2e/client"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

// Scanner Pod Builder Functions

// ScannerPodConfig holds configuration for scanner pod creation
type ScannerPodConfig struct {
	Name          string
	Namespace     string
	RestartPolicy corev1.RestartPolicy
	Persistent    bool   // If true, runs infinite loop; if false, one-shot scan
	OperatorNS    string // For one-shot scans
}

// buildScannerPodSpec creates a scanner pod specification with configurable options
func buildScannerPodSpec(config ScannerPodConfig) *corev1.Pod {
	command := buildScannerCommand(config.Persistent, config.OperatorNS)

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      config.Name,
			Namespace: config.Namespace,
		},
		Spec: corev1.PodSpec{
			ServiceAccountName: "default",
			RestartPolicy:      config.RestartPolicy,
			HostNetwork:        true,
			HostPID:            true,
			Containers: []corev1.Container{
				{
					Name:    ScannerContainerName,
					Image:   ScannerImage,
					Command: []string{"/bin/bash", "-c", command},
					SecurityContext: &corev1.SecurityContext{
						Privileged: boolPtr(true),
						RunAsUser:  int64Ptr(0),
					},
					Resources: buildScannerResources(),
				},
			},
		},
	}
}

// buildScannerCommand generates the scanner container command based on mode
func buildScannerCommand(persistent bool, operatorNS string) string {
	// Common installation steps
	baseSetup := `
set -ex
echo "=== Installing dependencies ==="
dnf install -y --allowerasing git golang tar curl lsof openssl bash wget procps-ng bind-utils

echo "=== Installing oc ==="
ARCH=$(uname -m)
wget -q -O /tmp/openshift-client.tar.gz "https://mirror.openshift.com/pub/openshift-v4/${ARCH}/clients/ocp/latest/openshift-client-linux.tar.gz"
tar -C /usr/local/bin -xzf /tmp/openshift-client.tar.gz oc kubectl
rm -f /tmp/openshift-client.tar.gz

echo "=== Building tls-scanner ==="
cd /opt
git clone https://github.com/richardsonnick/tls-scanner.git
cd tls-scanner
make build
cp bin/tls-scanner /usr/local/bin/tls-scanner
chmod +x /usr/local/bin/tls-scanner

echo "=== Installing testssl.sh ==="
cd /opt
git clone https://github.com/drwetter/testssl.sh.git
cd testssl.sh
# Use v3.0.8 which still supports --connect-timeout flag
# Later versions renamed it to --socket-timeout (incompatible with tls-scanner)
git checkout v3.0.8
cd ..
# Copy entire testssl.sh directory to preserve etc/ support files
mkdir -p /usr/local/share/testssl
cp -r testssl.sh/* /usr/local/share/testssl/
ln -s /usr/local/share/testssl/testssl.sh /usr/local/bin/testssl.sh
chmod +x /usr/local/bin/testssl.sh

echo "=== Scanner ready ==="
touch /tmp/scanner.ready
ls -la /usr/local/bin/tls-scanner
ls -la /usr/local/bin/testssl.sh
ls -la /usr/local/share/testssl/etc/
`

	if persistent {
		return baseSetup + `
echo "Scanner pod will stay alive indefinitely"
while true; do
  sleep 3600
  echo "Scanner still alive at $(date)"
done
`
	} else {
		return baseSetup + fmt.Sprintf(`
echo "=== Running one-shot scan ==="
mkdir -p /results
/usr/local/bin/tls-scanner \
  --all-pods \
  --namespace-filter %s \
  --artifact-dir /results \
  --json-file scan.json \
  --csv-file scan.csv \
  --log-file scan.log 2>&1 | tee /results/output.log

echo "=== Scan complete ==="
touch /results/scan.done
sleep 300
`, operatorNS)
	}
}

// buildScannerResources returns resource requirements for scanner pod
func buildScannerResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(ScannerCPURequest),
			corev1.ResourceMemory: resource.MustParse(ScannerMemoryRequest),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(ScannerCPULimit),
			corev1.ResourceMemory: resource.MustParse(ScannerMemoryLimit),
		},
	}
}

// SetupSharedScanner creates a single scanner pod that can be reused across all test scenarios
func SetupSharedScanner(cs *testclient.ClientSet, ctx context.Context, operatorNS string) error {
	LogStep("Setting up shared TLS scanner pod (one-time setup)...")

	// Create scanner namespace (if it doesn't exist)
	ns, err := cs.CoreV1Interface.Namespaces().Get(ctx, SharedScannerNamespace, metav1.GetOptions{})
	if err != nil {
		// Namespace doesn't exist, create it
		LogStep("  Creating scanner namespace...")
		ns, err = cs.CoreV1Interface.Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: SharedScannerNamespace,
			},
		}, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("failed to create scanner namespace: %v", err)
		}

		// Wait for OpenShift to add SCC annotations to the namespace
		// This is required for pod creation to succeed
		err = wait.PollUntilContextTimeout(ctx, 1*time.Second, 30*time.Second, true, func(ctx context.Context) (bool, error) {
			ns, err = cs.CoreV1Interface.Namespaces().Get(ctx, SharedScannerNamespace, metav1.GetOptions{})
			if err != nil {
				return false, nil
			}
			// Check if SCC UID range annotation is present
			if _, ok := ns.Annotations["openshift.io/sa.scc.uid-range"]; ok {
				return true, nil
			}
			return false, nil
		})
		if err != nil {
			return fmt.Errorf("namespace created but SCC annotations not added within timeout: %v", err)
		}
		LogStep("  Scanner namespace created with SCC annotations")

		// Grant privileges to scanner service account (only needed once when namespace is created)
		if err := grantScannerPrivileges(SharedScannerNamespace); err != nil {
			return fmt.Errorf("failed to grant scanner privileges: %v", err)
		}
	} else {
		LogStep("  Scanner namespace already exists, reusing it")
	}

	// Check if scanner pod already exists AND is ready
	existingPod, err := cs.CoreV1Interface.Pods(SharedScannerNamespace).Get(ctx, ScannerPodName, metav1.GetOptions{})
	if err == nil {
		// Pod exists, but check if it's actually Ready and fully built
		podReady := false
		for _, cond := range existingPod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				podReady = true
				break
			}
		}

		if podReady {
			// Verify scanner binary exists (pod could be Ready but build not complete)
			cmd := osexec.Command("oc", "exec", "-n", SharedScannerNamespace, ScannerPodName, "--", "test", "-f", "/usr/local/bin/tls-scanner")
			if cmd.Run() == nil {
				LogStep("  Scanner pod already exists and is ready, reusing it")
				return nil
			}
			LogStep("  Scanner pod exists but build incomplete, will recreate")
		} else {
			LogStep("  Scanner pod exists but not ready (may be Terminating), will wait for deletion and recreate")
		}

		// Pod exists but not ready - delete it and create fresh
		LogStep("  Deleting existing pod...")
		_ = cs.CoreV1Interface.Pods(SharedScannerNamespace).Delete(ctx, ScannerPodName, metav1.DeleteOptions{})

		// Wait for pod to be fully deleted
		LogStep("  Waiting for pod deletion to complete...")
		_ = wait.PollUntilContextTimeout(ctx, 2*time.Second, 30*time.Second, true, func(ctx context.Context) (bool, error) {
			_, err := cs.CoreV1Interface.Pods(SharedScannerNamespace).Get(ctx, ScannerPodName, metav1.GetOptions{})
			if err != nil {
				return true, nil // Pod deleted
			}
			return false, nil // Keep waiting
		})
	}

	// Create scanner pod using builder
	scannerPod := buildScannerPodSpec(ScannerPodConfig{
		Name:          ScannerPodName,
		Namespace:     SharedScannerNamespace,
		RestartPolicy: corev1.RestartPolicyAlways,
		Persistent:    true,
		OperatorNS:    "", // Not used for persistent scanner
	})

	_, err = cs.CoreV1Interface.Pods(SharedScannerNamespace).Create(ctx, scannerPod, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create scanner pod: %v", err)
	}

	// Wait for pod to be ready AND for the build script to complete
	LogStep("  Waiting for scanner pod to build and become ready (one-time, ~5-10 minutes)...")
	err = wait.PollUntilContextTimeout(ctx, 5*time.Second, ScannerBuildTimeout, true, func(ctx context.Context) (bool, error) {
		p, err := cs.CoreV1Interface.Pods(SharedScannerNamespace).Get(ctx, ScannerPodName, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}

		// First check if pod is Ready
		podReady := false
		for _, cond := range p.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				podReady = true
				break
			}
		}
		if !podReady {
			return false, nil
		}

		// Pod is Ready, now verify the build script actually finished by checking if scanner binary exists
		cmd := osexec.Command("oc", "exec", "-n", SharedScannerNamespace, ScannerPodName, "--", "test", "-f", "/usr/local/bin/tls-scanner")
		err = cmd.Run()
		if err != nil {
			// Binary doesn't exist yet, keep waiting
			return false, nil
		}

		// Verify testssl.sh also exists
		cmd = osexec.Command("oc", "exec", "-n", SharedScannerNamespace, ScannerPodName, "--", "test", "-f", "/usr/local/bin/testssl.sh")
		err = cmd.Run()
		if err != nil {
			// testssl.sh doesn't exist yet, keep waiting
			return false, nil
		}

		// Both binaries exist, scanner is truly ready
		return true, nil
	})
	if err != nil {
		// Get pod logs for debugging
		logs, _ := getPodLogs(cs, ctx, SharedScannerNamespace, ScannerPodName, nil)
		return fmt.Errorf("scanner pod not ready: %v. Logs: %s", err, logs)
	}

	LogStep("  Shared scanner pod is ready and can be reused for all scenarios")
	return nil
}

// CleanupSharedScanner removes only the scanner pod, keeping the namespace for reuse
func CleanupSharedScanner(cs *testclient.ClientSet, ctx context.Context) {
	LogStep("Cleaning up shared TLS scanner pod...")

	// Delete only the pod, not the namespace
	// The namespace and SCC grants will be reused by subsequent scenarios
	err := cs.CoreV1Interface.Pods(SharedScannerNamespace).Delete(ctx, ScannerPodName, metav1.DeleteOptions{})
	if err != nil {
		LogStep(fmt.Sprintf("  Note: Scanner pod deletion failed (might not exist): %v", err))
	}

	// Wait for pod to be fully deleted
	LogStep("  Waiting for scanner pod to be fully deleted...")
	_ = wait.PollUntilContextTimeout(ctx, 2*time.Second, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		_, err := cs.CoreV1Interface.Pods(SharedScannerNamespace).Get(ctx, ScannerPodName, metav1.GetOptions{})
		if err != nil {
			// Pod doesn't exist anymore (deleted successfully)
			return true, nil
		}
		// Pod still exists, keep waiting
		return false, nil
	})

	LogStep("  Scanner pod cleaned up (namespace kept for reuse)")
}

// CleanupScannerNamespace removes the entire scanner namespace
// This should be called at the very end of all test scenarios
func CleanupScannerNamespace(cs *testclient.ClientSet, ctx context.Context) {
	LogStep("Cleaning up scanner namespace (final cleanup)...")
	_ = cs.CoreV1Interface.Namespaces().Delete(ctx, SharedScannerNamespace, metav1.DeleteOptions{})
}

// RestartScannerPod restarts the shared scanner pod to refresh its Kubernetes API connection.
// This is needed when TLS profile changes (e.g., Baseline → Modern) affect the APIServer's TLS configuration.
// The scanner needs to re-establish connection with new TLS settings.
func RestartScannerPod(cs *testclient.ClientSet, ctx context.Context) error {
	LogStep("  Deleting scanner pod...")

	// Delete the old pod
	err := cs.CoreV1Interface.Pods(SharedScannerNamespace).Delete(ctx, ScannerPodName, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete scanner pod: %v", err)
	}

	// Wait for pod to be fully deleted
	LogStep("  Waiting for pod deletion to complete...")
	err = wait.PollUntilContextTimeout(ctx, 2*time.Second, 1*time.Minute, true, func(ctx context.Context) (bool, error) {
		_, err := cs.CoreV1Interface.Pods(SharedScannerNamespace).Get(ctx, ScannerPodName, metav1.GetOptions{})
		if err != nil {
			// Pod not found = deleted successfully
			return true, nil
		}
		// Pod still exists, keep waiting
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("failed waiting for pod deletion: %v", err)
	}

	// Create a new scanner pod with the same configuration
	LogStep("  Creating new scanner pod...")
	scannerPod := buildScannerPodSpec(ScannerPodConfig{
		Name:          ScannerPodName,
		Namespace:     SharedScannerNamespace,
		RestartPolicy: corev1.RestartPolicyAlways,
		Persistent:    true,
		OperatorNS:    "", // Not used for persistent scanner
	})

	_, err = cs.CoreV1Interface.Pods(SharedScannerNamespace).Create(ctx, scannerPod, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create new scanner pod: %v", err)
	}

	// Wait for new pod to be ready
	LogStep("  Waiting for new scanner pod to build and become ready...")
	err = wait.PollUntilContextTimeout(ctx, 5*time.Second, ScannerBuildTimeout, true, func(ctx context.Context) (bool, error) {
		p, err := cs.CoreV1Interface.Pods(SharedScannerNamespace).Get(ctx, ScannerPodName, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}

		// Check if pod is Ready
		podReady := false
		for _, cond := range p.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				podReady = true
				break
			}
		}
		if !podReady {
			return false, nil
		}

		// Verify the build script completed (scanner binary exists)
		cmd := osexec.Command("oc", "exec", "-n", SharedScannerNamespace, ScannerPodName, "--", "test", "-f", "/usr/local/bin/tls-scanner")
		err = cmd.Run()
		if err != nil {
			return false, nil
		}

		return true, nil
	})

	if err != nil {
		logs, _ := getPodLogs(cs, ctx, SharedScannerNamespace, ScannerPodName, int64Ptr(100))
		return fmt.Errorf("new scanner pod did not become ready: %v. Logs: %s", err, logs)
	}

	LogStep("  Scanner pod restarted successfully")
	return nil
}

// RunScanWithSharedScanner executes a TLS scan using the existing shared scanner pod
func RunScanWithSharedScanner(cs *testclient.ClientSet, ctx context.Context, operatorNS string, tlsVersion string) (bool, string) {
	LogStep(fmt.Sprintf("  Running TLS %s scan (using shared scanner pod, fast)", tlsVersion))
	LogStep("  DEBUG: Checking if scanner pod exists...")

	// First, verify the scanner pod is still running
	pod, err := cs.CoreV1Interface.Pods(SharedScannerNamespace).Get(ctx, ScannerPodName, metav1.GetOptions{})
	if err != nil {
		LogStep(fmt.Sprintf("  DEBUG: Pod Get failed: %v", err))
		return false, fmt.Sprintf("Scanner pod not found: %v. Pod may have crashed or been deleted.", err)
	}
	LogStep(fmt.Sprintf("  DEBUG: Pod found, phase=%s", pod.Status.Phase))
	if pod.Status.Phase != corev1.PodRunning {
		logs, _ := getPodLogs(cs, ctx, SharedScannerNamespace, ScannerPodName, int64Ptr(50))
		LogStep("  DEBUG: Pod not running, returning error")
		return false, fmt.Sprintf("Scanner pod is not running (phase: %s). Logs:\n%s", pod.Status.Phase, logs)
	}

	// Create a unique results directory for this scan
	resultsDir := fmt.Sprintf("/results/scan-%d", time.Now().Unix())
	LogStep(fmt.Sprintf("  DEBUG: Created resultsDir: %s", resultsDir))

	// Execute the scan in the existing scanner pod SYNCHRONOUSLY
	// This is simpler and more reliable than backgrounding
	LogStep("  Starting TLS scan (this will take 2-10 minutes)...")

	// Scan all pods in the namespace
	// Note: Scanner may discover multiple ports and exit with status 1 on connection errors,
	// but we handle this by checking if JSON file exists and parsing results from successful scans
	scanCmd := fmt.Sprintf(`
mkdir -p %s
set +e
/usr/local/bin/tls-scanner \
  --all-pods \
  --namespace-filter %s \
  --artifact-dir %s \
  --json-file scan.json \
  --csv-file scan.csv \
  --log-file scan.log 2>&1
SCAN_EXIT=$?
echo "Scan exit code: $SCAN_EXIT"
echo "Scan completed at $(date)"
`, resultsDir, operatorNS, resultsDir)

	cmd := osexec.Command("oc", "exec", "-n", SharedScannerNamespace, ScannerPodName, "--", "/bin/bash", "-c", scanCmd)

	// Run scan synchronously and wait for it to complete
	LogStep("  Executing scan command (waiting for completion)...")
	output, scanErr := cmd.CombinedOutput()
	LogStep(fmt.Sprintf("  Scan completed. Output length: %d bytes", len(output)))

	// Note: Scanner may return exit 1 for warnings but still produce valid results
	// So we don't fail immediately on non-zero exit - check JSON results first
	if scanErr != nil {
		LogStep(fmt.Sprintf("  Scanner exited with error (will check JSON results): %v", scanErr))
	}

	// Check if JSON file exists first before trying to cat it
	checkCmd := osexec.Command("oc", "exec", "-n", SharedScannerNamespace, ScannerPodName, "--", "test", "-f", fmt.Sprintf("%s/scan.json", resultsDir))
	jsonExists := checkCmd.Run() == nil

	if !jsonExists {
		// JSON file doesn't exist - scanner failed before producing results
		// This can happen if scanner encounters fatal errors (connection refused, etc.)
		// Try to get CSV or log files for debugging
		csvCmd := osexec.Command("oc", "exec", "-n", SharedScannerNamespace, ScannerPodName, "--", "cat", fmt.Sprintf("%s/scan.csv", resultsDir))
		csvOutput, _ := csvCmd.CombinedOutput()
		logCmd := osexec.Command("oc", "exec", "-n", SharedScannerNamespace, ScannerPodName, "--", "cat", fmt.Sprintf("%s/scan.log", resultsDir))
		logOutput, _ := logCmd.CombinedOutput()

		return false, fmt.Sprintf("Scanner did not produce JSON output. This usually means it encountered fatal connection errors on some ports.\nScanner exit status: %v\nScanner output length: %d bytes\nCSV output:\n%s\nLog output:\n%s",
			scanErr, len(output), string(csvOutput), string(logOutput))
	}

	// Get scan results (JSON file exists)
	cmd = osexec.Command("oc", "exec", "-n", SharedScannerNamespace, ScannerPodName, "--", "cat", fmt.Sprintf("%s/scan.json", resultsDir))
	jsonOutput, err := cmd.CombinedOutput()
	if err != nil {
		// Unexpected - JSON file exists but can't read it
		logs, _ := getPodLogs(cs, ctx, SharedScannerNamespace, ScannerPodName, int64Ptr(50))
		return false, fmt.Sprintf("JSON file exists but failed to read it: %v. Output: %s. Logs:\n%s", err, string(jsonOutput), logs)
	}
	output = jsonOutput // Use JSON output for parsing

	// Parse and analyze scan results using helper
	// Pass namespace to analyzeTLSScanResults so it can verify expected ports and check all discovered endpoints
	success, message, err := analyzeTLSScanResults(output, tlsVersion, operatorNS)
	if err != nil {
		return false, fmt.Sprintf("Failed to analyze scan results: %v", err)
	}

	if success {
		LogStep(fmt.Sprintf("  TLS %s is supported on all discovered endpoints in %s", tlsVersion, operatorNS))
	} else {
		LogStep(fmt.Sprintf("  TLS %s support check failed in %s", tlsVersion, operatorNS))
	}

	return success, message
}
