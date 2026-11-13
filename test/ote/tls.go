package ote

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/util/retry"

	configv1 "github.com/openshift/api/config/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned"
	machineconfigclient "github.com/openshift/client-go/machineconfiguration/clientset/versioned"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	e2e "k8s.io/kubernetes/test/e2e/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

const (
	MCPRolloutCompleteTimeout = 120 * time.Minute
	NodeStabilityTimeout      = 90 * time.Minute
	OperatorSettleTimeMinutes = 60
	APIServerOperatorTimeout  = 10 * time.Minute
)

var _ = Describe("[sig-network] TLS Profile Compliance", func() {
	t := newTLSTestContext()

	It("should verify TLS compliance across Modern and Intermediate profiles with different adherence policies [Disruptive][Timeout:300m]",
		Label("Disruptive"), func(ctx context.Context) {
			By("Configuring and verifying Modern TLS Profile with LegacyAdheringComponentsOnly")
			t.configureTLSProfileWithAdherence(ctx, configv1.TLSProfileModernType, configv1.TLSAdherencePolicyLegacyAdheringComponentsOnly)
			t.verifyAllComponentsCompliance(ctx)

			By("Configuring and verifying Modern TLS Profile with StrictAllComponents")
			t.configureTLSProfileWithAdherence(ctx, configv1.TLSProfileModernType, configv1.TLSAdherencePolicyStrictAllComponents)
			t.verifyAllComponentsCompliance(ctx)

			By("Configuring and verifying Intermediate TLS Profile with StrictAllComponents")
			t.configureTLSProfileWithAdherence(ctx, configv1.TLSProfileIntermediateType, configv1.TLSAdherencePolicyStrictAllComponents)
			t.verifyAllComponentsCompliance(ctx)
		})
})

type TLSTestContext struct {
	oc                  *CLI
	k8sClient           kubernetes.Interface
	configClient        configv1client.Interface
	machineConfigClient machineconfigclient.Interface
}

func newTLSTestContext() *TLSTestContext {
	t := &TLSTestContext{}

	BeforeEach(func(ctx context.Context) {
		t.oc = NewCLIWithPodSecurityLevel("networking-tls", admissionapi.LevelPrivileged)
		// TLS tests operate cluster-wide, no namespace needed

		isOpenShift, err := isOpenShiftCluster(ctx, t.oc)
		Expect(err).NotTo(HaveOccurred(), "Failed to check if cluster is OpenShift")
		if !isOpenShift {
			Skip("TLS Profile Compliance testing requires OpenShift cluster with config.openshift.io APIs")
		}

		config, err := t.oc.getConfig()
		Expect(err).NotTo(HaveOccurred(), "Failed to get kubeconfig")

		t.k8sClient, err = kubernetes.NewForConfig(config)
		Expect(err).NotTo(HaveOccurred(), "Failed to create Kubernetes client")

		t.configClient, err = configv1client.NewForConfig(config)
		Expect(err).NotTo(HaveOccurred(), "Failed to create Config client")

		t.machineConfigClient, err = machineconfigclient.NewForConfig(config)
		Expect(err).NotTo(HaveOccurred(), "Failed to create MachineConfig client")
	})

	return t
}

// awaitTLSConfigStability waits for operators to stabilize after TLS configuration change
func (t *TLSTestContext) awaitTLSConfigStability(ctx context.Context) {
	t.awaitAPIServerOperatorStable(ctx)
	t.awaitOperatorsSettled(ctx)

	e2e.Logf("All cluster operators settled")
}

func (t *TLSTestContext) verifyAllComponentsCompliance(ctx context.Context) {
	e2e.Logf("Starting TLS compliance checks with retry (timeout: %v, interval: %v)", 5*time.Minute, 30*time.Second)
	Eventually(func(g Gomega, ctx context.Context) {
		By("Testing TLS compliance for multus-admission-controller in openshift-multus (ports 6443 & 8443)")
		t.verifyMultusTLSComplianceInPod(ctx, g)

		By("Testing TLS compliance for ovn-kubernetes nodes in openshift-ovn-kubernetes (ports 9103, 9105)")
		t.verifyOVNKubernetesNodeTLSComplianceInPod(ctx, g)

		By("Testing TLS compliance for cluster-network-operator in openshift-network-operator (ports 9104, 9103, 9105)")
		t.verifyCNOTLSComplianceInPod(ctx, g)

		By("Testing TLS compliance for networking-console-plugin in openshift-network-console (port 9443)")
		t.verifyNetworkConsoleTLSComplianceInPod(ctx, g)

		e2e.Logf("All TLS compliance checks passed")
	}).WithContext(ctx).WithTimeout(5 * time.Minute).WithPolling(10 * time.Second).Should(Succeed())
}

