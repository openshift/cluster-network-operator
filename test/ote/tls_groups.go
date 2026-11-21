package ote

import (
	"context"
	"fmt"
	"slices"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	e2e "k8s.io/kubernetes/test/e2e/framework"

	. "github.com/onsi/gomega"
)

// TLSGroupTestCase defines a container and port to test TLS group preferences on.
type TLSGroupTestCase struct {
	container string
	port      string
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
	timestamps, err := GetMCPUpdatedTimestamps(ctx, t.machineConfigClient)
	if err != nil {
		return false, fmt.Errorf("failed to get MCP timestamps: %w", err)
	}
	AwaitAllMCPsUpdatedSince(ctx, t.machineConfigClient, timestamps, MCPRolloutCompleteTimeout)
	t.awaitNodeStability(ctx)
	t.awaitTLSConfigStability(ctx)

	return false, nil
}

// configureAPIServerWithTLSGroups configures the APIServer with custom TLS groups.
func (t *TLSTestContext) configureAPIServerWithTLSGroups(ctx context.Context, tlsGroups []configv1.TLSGroup) error {
	e2e.Logf("Configuring APIServer with TLS groups: %v", tlsGroups)

	apiserver, err := t.configClient.ConfigV1().APIServers().Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get apiserver: %w", err)
	}

	// Check if already configured (idempotent)
	if apiserver.Spec.TLSSecurityProfile != nil &&
		apiserver.Spec.TLSSecurityProfile.Type == configv1.TLSProfileCustomType &&
		apiserver.Spec.TLSSecurityProfile.Custom != nil &&
		slices.Equal(apiserver.Spec.TLSSecurityProfile.Custom.Groups, tlsGroups) &&
		apiserver.Spec.TLSAdherence == configv1.TLSAdherencePolicyStrictAllComponents {
		e2e.Logf("APIServer TLS groups already configured, no update needed")
		return nil
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
		return fmt.Errorf("failed to update apiserver: %w", err)
	}

	e2e.Logf("APIServer TLS groups updated successfully")
	e2e.Logf("Waiting for cluster to stabilize after TLS configuration")

	// Wait for cluster stabilization
	timestamps, err := GetMCPUpdatedTimestamps(ctx, t.machineConfigClient)
	if err != nil {
		return fmt.Errorf("failed to get MCP timestamps: %w", err)
	}
	AwaitAllMCPsUpdatedSince(ctx, t.machineConfigClient, timestamps, MCPRolloutCompleteTimeout)
	t.awaitNodeStability(ctx)
	t.awaitTLSConfigStability(ctx)

	return nil
}

// verifyTLSGroupsInPods verifies TLS group preferences for a component by testing
// specified container/port combinations in running pods.
func (t *TLSTestContext) verifyTLSGroupsInPods(
	ctx context.Context,
	namespace string,
	labelSelector string,
	componentName string,
	allowedGroups []configv1.TLSGroup,
	rejectedGroup configv1.TLSGroup,
	testCases []TLSGroupTestCase,
) {
	e2e.Logf("Verifying %s TLS groups (allowed: %v, rejected: %s)", componentName, allowedGroups, rejectedGroup)

	// 15-minute timeout accounts for CNO detecting TLS config change, restarting,
	// rendering new manifests, and DaemonSets/Deployments rolling out new pods
	Eventually(func(g Gomega, ctx context.Context) {
		// Get a running pod
		testPod, err := getReadyPodWithLabel(ctx, t.k8sClient, namespace, labelSelector)
		g.Expect(err).NotTo(HaveOccurred(), "failed to get ready %s pod", componentName)

		e2e.Logf("Testing TLS groups using pod %s/%s", namespace, testPod)

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
				g.Expect(err).NotTo(HaveOccurred(), "%s on port %s: %s handshake failed\nOutput: %s", tc.container, tc.port, group, output)

				g.Expect(output).To(ContainSubstring("CONNECTION ESTABLISHED"),
					"%s on port %s: %s expected CONNECTION ESTABLISHED\nOutput: %s", tc.container, tc.port, group, output)

				expectedName := getExpectedOutputName(group)
				g.Expect(output).To(ContainSubstring(expectedName),
					"%s on port %s: %s expected Peer Temp Key with %s\nOutput: %s", tc.container, tc.port, group, expectedName, output)

				e2e.Logf("  ✓ %s handshake succeeded with %s", group, expectedName)
			}

			// Test rejected group (should fail)
			e2e.Logf("  Testing %s (should fail)", rejectedGroup)
			output, err := t.execOpensslInPod(ctx, namespace, testPod, tc.container, tc.port, string(rejectedGroup))
			g.Expect(err).To(HaveOccurred(), "%s on port %s: %s expected handshake to fail but succeeded\nOutput: %s", tc.container, tc.port, rejectedGroup, output)

			g.Expect(output).To(Or(ContainSubstring("handshake failure"), ContainSubstring("alert")),
				"%s on port %s: %s expected handshake failure\nOutput: %s", tc.container, tc.port, rejectedGroup, output)

			e2e.Logf("  ✓ %s handshake correctly rejected", rejectedGroup)
		}

		e2e.Logf("All %s TLS group compliance tests passed", componentName)
	}).WithContext(ctx).WithTimeout(15 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())
}

// verifyMultusTLSGroups verifies TLS group preferences for Multus admission controller.
func (t *TLSTestContext) verifyMultusTLSGroups(ctx context.Context, allowedGroups []configv1.TLSGroup, rejectedGroup configv1.TLSGroup) {
	t.verifyTLSGroupsInPods(ctx,
		"openshift-multus",
		"app=multus-admission-controller",
		"Multus",
		allowedGroups,
		rejectedGroup,
		[]TLSGroupTestCase{
			{"multus-admission-controller", "6443"},
			{"kube-rbac-proxy", "8443"},
		},
	)
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

// verifyOVNKubernetesTLSGroups verifies TLS group preferences for OVN-Kubernetes node.
func (t *TLSTestContext) verifyOVNKubernetesTLSGroups(ctx context.Context, allowedGroups []configv1.TLSGroup, rejectedGroup configv1.TLSGroup) {
	t.verifyTLSGroupsInPods(ctx,
		"openshift-ovn-kubernetes",
		"app=ovnkube-node",
		"OVN-Kubernetes",
		allowedGroups,
		rejectedGroup,
		[]TLSGroupTestCase{
			{"kube-rbac-proxy-node", "9103"},
			{"kube-rbac-proxy-ovn-metrics", "9105"},
		},
	)
}

// verifyCNOTLSGroups verifies TLS group preferences for CNO.
func (t *TLSTestContext) verifyCNOTLSGroups(ctx context.Context, allowedGroups []configv1.TLSGroup, rejectedGroup configv1.TLSGroup) {
	t.verifyTLSGroupsInPods(ctx,
		"openshift-network-operator",
		"name=network-operator",
		"CNO",
		allowedGroups,
		rejectedGroup,
		[]TLSGroupTestCase{
			{"network-operator", "9104"},
		},
	)
}

// verifyNetworkConsoleTLSGroups verifies TLS group preferences for Network Console.
func (t *TLSTestContext) verifyNetworkConsoleTLSGroups(ctx context.Context, allowedGroups []configv1.TLSGroup, rejectedGroup configv1.TLSGroup) {
	t.verifyTLSGroupsInPods(ctx,
		"openshift-network-console",
		"app.kubernetes.io/name=networking-console-plugin",
		"Network Console",
		allowedGroups,
		rejectedGroup,
		[]TLSGroupTestCase{
			{"networking-console-plugin", "9443"},
		},
	)
}
