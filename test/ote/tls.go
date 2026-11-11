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
	MCPRolloutStartTimeout    = 10 * time.Minute
	MCPRolloutCompleteTimeout = 120 * time.Minute
	NodeStabilityTimeout      = 90 * time.Minute
	OperatorSettleTimeMinutes = 60
	APIServerOperatorTimeout  = 10 * time.Minute
)

var _ = Describe("[sig-network] TLS Profile Compliance", func() {
	t := newTLSTestContext()

	It("should verify TLS compliance across Modern and Intermediate profiles with different adherence policies",
		Label("Disruptive"), func(ctx context.Context) {
			// Scenario 1: Modern TLS Profile with LegacyAdheringComponentsOnly
			By("Configuring and verifying Modern TLS Profile with LegacyAdheringComponentsOnly")
			t.configureTLSProfileWithAdherence(ctx, configv1.TLSProfileModernType, configv1.TLSAdherencePolicyLegacyAdheringComponentsOnly)
			t.verifyAllComponentsCompliance(ctx)

			// Scenario 2: Modern TLS Profile with StrictAllComponents
			By("Configuring and verifying Modern TLS Profile with StrictAllComponents")
			t.configureTLSProfileWithAdherence(ctx, configv1.TLSProfileModernType, configv1.TLSAdherencePolicyStrictAllComponents)
			t.verifyAllComponentsCompliance(ctx)

			// Scenario 3: Intermediate TLS Profile with StrictAllComponents
			By("Configuring and verifying Intermediate TLS Profile with StrictAllComponents")
			t.configureTLSProfileWithAdherence(ctx, configv1.TLSProfileIntermediateType, configv1.TLSAdherencePolicyStrictAllComponents)
			t.verifyAllComponentsCompliance(ctx)
		})

	It("should verify TLS group preferences compliance with restricted groups (secp521r1, secp256r1) across networking components",
		Label("Disruptive"), func(ctx context.Context) {
			groups := []configv1.TLSGroup{configv1.TLSGroupSecP521r1, configv1.TLSGroupSecP256r1}
			rejectedGroup := configv1.TLSGroupSecP384r1

			// Setup: Enable feature gate and configure APIServer (done once for all scenarios)
			By("Enabling TLSGroupPreferences feature gate")
			alreadyEnabled, err := t.enableTLSGroupPreferencesFeatureGate(ctx)
			Expect(err).NotTo(HaveOccurred(), "Failed to enable TLSGroupPreferences feature gate")
			if alreadyEnabled {
				e2e.Logf("TLSGroupPreferences feature gate was already enabled")
			}

			By("Configuring APIServer with custom TLS profile and groups")
			updated, err := t.configureAPIServerWithTLSGroups(ctx, groups)
			Expect(err).NotTo(HaveOccurred(), "Failed to configure APIServer with TLS groups")

			// Scenario 1: Verify Multus TLS group compliance
			By("Verifying Multus TLS group compliance")
			if updated {
				By("Restarting Multus admission controller pods to pick up new TLS configuration")
				err = t.restartMultusPods(ctx)
				Expect(err).NotTo(HaveOccurred(), "Failed to restart Multus pods")
			}
			err = t.verifyMultusTLSGroups(ctx, groups, rejectedGroup)
			Expect(err).NotTo(HaveOccurred(), "Multus TLS group preferences verification failed")
			e2e.Logf("Multus TLS group preferences compliance verified")

			// Scenario 2: Verify OVN-Kubernetes TLS group compliance
			By("Verifying OVN-Kubernetes TLS group compliance")
			if updated {
				By("Restarting OVN-Kubernetes node pods to pick up new TLS configuration")
				err = t.restartOVNKubernetesPods(ctx)
				Expect(err).NotTo(HaveOccurred(), "Failed to restart OVN-Kubernetes pods")
			}
			err = t.verifyOVNKubernetesTLSGroups(ctx, groups, rejectedGroup)
			Expect(err).NotTo(HaveOccurred(), "OVN-Kubernetes TLS group preferences verification failed")
			e2e.Logf("OVN-Kubernetes TLS group preferences compliance verified")

			// Scenario 3: Verify CNO TLS group compliance
			By("Verifying CNO TLS group compliance")
			if updated {
				By("Restarting CNO pods to pick up new TLS configuration")
				err = t.restartCNOPods(ctx)
				Expect(err).NotTo(HaveOccurred(), "Failed to restart CNO pods")
			}
			err = t.verifyCNOTLSGroups(ctx, groups, rejectedGroup)
			Expect(err).NotTo(HaveOccurred(), "CNO TLS group preferences verification failed")
			e2e.Logf("CNO TLS group preferences compliance verified")

			// Scenario 4: Verify Network Console TLS group compliance
			By("Verifying Network Console TLS group compliance")
			if updated {
				By("Restarting Network Console pods to pick up new TLS configuration")
				err = t.restartNetworkConsolePods(ctx)
				Expect(err).NotTo(HaveOccurred(), "Failed to restart Network Console pods")
			}
			err = t.verifyNetworkConsoleTLSGroups(ctx, groups, rejectedGroup)
			Expect(err).NotTo(HaveOccurred(), "Network Console TLS group preferences verification failed")
			e2e.Logf("Network Console TLS group preferences compliance verified")

			e2e.Logf("All TLS group preferences compliance tests passed")
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
		// Test multus-cni
		By("Testing TLS compliance for multus-admission-controller in openshift-multus (ports 6443 & 8443)")
		t.verifyMultusTLSComplianceInPod(ctx, g)

		// Test ovn-kubernetes nodes
		By("Testing TLS compliance for ovn-kubernetes nodes in openshift-ovn-kubernetes (ports 9103, 9105)")
		t.verifyOVNKubernetesNodeTLSComplianceInPod(ctx, g)

		// Test cluster-network-operator
		By("Testing TLS compliance for cluster-network-operator in openshift-network-operator (ports 9104, 9103, 9105)")
		t.verifyCNOTLSComplianceInPod(ctx, g)

		// Test networking-console-plugin
		By("Testing TLS compliance for networking-console-plugin in openshift-network-console (port 9443)")
		t.verifyNetworkConsoleTLSComplianceInPod(ctx, g)

		e2e.Logf("All TLS compliance checks passed")
	}).WithContext(ctx).WithTimeout(5 * time.Minute).WithPolling(10 * time.Second).Should(Succeed())
}

func (t *TLSTestContext) configureTLSProfileWithAdherence(ctx context.Context, tlsProfileType configv1.TLSProfileType, tlsAdherencePolicy configv1.TLSAdherencePolicy) {
	e2e.Logf("=== Configuring %s TLS Profile with %s ===", tlsProfileType, tlsAdherencePolicy)

	e2e.Logf("Configuring APIServer with %s TLS profile and tlsAdherence=%s", tlsProfileType, tlsAdherencePolicy)
	t.patchAPIServerTLSProfile(ctx, tlsProfileType, tlsAdherencePolicy)
	e2e.Logf("APIServer TLS profile configured successfully")

	requiresMCPRollout := ((tlsProfileType == configv1.TLSProfileModernType &&
		tlsAdherencePolicy == configv1.TLSAdherencePolicyLegacyAdheringComponentsOnly) ||
		(tlsProfileType == configv1.TLSProfileIntermediateType &&
			tlsAdherencePolicy == configv1.TLSAdherencePolicyStrictAllComponents))

	if requiresMCPRollout {
		AwaitMCPRolloutStart(ctx, t.machineConfigClient, MCPRolloutStartTimeout)
		AwaitAllMCPsComplete(ctx, t.machineConfigClient, MCPRolloutCompleteTimeout)

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
	timeout := time.Duration(OperatorSettleTimeMinutes) * time.Minute

	e2e.Logf("Waiting for cluster operators to settle (timeout: %v)", timeout)

	Eventually(func(g Gomega, ctx context.Context) {
		coList, err := t.configClient.ConfigV1().ClusterOperators().List(ctx, metav1.ListOptions{})
		g.Expect(err).NotTo(HaveOccurred())

		unsettled := []string{}

		for _, co := range coList.Items {
			available := getCondition(co.Status.Conditions, configv1.OperatorAvailable)
			degraded := getCondition(co.Status.Conditions, configv1.OperatorDegraded)
			progressing := getCondition(co.Status.Conditions, configv1.OperatorProgressing)

			isSettled := available.Status == configv1.ConditionTrue &&
				degraded.Status == configv1.ConditionFalse &&
				progressing.Status == configv1.ConditionFalse

			if !isSettled {
				unsettled = append(unsettled, fmt.Sprintf("%s(A=%s,D=%s,P=%s)",
					co.Name, available.Status, degraded.Status, progressing.Status))
			}
		}

		g.Expect(unsettled).To(BeEmpty(), "Operators have not settled: "+strings.Join(unsettled, ", "))
	}).WithContext(ctx).WithTimeout(timeout).WithPolling(10 * time.Second).Should(Succeed())

	e2e.Logf("All operators settled")
}

func getCondition(conditions []configv1.ClusterOperatorStatusCondition, condType configv1.ClusterStatusConditionType) configv1.ClusterOperatorStatusCondition {
	for _, cond := range conditions {
		if cond.Type == condType {
			return cond
		}
	}
	return configv1.ClusterOperatorStatusCondition{
		Type:   condType,
		Status: configv1.ConditionUnknown,
	}
}

func (t *TLSTestContext) awaitAPIServerOperatorStable(ctx context.Context) {
	e2e.Logf("Waiting for operators to be stable with new TLS configuration (timeout: %v)", APIServerOperatorTimeout)

	relevantOperators := []string{"kube-apiserver", "openshift-apiserver", "network"}

	Eventually(func(g Gomega, ctx context.Context) {
		for _, coName := range relevantOperators {
			co, err := t.configClient.ConfigV1().ClusterOperators().Get(ctx, coName, metav1.GetOptions{})
			g.Expect(err).NotTo(HaveOccurred())

			available := getCondition(co.Status.Conditions, configv1.OperatorAvailable)
			progressing := getCondition(co.Status.Conditions, configv1.OperatorProgressing)
			degraded := getCondition(co.Status.Conditions, configv1.OperatorDegraded)

			g.Expect(available.Status).To(Equal(configv1.ConditionTrue), "Operator %s not available", coName)
			g.Expect(progressing.Status).To(Equal(configv1.ConditionFalse), "Operator %s still progressing", coName)
			g.Expect(degraded.Status).To(Equal(configv1.ConditionFalse), "Operator %s is degraded", coName)
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
	skipTLS12ForModernStrict bool,
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

			if skipTLS12ForModernStrict &&
				apiserver.Spec.TLSSecurityProfile != nil &&
				apiserver.Spec.TLSSecurityProfile.Type == configv1.TLSProfileModernType &&
				apiserver.Spec.TLSAdherence == configv1.TLSAdherencePolicyStrictAllComponents {
				e2e.Logf("Skipping TLS 1.2 test for %s on port %s (Modern + StrictAllComponents)", componentName, port)
			} else {
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
			}

			return nil
		})
		g.Expect(err).NotTo(HaveOccurred(), "TLS test failed on port %s", port)
	}

	e2e.Logf("All %s TLS endpoints verified in %s/%s", componentName, namespace, testPod)
}

func (t *TLSTestContext) verifyMultusTLSComplianceInPod(ctx context.Context, g Gomega) {
	t.verifyTLSComplianceInPods(ctx, g, "openshift-multus", "app=multus-admission-controller",
		[]string{"6443", "8443"}, "Multus", true, false)
}

func (t *TLSTestContext) verifyOVNKubernetesNodeTLSComplianceInPod(ctx context.Context, g Gomega) {
	t.verifyTLSComplianceInPods(ctx, g, "openshift-ovn-kubernetes", "app=ovnkube-node",
		[]string{"9103", "9105"}, "OVN-Kubernetes Node", false, false)
}

func (t *TLSTestContext) verifyCNOTLSComplianceInPod(ctx context.Context, g Gomega) {
	t.verifyTLSComplianceInPods(ctx, g, "openshift-network-operator", "name=network-operator",
		[]string{"9104", "9103", "9105"}, "CNO", false, false)
}

func (t *TLSTestContext) verifyNetworkConsoleTLSComplianceInPod(ctx context.Context, g Gomega) {
	t.verifyTLSComplianceInPods(ctx, g, "openshift-network-console", "app.kubernetes.io/name=networking-console-plugin",
		[]string{"9443"}, "Network Console", false, false)
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

// enableTLSGroupPreferencesFeatureGate enables the TLSGroupPreferences feature gate.
// Returns true if the feature was already enabled.
func (t *TLSTestContext) enableTLSGroupPreferencesFeatureGate(ctx context.Context) (bool, error) {
	featureName := "TLSGroupPreferences"
	e2e.Logf("Checking and enabling %s feature gate", featureName)

	fg, err := t.configClient.ConfigV1().FeatureGates().Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to get featuregate: %w", err)
	}

	// Check if already enabled
	if fg.Spec.FeatureSet == configv1.CustomNoUpgrade && fg.Spec.CustomNoUpgrade != nil {
		for _, enabled := range fg.Spec.CustomNoUpgrade.Enabled {
			if string(enabled) == featureName {
				e2e.Logf("%s feature gate is already enabled", featureName)
				return true, nil
			}
		}
	}

	e2e.Logf("Enabling %s feature gate", featureName)
	// Add the feature to existing features
	if fg.Spec.FeatureSet == configv1.CustomNoUpgrade && fg.Spec.CustomNoUpgrade != nil {
		fg.Spec.CustomNoUpgrade.Enabled = append(fg.Spec.CustomNoUpgrade.Enabled, configv1.FeatureGateName(featureName))
	} else {
		fg.Spec.FeatureSet = configv1.CustomNoUpgrade
		fg.Spec.CustomNoUpgrade = &configv1.CustomFeatureGates{
			Enabled: []configv1.FeatureGateName{configv1.FeatureGateName(featureName)},
		}
	}

	_, err = t.configClient.ConfigV1().FeatureGates().Update(ctx, fg, metav1.UpdateOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to update featuregate: %w", err)
	}

	// Wait for cluster stabilization
	e2e.Logf("Waiting for cluster to stabilize after enabling feature gate")
	AwaitMCPRolloutStart(ctx, t.machineConfigClient, MCPRolloutStartTimeout)
	AwaitAllMCPsComplete(ctx, t.machineConfigClient, MCPRolloutCompleteTimeout)
	t.awaitNodeStability(ctx)
	t.awaitTLSConfigStability(ctx)

	return false, nil
}

// configureAPIServerWithTLSGroups configures the APIServer with custom TLS groups.
// Returns true if APIServer was updated, false if already configured.
func (t *TLSTestContext) configureAPIServerWithTLSGroups(ctx context.Context, tlsGroups []configv1.TLSGroup) (bool, error) {
	e2e.Logf("Configuring APIServer with TLS groups: %v", tlsGroups)

	apiserver, err := t.configClient.ConfigV1().APIServers().Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to get apiserver: %w", err)
	}

	// Check if already configured (idempotent)
	if apiserver.Spec.TLSSecurityProfile != nil &&
		apiserver.Spec.TLSSecurityProfile.Type == configv1.TLSProfileCustomType &&
		apiserver.Spec.TLSSecurityProfile.Custom != nil &&
		slices.Equal(apiserver.Spec.TLSSecurityProfile.Custom.Groups, tlsGroups) &&
		apiserver.Spec.TLSAdherence == configv1.TLSAdherencePolicyStrictAllComponents {
		e2e.Logf("APIServer TLS groups already configured, no update needed")
		return false, nil
	}

	// Default ciphers that satisfy OpenShift requirements
	ciphers := []string{
		"TLS_AES_128_GCM_SHA256",
		"TLS_AES_256_GCM_SHA384",
		"TLS_CHACHA20_POLY1305_SHA256",
		"ECDHE-ECDSA-AES128-GCM-SHA256",
		"ECDHE-RSA-AES128-GCM-SHA256",
		"ECDHE-ECDSA-AES256-GCM-SHA384",
		"ECDHE-RSA-AES256-GCM-SHA384",
	}

	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		apiserver, err := t.configClient.ConfigV1().APIServers().Get(ctx, "cluster", metav1.GetOptions{})
		if err != nil {
			return err
		}

		apiserver.Spec.TLSSecurityProfile = &configv1.TLSSecurityProfile{
			Type: configv1.TLSProfileCustomType,
			Custom: &configv1.CustomTLSProfile{
				TLSProfileSpec: configv1.TLSProfileSpec{
					MinTLSVersion: configv1.VersionTLS12,
					Ciphers:       ciphers,
					Groups:        tlsGroups,
				},
			},
		}
		apiserver.Spec.TLSAdherence = configv1.TLSAdherencePolicyStrictAllComponents

		_, err = t.configClient.ConfigV1().APIServers().Update(ctx, apiserver, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return false, fmt.Errorf("failed to update apiserver: %w", err)
	}

	e2e.Logf("APIServer TLS groups updated successfully")
	e2e.Logf("Waiting for cluster to stabilize after TLS configuration")

	// Wait for cluster stabilization
	AwaitMCPRolloutStart(ctx, t.machineConfigClient, MCPRolloutStartTimeout)
	AwaitAllMCPsComplete(ctx, t.machineConfigClient, MCPRolloutCompleteTimeout)
	t.awaitNodeStability(ctx)
	t.awaitTLSConfigStability(ctx)

	return true, nil
}

// restartMultusPods restarts Multus admission controller pods.
func (t *TLSTestContext) restartMultusPods(ctx context.Context) error {
	namespace := "openshift-multus"
	labelSelector := "app=multus-admission-controller"
	e2e.Logf("Restarting Multus pods with selector %s in namespace %s", labelSelector, namespace)

	pods, err := t.k8sClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}

	oldPodUIDs := make(map[string]bool)
	for _, pod := range pods.Items {
		oldPodUIDs[string(pod.UID)] = true
		e2e.Logf("Deleting pod %s (UID: %s)", pod.Name, pod.UID)
		err := t.k8sClient.CoreV1().Pods(namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{
			GracePeriodSeconds: new(int64), // 0 grace period
		})
		if err != nil {
			e2e.Logf("Warning: failed to delete pod %s: %v", pod.Name, err)
		}
	}

	e2e.Logf("Waiting for new Multus pods to be ready...")
	Eventually(func(g Gomega, ctx context.Context) {
		newPods, err := t.k8sClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: labelSelector,
		})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(newPods.Items).NotTo(BeEmpty(), "No pods found after restart")

		allNewAndReady := true
		for _, pod := range newPods.Items {
			if oldPodUIDs[string(pod.UID)] {
				allNewAndReady = false
				continue
			}

			if pod.Status.Phase != corev1.PodRunning {
				allNewAndReady = false
			}

			ready := slices.ContainsFunc(pod.Status.Conditions, func(condition corev1.PodCondition) bool {
				return condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue
			})
			if !ready {
				allNewAndReady = false
			}
		}

		g.Expect(allNewAndReady).To(BeTrue(), "Not all new Multus pods are ready")
	}).WithContext(ctx).WithTimeout(5 * time.Minute).WithPolling(10 * time.Second).Should(Succeed())

	e2e.Logf("All new Multus pods are ready")
	return nil
}