func (t *TLSTestContext) configureTLSProfileWithAdherence(ctx context.Context, tlsProfileType configv1.TLSProfileType, tlsAdherencePolicy configv1.TLSAdherencePolicy) {
	e2e.Logf("=== Configuring %s TLS Profile with %s ===", tlsProfileType, tlsAdherencePolicy)

	requiresMCPRollout := ((tlsProfileType == configv1.TLSProfileModernType &&
		tlsAdherencePolicy == configv1.TLSAdherencePolicyLegacyAdheringComponentsOnly) ||
		(tlsProfileType == configv1.TLSProfileIntermediateType &&
			tlsAdherencePolicy == configv1.TLSAdherencePolicyStrictAllComponents))

	// Capture baseline MCP timestamps BEFORE making the change to avoid race condition
	var mcpBaselines map[string]time.Time
	if requiresMCPRollout {
		var err error
		mcpBaselines, err = GetMCPUpdatedTimestamps(ctx, t.machineConfigClient)
		Expect(err).NotTo(HaveOccurred(), "failed to capture pre-update MCP timestamps")
		e2e.Logf("Captured baseline MCP update times for %d pools", len(mcpBaselines))
	}

	e2e.Logf("Configuring APIServer with %s TLS profile and tlsAdherence=%s", tlsProfileType, tlsAdherencePolicy)
	t.patchAPIServerTLSProfile(ctx, tlsProfileType, tlsAdherencePolicy)
	e2e.Logf("APIServer TLS profile configured successfully")

	if requiresMCPRollout {
		AwaitAllMCPsUpdatedSince(ctx, t.machineConfigClient, mcpBaselines, MCPRolloutCompleteTimeout)

		t.awaitNodeStability(ctx)
		t.awaitTLSConfigStability(ctx)
	} else {
		// No MCP rollout path
		t.awaitTLSConfigStability(ctx)
		e2e.Logf("TLS configuration applied (no MCP rollout required for this profile)")
	}

	e2e.Logf("Verifying APIServer TLS profile configuration")
	apiserver, err := t.configClient.ConfigV1().APIServers().Get(ctx, "cluster", metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred(), "failed to get APIServer")

	Expect(apiserver.Spec.TLSSecurityProfile).NotTo(BeNil(), "APIServer TLSSecurityProfile is nil")
	Expect(apiserver.Spec.TLSSecurityProfile.Type).To(Equal(tlsProfileType), "APIServer TLS profile mismatch")
	Expect(apiserver.Spec.TLSAdherence).To(Equal(tlsAdherencePolicy), "APIServer tlsAdherence mismatch")

	if apiserver.Spec.TLSAdherence == "" {
		e2e.Logf("APIServer %s TLS profile and tlsAdherence=<empty> (default) verified", tlsProfileType)
	} else {
		e2e.Logf("APIServer %s TLS profile and tlsAdherence=%s verified", tlsProfileType, apiserver.Spec.TLSAdherence)
	}

	e2e.Logf("=== %s TLS Profile successfully configured and cluster is stable ===", tlsProfileType)
}

func (t *TLSTestContext) patchAPIServerTLSProfile(ctx context.Context, tlsProfileType configv1.TLSProfileType, tlsAdherencePolicy configv1.TLSAdherencePolicy) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		apiserver, err := t.configClient.ConfigV1().APIServers().Get(ctx, "cluster", metav1.GetOptions{})
		if err != nil {
			return err
		}

		switch tlsProfileType {
		case configv1.TLSProfileModernType:
			apiserver.Spec.TLSSecurityProfile = &configv1.TLSSecurityProfile{
				Type:   configv1.TLSProfileModernType,
				Modern: &configv1.ModernTLSProfile{},
			}
		case configv1.TLSProfileIntermediateType:
			apiserver.Spec.TLSSecurityProfile = &configv1.TLSSecurityProfile{
				Type:         configv1.TLSProfileIntermediateType,
				Intermediate: &configv1.IntermediateTLSProfile{},
			}
		case configv1.TLSProfileOldType:
			apiserver.Spec.TLSSecurityProfile = &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileOldType,
				Old:  &configv1.OldTLSProfile{},
			}
		default:
			// Non-retryable error - return it to exit retry loop
			return fmt.Errorf("unsupported TLS profile type: %s (must be Modern, Intermediate, or Old)", tlsProfileType)
		}

		apiserver.Spec.TLSAdherence = tlsAdherencePolicy

		_, err = t.configClient.ConfigV1().APIServers().Update(ctx, apiserver, metav1.UpdateOptions{})
		return err
	})
	Expect(err).NotTo(HaveOccurred(), "failed to update apiserver")
}