// verifyMultusTLSGroups verifies TLS group preferences for Multus admission controller.
func (t *TLSTestContext) verifyMultusTLSGroups(ctx context.Context, allowedGroups []configv1.TLSGroup, rejectedGroup configv1.TLSGroup) error {
	namespace := "openshift-multus"
	labelSelector := "app=multus-admission-controller"

	e2e.Logf("Verifying Multus TLS groups (allowed: %v, rejected: %s)", allowedGroups, rejectedGroup)

	// Get a running Multus pod
	pods, err := t.k8sClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return fmt.Errorf("failed to list Multus pods: %w", err)
	}

	var testPod string
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning {
			if slices.ContainsFunc(pod.Status.Conditions, func(condition corev1.PodCondition) bool {
				return condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue
			}) {
				testPod = pod.Name
				break
			}
		}
	}

	if testPod == "" {
		return fmt.Errorf("no ready Multus pod found")
	}

	e2e.Logf("Testing TLS groups using pod %s/%s", namespace, testPod)

	// Test both containers and both ports
	testCases := []struct {
		container string
		port      string
	}{
		{"multus-admission-controller", "6443"},
		{"kube-rbac-proxy", "8443"},
	}

	// Helper to get expected output name for a group
	getExpectedOutputName := func(group configv1.TLSGroup) string {
		if group == configv1.TLSGroupSecP256r1 {
			return "prime256v1" // openssl outputs "prime256v1" for secp256r1
		}
		return string(group)
	}

	for _, tc := range testCases {
		e2e.Logf("Testing container %s on port %s", tc.container, tc.port)

		// Test each allowed group
		for _, group := range allowedGroups {
			e2e.Logf("  Testing %s (should succeed)", group)
			output, err := t.execOpensslInPod(ctx, namespace, testPod, tc.container, tc.port, string(group))
			if err != nil {
				return fmt.Errorf("%s on port %s: %s handshake failed: %w\nOutput: %s", tc.container, tc.port, group, err, output)
			}

			if !strings.Contains(output, "CONNECTION ESTABLISHED") {
				return fmt.Errorf("%s on port %s: %s expected CONNECTION ESTABLISHED\nOutput: %s", tc.container, tc.port, group, output)
			}

			expectedName := getExpectedOutputName(group)
			if !strings.Contains(output, expectedName) {
				return fmt.Errorf("%s on port %s: %s expected Peer Temp Key with %s\nOutput: %s", tc.container, tc.port, group, expectedName, output)
			}

			e2e.Logf("  ✓ %s handshake succeeded with %s", group, expectedName)
		}

		// Test rejected group (should fail)
		e2e.Logf("  Testing %s (should fail)", rejectedGroup)
		output, err := t.execOpensslInPod(ctx, namespace, testPod, tc.container, tc.port, string(rejectedGroup))
		if err == nil {
			return fmt.Errorf("%s on port %s: %s expected handshake to fail but succeeded\nOutput: %s", tc.container, tc.port, rejectedGroup, output)
		}

		if !strings.Contains(output, "handshake failure") && !strings.Contains(output, "alert") {
			return fmt.Errorf("%s on port %s: %s expected handshake failure\nOutput: %s", tc.container, tc.port, rejectedGroup, output)
		}

		e2e.Logf("  ✓ %s handshake correctly rejected", rejectedGroup)
	}

	e2e.Logf("All Multus TLS group compliance tests passed")
	return nil
}

// execOpensslInPod executes openssl s_client in a pod container.
func (t *TLSTestContext) execOpensslInPod(ctx context.Context, namespace, podName, container, port, groups string) (string, error) {
	// Use echo Q to send quit command to openssl
	cmd := fmt.Sprintf("echo Q | timeout 3 openssl s_client -connect localhost:%s -groups %s -brief 2>&1", port, groups)

	e2e.Logf("Executing in %s/%s container %s: %s", namespace, podName, container, cmd)

	output, err := t.oc.AsAdmin().Run("exec").
		Args("-n", namespace, podName, "-c", container, "--", "sh", "-c", cmd).
		Output(ctx)

	if len(output) > 0 {
		e2e.Logf("Output:\n%s", output)
	} else {
		e2e.Logf("Warning: No output received from openssl command")
	}

	// For rejected handshakes, openssl returns an error - this is expected
	if err != nil {
		e2e.Logf("Exec error (may be expected for rejected handshake): %v", err)
		return output, err
	}

	return output, nil
}

// restartOVNKubernetesPods restarts OVN-Kubernetes node pods.
func (t *TLSTestContext) restartOVNKubernetesPods(ctx context.Context) error {
	namespace := "openshift-ovn-kubernetes"
	labelSelector := "app=ovnkube-node"
	e2e.Logf("Restarting OVN-Kubernetes pods with selector %s in namespace %s", labelSelector, namespace)

	pods, err := t.k8sClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}

	oldPodUIDs := make(map[string]bool)
	for _, pod := range pods.Items {
		oldPodUIDs[string(pod.UID)] = true
		e2e.Logf("Deleting pod %s (UID: %s)", pod.Name, pod.UID)
		err := t.k8sClient.CoreV1().Pods(namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{
			GracePeriodSeconds: new(int64), // 0 grace period
		})
		if err != nil {
			e2e.Logf("Warning: failed to delete pod %s: %v", pod.Name, err)
		}
	}

	e2e.Logf("Waiting for new OVN-Kubernetes pods to be ready...")
	Eventually(func(g Gomega, ctx context.Context) {
		newPods, err := t.k8sClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: labelSelector,
		})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(newPods.Items).NotTo(BeEmpty(), "No pods found after restart")

		allNewAndReady := true
		for _, pod := range newPods.Items {
			if oldPodUIDs[string(pod.UID)] {
				allNewAndReady = false
				continue
			}

			if pod.Status.Phase != corev1.PodRunning {
				allNewAndReady = false
			}

			ready := slices.ContainsFunc(pod.Status.Conditions, func(condition corev1.PodCondition) bool {
				return condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue
			})
			if !ready {
				allNewAndReady = false
			}
		}

		g.Expect(allNewAndReady).To(BeTrue(), "Not all new OVN-Kubernetes pods are ready")
	}).WithContext(ctx).WithTimeout(5 * time.Minute).WithPolling(10 * time.Second).Should(Succeed())

	e2e.Logf("All new OVN-Kubernetes pods are ready")
	return nil
}