func (t *TLSTestContext) awaitNodeStability(ctx context.Context) {
	e2e.Logf("Waiting for all nodes to be ready and stable (timeout: %v)", NodeStabilityTimeout)

	Eventually(func(g Gomega, ctx context.Context) {
		nodes, err := t.k8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		g.Expect(err).NotTo(HaveOccurred())

		notReady := []string{}
		for _, node := range nodes.Items {
			_, ready := getNodeReadyTime(&node)
			if !ready {
				notReady = append(notReady, node.Name)
			}
		}

		g.Expect(notReady).To(BeEmpty(), "Nodes are not ready: "+strings.Join(notReady, ", "))

		e2e.Logf("All %d nodes are ready", len(nodes.Items))
	}).WithContext(ctx).WithTimeout(NodeStabilityTimeout).WithPolling(10 * time.Second).Should(Succeed())

	e2e.Logf("All nodes are ready and stable")
}

func getNodeReadyTime(node *corev1.Node) (metav1.Time, bool) {
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			if cond.Status == corev1.ConditionTrue {
				return cond.LastTransitionTime, true
			}
			return metav1.Time{}, false
		}
	}
	return metav1.Time{}, false
}

func (t *TLSTestContext) awaitOperatorsSettled(ctx context.Context) {
	// NOTE: Theoretically racy (same as MCP rollout), but acceptable in practice because:
	// - awaitAPIServerOperatorStable() runs first with 10-minute timeout
	// - TLS changes force pod restarts in kube-apiserver/openshift-apiserver (takes minutes)
	// - Other operators depend on API servers, creating cascading delays
	timeout := time.Duration(OperatorSettleTimeMinutes) * time.Minute

	e2e.Logf("Waiting for cluster operators to settle (timeout: %v)", timeout)

	Eventually(func(g Gomega, ctx context.Context) {
		coList, err := t.configClient.ConfigV1().ClusterOperators().List(ctx, metav1.ListOptions{})
		g.Expect(err).NotTo(HaveOccurred())

		unsettled := []string{}

		for _, co := range coList.Items {
			available := getConditionStatus(co.Status.Conditions, configv1.OperatorAvailable)
			degraded := getConditionStatus(co.Status.Conditions, configv1.OperatorDegraded)
			progressing := getConditionStatus(co.Status.Conditions, configv1.OperatorProgressing)

			isSettled := available == configv1.ConditionTrue &&
				degraded == configv1.ConditionFalse &&
				progressing == configv1.ConditionFalse

			if !isSettled {
				unsettled = append(unsettled, fmt.Sprintf("%s(A=%s,D=%s,P=%s)",
					co.Name, available, degraded, progressing))
			}
		}

		g.Expect(unsettled).To(BeEmpty(), "Operators have not settled: "+strings.Join(unsettled, ", "))
	}).WithContext(ctx).WithTimeout(timeout).WithPolling(10 * time.Second).Should(Succeed())

	e2e.Logf("All operators settled")
}

func getConditionStatus(conditions []configv1.ClusterOperatorStatusCondition, condType configv1.ClusterStatusConditionType) configv1.ConditionStatus {
	for _, cond := range conditions {
		if cond.Type == condType {
			return cond.Status
		}
	}
	return configv1.ConditionUnknown
}