// verifyOVNKubernetesTLSGroups verifies TLS group preferences for OVN-Kubernetes node.
func (t *TLSTestContext) verifyOVNKubernetesTLSGroups(ctx context.Context, allowedGroups []configv1.TLSGroup, rejectedGroup configv1.TLSGroup) error {
	namespace := "openshift-ovn-kubernetes"
	labelSelector := "app=ovnkube-node"

	e2e.Logf("Verifying OVN-Kubernetes TLS groups (allowed: %v, rejected: %s)", allowedGroups, rejectedGroup)

	// Get a running OVN-Kubernetes pod
	pods, err := t.k8sClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return fmt.Errorf("failed to list OVN-Kubernetes pods: %w", err)
	}

	var testPod string
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning {
			if slices.ContainsFunc(pod.Status.Conditions, func(condition corev1.PodCondition) bool {
				return condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue
			}) {
				testPod = pod.Name
				break
			}
		}
	}

	if testPod == "" {
		return fmt.Errorf("no ready OVN-Kubernetes pod found")
	}

	e2e.Logf("Testing TLS groups using pod %s/%s", namespace, testPod)

	// Test both kube-rbac-proxy containers and their ports
	testCases := []struct {
		container string
		port      string
	}{
		{"kube-rbac-proxy-node", "9103"},
		{"kube-rbac-proxy-ovn-metrics", "9105"},
	}

	// Helper to get expected output name for a group
	getExpectedOutputName := func(group configv1.TLSGroup) string {
		if group == configv1.TLSGroupSecP256r1 {
			return "prime256v1" // openssl outputs "prime256v1" for secp256r1
		}
		return string(group)
	}

	for _, tc := range testCases {
		e2e.Logf("Testing container %s on port %s", tc.container, tc.port)

		// Test each allowed group
		for _, group := range allowedGroups {
			e2e.Logf("  Testing %s (should succeed)", group)
			output, err := t.execOpensslInPod(ctx, namespace, testPod, tc.container, tc.port, string(group))
			if err != nil {
				return fmt.Errorf("%s on port %s: %s handshake failed: %w\nOutput: %s", tc.container, tc.port, group, err, output)
			}

			if !strings.Contains(output, "CONNECTION ESTABLISHED") {
				return fmt.Errorf("%s on port %s: %s expected CONNECTION ESTABLISHED\nOutput: %s", tc.container, tc.port, group, output)
			}

			expectedName := getExpectedOutputName(group)
			if !strings.Contains(output, expectedName) {
				return fmt.Errorf("%s on port %s: %s expected Peer Temp Key with %s\nOutput: %s", tc.container, tc.port, group, expectedName, output)
			}

			e2e.Logf("  ✓ %s handshake succeeded with %s", group, expectedName)
		}

		// Test rejected group (should fail)
		e2e.Logf("  Testing %s (should fail)", rejectedGroup)
		output, err := t.execOpensslInPod(ctx, namespace, testPod, tc.container, tc.port, string(rejectedGroup))
		if err == nil {
			return fmt.Errorf("%s on port %s: %s expected handshake to fail but succeeded\nOutput: %s", tc.container, tc.port, rejectedGroup, output)
		}

		if !strings.Contains(output, "handshake failure") && !strings.Contains(output, "alert") {
			return fmt.Errorf("%s on port %s: %s expected handshake failure\nOutput: %s", tc.container, tc.port, rejectedGroup, output)
		}

		e2e.Logf("  ✓ %s handshake correctly rejected", rejectedGroup)
	}

	e2e.Logf("All OVN-Kubernetes TLS group compliance tests passed")
	return nil
}

// restartCNOPods restarts CNO pods.
func (t *TLSTestContext) restartCNOPods(ctx context.Context) error {
	namespace := "openshift-network-operator"
	labelSelector := "name=network-operator"
	e2e.Logf("Restarting CNO pods with selector %s in namespace %s", labelSelector, namespace)

	pods, err := t.k8sClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}

	oldPodUIDs := make(map[string]bool)
	for _, pod := range pods.Items {
		oldPodUIDs[string(pod.UID)] = true
		e2e.Logf("Deleting pod %s (UID: %s)", pod.Name, pod.UID)
		err := t.k8sClient.CoreV1().Pods(namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{
			GracePeriodSeconds: new(int64), // 0 grace period
		})
		if err != nil {
			e2e.Logf("Warning: failed to delete pod %s: %v", pod.Name, err)
		}
	}

	e2e.Logf("Waiting for new CNO pods to be ready...")
	Eventually(func(g Gomega, ctx context.Context) {
		newPods, err := t.k8sClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: labelSelector,
		})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(newPods.Items).NotTo(BeEmpty(), "No pods found after restart")

		allNewAndReady := true
		for _, pod := range newPods.Items {
			if oldPodUIDs[string(pod.UID)] {
				allNewAndReady = false
				continue
			}

			if pod.Status.Phase != corev1.PodRunning {
				allNewAndReady = false
			}

			ready := slices.ContainsFunc(pod.Status.Conditions, func(condition corev1.PodCondition) bool {
				return condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue
			})
			if !ready {
				allNewAndReady = false
			}
		}

		g.Expect(allNewAndReady).To(BeTrue(), "Not all new CNO pods are ready")
	}).WithContext(ctx).WithTimeout(5 * time.Minute).WithPolling(10 * time.Second).Should(Succeed())

	e2e.Logf("All new CNO pods are ready")
	return nil
}