func (t *TLSTestContext) awaitAPIServerOperatorStable(ctx context.Context) {
	e2e.Logf("Waiting for operators to be stable with new TLS configuration (timeout: %v)", APIServerOperatorTimeout)

	relevantOperators := []string{"kube-apiserver", "openshift-apiserver", "network"}

	Eventually(func(g Gomega, ctx context.Context) {
		for _, coName := range relevantOperators {
			co, err := t.configClient.ConfigV1().ClusterOperators().Get(ctx, coName, metav1.GetOptions{})
			g.Expect(err).NotTo(HaveOccurred())

			available := getConditionStatus(co.Status.Conditions, configv1.OperatorAvailable)
			progressing := getConditionStatus(co.Status.Conditions, configv1.OperatorProgressing)
			degraded := getConditionStatus(co.Status.Conditions, configv1.OperatorDegraded)

			g.Expect(available).To(Equal(configv1.ConditionTrue), "Operator %s not available", coName)
			g.Expect(progressing).To(Equal(configv1.ConditionFalse), "Operator %s still progressing", coName)
			g.Expect(degraded).To(Equal(configv1.ConditionFalse), "Operator %s is degraded", coName)
		}
	}).WithContext(ctx).WithTimeout(APIServerOperatorTimeout).WithPolling(10 * time.Second).Should(Succeed())

	e2e.Logf("All TLS-related operators are stable (Available=True, Progressing=False, Degraded=False)")
}

func (t *TLSTestContext) verifyTLSComplianceInPods(
	ctx context.Context,
	g Gomega,
	namespace, labelSelector string,
	ports []string,
	componentName string,
	isAdheringComponent bool,
) {
	apiserver, err := t.configClient.ConfigV1().APIServers().Get(ctx, "cluster", metav1.GetOptions{})
	g.Expect(err).NotTo(HaveOccurred(), "failed to get APIServer config")

	var expectTLS12Reject bool
	var testDescription string

	if apiserver.Spec.TLSSecurityProfile != nil {
		profileType := apiserver.Spec.TLSSecurityProfile.Type
		adherencePolicy := apiserver.Spec.TLSAdherence

		switch {
		case profileType == configv1.TLSProfileModernType && adherencePolicy == configv1.TLSAdherencePolicyStrictAllComponents:
			expectTLS12Reject = true
			testDescription = "Modern + StrictAllComponents: TLS 1.3 only, TLS 1.2 should be rejected"

		case profileType == configv1.TLSProfileModernType && adherencePolicy == configv1.TLSAdherencePolicyLegacyAdheringComponentsOnly:
			expectTLS12Reject = isAdheringComponent
			if isAdheringComponent {
				testDescription = "Modern + LegacyAdheringComponentsOnly (adhering component): TLS 1.3 only, TLS 1.2 should be rejected"
			} else {
				testDescription = "Modern + LegacyAdheringComponentsOnly (non-adhering component): both TLS 1.2 and TLS 1.3 should work"
			}

		case profileType == configv1.TLSProfileIntermediateType && adherencePolicy == configv1.TLSAdherencePolicyStrictAllComponents:
			expectTLS12Reject = false
			testDescription = "Intermediate + StrictAllComponents: both TLS 1.2 and TLS 1.3 should work"

		default:
			expectTLS12Reject = false
			testDescription = fmt.Sprintf("Profile %s with adherence %s: both TLS 1.2 and TLS 1.3 should work", profileType, adherencePolicy)
		}

		e2e.Logf("Testing %s with profile: %s, adherence: %s, adhering: %v", componentName, profileType, adherencePolicy, isAdheringComponent)
		e2e.Logf("%s", testDescription)
	} else {
		expectTLS12Reject = false
		testDescription = "No profile configured: both TLS 1.2 and TLS 1.3 should work"
		e2e.Logf("Testing %s with profile: %s", componentName, configv1.TLSProfileIntermediateType)
		e2e.Logf("%s", testDescription)
	}

	pods, err := t.k8sClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	g.Expect(err).NotTo(HaveOccurred(), "failed to list pods with selector %s", labelSelector)
	g.Expect(pods.Items).NotTo(BeEmpty(), "no pods found with selector %s in namespace %s", labelSelector, namespace)

	var testPod string
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		if slices.ContainsFunc(pod.Status.Conditions, func(condition corev1.PodCondition) bool {
			return condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue
		}) {
			testPod = pod.Name
			break
		}
	}

	g.Expect(testPod).NotTo(BeEmpty(), "no ready pods found with selector %s in namespace %s", labelSelector, namespace)

	for _, port := range ports {
		resourceName := fmt.Sprintf("pod/%s", testPod)
		e2e.Logf("Testing TLS on %s/%s port %s", namespace, resourceName, port)

		err := forwardPortAndExecute(ctx, resourceName, namespace, port, func(localPort int) error {
			//nolint:gosec // G402: InsecureSkipVerify required for testing TLS compliance
			tls13Config := &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, InsecureSkipVerify: true}
			e2e.Logf("Testing TLS 1.3 on port %s (should succeed)", port)
			err := checkTLSConnection(ctx, localPort, tls13Config, nil)
			g.Expect(err).NotTo(HaveOccurred(), "TLS 1.3 test failed on port %s", port)
			e2e.Logf("TLS 1.3 test PASSED on port %s", port)

			//nolint:gosec // G402: InsecureSkipVerify required for testing TLS compliance
			tls12Config := &tls.Config{MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12, InsecureSkipVerify: true}
			if expectTLS12Reject {
				e2e.Logf("Testing TLS 1.2 on port %s (should be REJECTED)", port)
				err := checkTLSConnection(ctx, localPort, tls13Config, tls12Config)
				g.Expect(err).NotTo(HaveOccurred(), "TLS 1.2 rejection check failed on port %s", port)
				e2e.Logf("TLS 1.2 correctly REJECTED on port %s", port)
			} else {
				e2e.Logf("Testing TLS 1.2 on port %s (should succeed)", port)
				err := checkTLSConnection(ctx, localPort, tls12Config, nil)
				g.Expect(err).NotTo(HaveOccurred(), "TLS 1.2 test failed on port %s", port)
				e2e.Logf("TLS 1.2 test PASSED on port %s", port)
			}

			return nil
		})
		g.Expect(err).NotTo(HaveOccurred(), "TLS test failed on port %s", port)
	}

	e2e.Logf("All %s TLS endpoints verified in %s/%s", componentName, namespace, testPod)
}