// verifyCNOTLSGroups verifies TLS group preferences for CNO.
func (t *TLSTestContext) verifyCNOTLSGroups(ctx context.Context, allowedGroups []configv1.TLSGroup, rejectedGroup configv1.TLSGroup) error {
	namespace := "openshift-network-operator"
	labelSelector := "name=network-operator"

	e2e.Logf("Verifying CNO TLS groups (allowed: %v, rejected: %s)", allowedGroups, rejectedGroup)

	// Get a running CNO pod
	pods, err := t.k8sClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return fmt.Errorf("failed to list CNO pods: %w", err)
	}

	var testPod string
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning {
			if slices.ContainsFunc(pod.Status.Conditions, func(condition corev1.PodCondition) bool {
				return condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue
			}) {
				testPod = pod.Name
				break
			}
		}
	}

	if testPod == "" {
		return fmt.Errorf("no ready CNO pod found")
	}

	e2e.Logf("Testing TLS groups using pod %s/%s", namespace, testPod)

	// CNO has network-operator container with kube-rbac-proxy on multiple ports
	testCases := []struct {
		container string
		port      string
	}{
		{"network-operator", "9104"},
		{"network-operator", "9103"},
		{"network-operator", "9105"},
	}

	// Helper to get expected output name for a group
	getExpectedOutputName := func(group configv1.TLSGroup) string {
		if group == configv1.TLSGroupSecP256r1 {
			return "prime256v1" // openssl outputs "prime256v1" for secp256r1
		}
		return string(group)
	}

	for _, tc := range testCases {
		e2e.Logf("Testing container %s on port %s", tc.container, tc.port)

		// Test each allowed group
		for _, group := range allowedGroups {
			e2e.Logf("  Testing %s (should succeed)", group)
			output, err := t.execOpensslInPod(ctx, namespace, testPod, tc.container, tc.port, string(group))
			if err != nil {
				return fmt.Errorf("%s on port %s: %s handshake failed: %w\nOutput: %s", tc.container, tc.port, group, err, output)
			}

			if !strings.Contains(output, "CONNECTION ESTABLISHED") {
				return fmt.Errorf("%s on port %s: %s expected CONNECTION ESTABLISHED\nOutput: %s", tc.container, tc.port, group, output)
			}

			expectedName := getExpectedOutputName(group)
			if !strings.Contains(output, expectedName) {
				return fmt.Errorf("%s on port %s: %s expected Peer Temp Key with %s\nOutput: %s", tc.container, tc.port, group, expectedName, output)
			}

			e2e.Logf("  ✓ %s handshake succeeded with %s", group, expectedName)
		}

		// Test rejected group (should fail)
		e2e.Logf("  Testing %s (should fail)", rejectedGroup)
		output, err := t.execOpensslInPod(ctx, namespace, testPod, tc.container, tc.port, string(rejectedGroup))
		if err == nil {
			return fmt.Errorf("%s on port %s: %s expected handshake to fail but succeeded\nOutput: %s", tc.container, tc.port, rejectedGroup, output)
		}

		if !strings.Contains(output, "handshake failure") && !strings.Contains(output, "alert") {
			return fmt.Errorf("%s on port %s: %s expected handshake failure\nOutput: %s", tc.container, tc.port, rejectedGroup, output)
		}

		e2e.Logf("  ✓ %s handshake correctly rejected", rejectedGroup)
	}

	e2e.Logf("All CNO TLS group compliance tests passed")
	return nil
}

// restartNetworkConsolePods restarts Network Console pods.
func (t *TLSTestContext) restartNetworkConsolePods(ctx context.Context) error {
	namespace := "openshift-network-console"
	labelSelector := "app.kubernetes.io/name=networking-console-plugin"
	e2e.Logf("Restarting Network Console pods with selector %s in namespace %s", labelSelector, namespace)

	pods, err := t.k8sClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}

	oldPodUIDs := make(map[string]bool)
	for _, pod := range pods.Items {
		oldPodUIDs[string(pod.UID)] = true
		e2e.Logf("Deleting pod %s (UID: %s)", pod.Name, pod.UID)
		err := t.k8sClient.CoreV1().Pods(namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{
			GracePeriodSeconds: new(int64), // 0 grace period
		})
		if err != nil {
			e2e.Logf("Warning: failed to delete pod %s: %v", pod.Name, err)
		}
	}

	e2e.Logf("Waiting for new Network Console pods to be ready...")
	Eventually(func(g Gomega, ctx context.Context) {
		newPods, err := t.k8sClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: labelSelector,
		})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(newPods.Items).NotTo(BeEmpty(), "No pods found after restart")

		allNewAndReady := true
		for _, pod := range newPods.Items {
			if oldPodUIDs[string(pod.UID)] {
				allNewAndReady = false
				continue
			}

			if pod.Status.Phase != corev1.PodRunning {
				allNewAndReady = false
			}

			ready := slices.ContainsFunc(pod.Status.Conditions, func(condition corev1.PodCondition) bool {
				return condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue
			})
			if !ready {
				allNewAndReady = false
			}
		}

		g.Expect(allNewAndReady).To(BeTrue(), "Not all new Network Console pods are ready")
	}).WithContext(ctx).WithTimeout(5 * time.Minute).WithPolling(10 * time.Second).Should(Succeed())

	e2e.Logf("All new Network Console pods are ready")
	return nil
}