func (t *TLSTestContext) verifyMultusTLSComplianceInPod(ctx context.Context, g Gomega) {
	t.verifyTLSComplianceInPods(ctx, g, "openshift-multus", "app=multus-admission-controller",
		[]string{"6443", "8443"}, "Multus", true)
}

func (t *TLSTestContext) verifyOVNKubernetesNodeTLSComplianceInPod(ctx context.Context, g Gomega) {
	t.verifyTLSComplianceInPods(ctx, g, "openshift-ovn-kubernetes", "app=ovnkube-node",
		[]string{"9103", "9105"}, "OVN-Kubernetes Node", false)
}

func (t *TLSTestContext) verifyCNOTLSComplianceInPod(ctx context.Context, g Gomega) {
	t.verifyTLSComplianceInPods(ctx, g, "openshift-network-operator", "name=network-operator",
		[]string{"9104", "9103", "9105"}, "CNO", false)
}

func (t *TLSTestContext) verifyNetworkConsoleTLSComplianceInPod(ctx context.Context, g Gomega) {
	t.verifyTLSComplianceInPods(ctx, g, "openshift-network-console", "app.kubernetes.io/name=networking-console-plugin",
		[]string{"9443"}, "Network Console", false)
}

// checkTLSConnection verifies that a TLS connection works with the expected config and fails with the other.
func checkTLSConnection(ctx context.Context, port int, tlsShouldWork, tlsShouldNotWork *tls.Config) error {
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{
			Timeout: 10 * time.Second,
		},
		Config: tlsShouldWork,
	}

	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort("localhost", strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("should work: %w", err)
	}
	err = conn.Close()
	if err != nil {
		return fmt.Errorf("failed to close connection: %w", err)
	}

	if tlsShouldNotWork == nil {
		return nil
	}

	negDialer := &tls.Dialer{
		NetDialer: &net.Dialer{
			Timeout: 10 * time.Second,
		},
		Config: tlsShouldNotWork,
	}

	conn, err = negDialer.DialContext(ctx, "tcp", net.JoinHostPort("localhost", strconv.Itoa(port)))
	if err == nil {
		return fmt.Errorf("should not work: connection unexpectedly succeeded, closing conn status: %w", conn.Close())
	}
	if !strings.Contains(err.Error(), "protocol version") &&
		!strings.Contains(err.Error(), "no supported versions satisfy") &&
		!strings.Contains(err.Error(), "handshake failure") {
		return fmt.Errorf("should not work: got error, but not a TLS version mismatch: %w", err)
	}
	return nil
}