// verifyNetworkConsoleTLSGroups verifies TLS group preferences for Network Console.
func (t *TLSTestContext) verifyNetworkConsoleTLSGroups(ctx context.Context, allowedGroups []configv1.TLSGroup, rejectedGroup configv1.TLSGroup) error {
	namespace := "openshift-network-console"
	labelSelector := "app.kubernetes.io/name=networking-console-plugin"

	e2e.Logf("Verifying Network Console TLS groups (allowed: %v, rejected: %s)", allowedGroups, rejectedGroup)

	// Get a running Network Console pod
	pods, err := t.k8sClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return fmt.Errorf("failed to list Network Console pods: %w", err)
	}

	var testPod string
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning {
			if slices.ContainsFunc(pod.Status.Conditions, func(condition corev1.PodCondition) bool {
				return condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue
			}) {
				testPod = pod.Name
				break
			}
		}
	}

	if testPod == "" {
		return fmt.Errorf("no ready Network Console pod found")
	}

	e2e.Logf("Testing TLS groups using pod %s/%s", namespace, testPod)

	// Network Console has networking-console-plugin container on port 9443
	testCases := []struct {
		container string
		port      string
	}{
		{"networking-console-plugin", "9443"},
	}

	// Helper to get expected output name for a group
	getExpectedOutputName := func(group configv1.TLSGroup) string {
		if group == configv1.TLSGroupSecP256r1 {
			return "prime256v1" // openssl outputs "prime256v1" for secp256r1
		}
		return string(group)
	}

	for _, tc := range testCases {
		e2e.Logf("Testing container %s on port %s", tc.container, tc.port)

		// Test each allowed group
		for _, group := range allowedGroups {
			e2e.Logf("  Testing %s (should succeed)", group)
			output, err := t.execOpensslInPod(ctx, namespace, testPod, tc.container, tc.port, string(group))
			if err != nil {
				return fmt.Errorf("%s on port %s: %s handshake failed: %w\nOutput: %s", tc.container, tc.port, group, err, output)
			}

			if !strings.Contains(output, "CONNECTION ESTABLISHED") {
				return fmt.Errorf("%s on port %s: %s expected CONNECTION ESTABLISHED\nOutput: %s", tc.container, tc.port, group, output)
			}

			expectedName := getExpectedOutputName(group)
			if !strings.Contains(output, expectedName) {
				return fmt.Errorf("%s on port %s: %s expected Peer Temp Key with %s\nOutput: %s", tc.container, tc.port, group, expectedName, output)
			}

			e2e.Logf("  ✓ %s handshake succeeded with %s", group, expectedName)
		}

		// Test rejected group (should fail)
		e2e.Logf("  Testing %s (should fail)", rejectedGroup)
		output, err := t.execOpensslInPod(ctx, namespace, testPod, tc.container, tc.port, string(rejectedGroup))
		if err == nil {
			return fmt.Errorf("%s on port %s: %s expected handshake to fail but succeeded\nOutput: %s", tc.container, tc.port, rejectedGroup, output)
		}

		if !strings.Contains(output, "handshake failure") && !strings.Contains(output, "alert") {
			return fmt.Errorf("%s on port %s: %s expected handshake failure\nOutput: %s", tc.container, tc.port, rejectedGroup, output)
		}

		e2e.Logf("  ✓ %s handshake correctly rejected", rejectedGroup)
	}

	e2e.Logf("All Network Console TLS group compliance tests passed")
	return nil
}
