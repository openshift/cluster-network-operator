package e2e

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/kubectl/pkg/scheme"
	"k8s.io/kubernetes/test/e2e/framework"
)

const (
	ovnNamespace       = "openshift-ovn-kubernetes"
	envOverridesCMName = "env-overrides"
	scriptLibCMName    = "ovnkube-script-lib"
	ovnkubeNodeLabel   = "app=ovnkube-node"
	ovnkubeController  = "ovnkube-controller"

	// Distinct from the DaemonSet default OVN_KUBE_LOG_LEVEL ("4") so failed
	// override application cannot be mistaken for a passing assertion.
	globalLogLevel = "5"
	nodeLogLevel   = "3"
)

var loglevelRE = regexp.MustCompile(`loglevel\s+(\d+)`)

// Automated regression coverage for env-overrides `_global` behavior.
// Clients come from framework.NewDefaultFramework: OTE sets TestContext in
// AddBeforeAll; the framework BeforeEach loads ClientSet.
// SkipNamespaceCreation because these specs only touch the OVN namespace.
var _ = Describe("[sig-network] CNO env-overrides", func() {
	f := framework.NewDefaultFramework("cno-env-overrides")
	f.SkipNamespaceCreation = true

	// `_global` overrides apply across nodes (OVN_KUBE_LOG_LEVEL example).
	// Disruptive: restarts every ovnkube-node pod cluster-wide.
	It("_global key applies OVN_KUBE_LOG_LEVEL on all ovnkube-node pods", Serial, Label("Disruptive"), func(ctx context.Context) {
		assertScriptLibSourcesGlobal(ctx, f)
		restoreEnvOverridesAfter(ctx, f)

		expectedNodes := distinctNodes(listReadyOVNKubeNodePods(ctx, f))
		Expect(expectedNodes).NotTo(BeEmpty())

		By("Creating env-overrides with only _global OVN_KUBE_LOG_LEVEL=" + globalLogLevel)
		applyEnvOverrides(ctx, f, map[string]string{
			"_global": "OVN_KUBE_LOG_LEVEL=" + globalLogLevel + "\n",
		})
		Expect(restartOVNKubeNodePods(ctx, f)).To(Succeed())

		By("Verifying /env/_global is mounted and loglevel is " + globalLogLevel + " on every node")
		pods := listReadyOVNKubeNodePods(ctx, f, expectedNodes...)
		for _, pod := range pods {
			globalContent, err := execInPod(ctx, f, ovnNamespace, pod.Name, ovnkubeController,
				[]string{"cat", "/env/_global"})
			Expect(err).NotTo(HaveOccurred(), "pod %s: cat /env/_global", pod.Name)
			Expect(globalContent).To(ContainSubstring("OVN_KUBE_LOG_LEVEL="+globalLogLevel), "pod %s", pod.Name)
			Expect(ovnKubeControllerLogLevel(ctx, f, pod.Name)).To(Equal(globalLogLevel), "pod %s on node %s", pod.Name, pod.Spec.NodeName)
			framework.Logf("%s (node %s) loglevel=%s", pod.Name, pod.Spec.NodeName, globalLogLevel)
		}
	})

	// Node-specific override takes precedence over `_global`.
	It("node-specific env-overrides take precedence over _global", Serial, Label("Disruptive"), func(ctx context.Context) {
		assertScriptLibSourcesGlobal(ctx, f)
		restoreEnvOverridesAfter(ctx, f)

		pods := listReadyOVNKubeNodePods(ctx, f)
		if len(distinctNodes(pods)) < 2 {
			Skip("This case requires two distinct nodes to compare node-specific vs _global")
		}
		overridePod := pods[0]
		overrideNode := overridePod.Spec.NodeName
		Expect(overrideNode).NotTo(BeEmpty())
		expectedNodes := distinctNodes(pods)

		By(fmt.Sprintf("Creating env-overrides with _global=%s and node %s=%s", globalLogLevel, overrideNode, nodeLogLevel))
		applyEnvOverrides(ctx, f, map[string]string{
			"_global":    "OVN_KUBE_LOG_LEVEL=" + globalLogLevel + "\n",
			overrideNode: "OVN_KUBE_LOG_LEVEL=" + nodeLogLevel + "\n",
		})
		Expect(restartOVNKubeNodePods(ctx, f)).To(Succeed())

		By(fmt.Sprintf("Verifying the overridden node keeps %s while other nodes get global %s", nodeLogLevel, globalLogLevel))
		for _, pod := range listReadyOVNKubeNodePods(ctx, f, expectedNodes...) {
			level := ovnKubeControllerLogLevel(ctx, f, pod.Name)
			if pod.Spec.NodeName == overrideNode {
				Expect(level).To(Equal(nodeLogLevel), "pod %s on overridden node %s should prefer node-specific value", pod.Name, overrideNode)
			} else {
				Expect(level).To(Equal(globalLogLevel), "pod %s on node %s should use _global", pod.Name, pod.Spec.NodeName)
			}
			framework.Logf("%s (node %s) loglevel=%s", pod.Name, pod.Spec.NodeName, level)
		}
	})

	// Without `_global`, existing node-specific / default behavior is preserved.
	It("without _global, node-specific override still applies and other nodes keep default", Serial, Label("Disruptive"), func(ctx context.Context) {
		assertScriptLibSourcesGlobal(ctx, f)
		restoreEnvOverridesAfter(ctx, f)

		By("Capturing baseline log levels with no test overrides")
		// Clear any existing env-overrides so baseline reflects defaults.
		deleteEnvOverrides(ctx, f)
		Expect(restartOVNKubeNodePods(ctx, f)).To(Succeed())
		baselineByNode := logLevelsByNode(ctx, f)
		Expect(baselineByNode).NotTo(BeEmpty())
		if len(baselineByNode) < 2 {
			Skip("This case requires two distinct nodes to verify default preservation")
		}

		expectedNodes := make([]string, 0, len(baselineByNode))
		for node := range baselineByNode {
			expectedNodes = append(expectedNodes, node)
		}
		pods := listReadyOVNKubeNodePods(ctx, f, expectedNodes...)
		overridePod := pods[0]
		overrideNode := overridePod.Spec.NodeName
		Expect(overrideNode).NotTo(BeEmpty())

		By(fmt.Sprintf("Creating env-overrides with only node %s=%s (no _global)", overrideNode, globalLogLevel))
		applyEnvOverrides(ctx, f, map[string]string{
			overrideNode: "OVN_KUBE_LOG_LEVEL=" + globalLogLevel + "\n",
		})
		Expect(restartOVNKubeNodePods(ctx, f)).To(Succeed())

		By("Verifying only the overridden node changes")
		for _, pod := range listReadyOVNKubeNodePods(ctx, f, expectedNodes...) {
			level := ovnKubeControllerLogLevel(ctx, f, pod.Name)
			if pod.Spec.NodeName == overrideNode {
				Expect(level).To(Equal(globalLogLevel), "pod %s on node %s should get node-specific override", pod.Name, overrideNode)
			} else {
				expected := baselineByNode[pod.Spec.NodeName]
				Expect(expected).NotTo(BeEmpty(), "missing baseline for node %s", pod.Spec.NodeName)
				Expect(level).To(Equal(expected),
					"pod %s on node %s should keep default/baseline loglevel without _global", pod.Name, pod.Spec.NodeName)
			}
			framework.Logf("%s (node %s) loglevel=%s", pod.Name, pod.Spec.NodeName, level)
		}
	})
})

func assertScriptLibSourcesGlobal(ctx context.Context, f *framework.Framework) {
	By("Confirming _global support in ovnkube-script-lib")
	scriptLib, err := f.ClientSet.CoreV1().ConfigMaps(ovnNamespace).Get(ctx, scriptLibCMName, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred())
	Expect(scriptLib.Data["ovnkube-lib.sh"]).To(ContainSubstring(`/env/_global`),
		"regression: cluster ovnkube-script-lib does not source /env/_global")
}

func restoreEnvOverridesAfter(ctx context.Context, f *framework.Framework) {
	By("Recording whether env-overrides already exists")
	existing, err := f.ClientSet.CoreV1().ConfigMaps(ovnNamespace).Get(ctx, envOverridesCMName, metav1.GetOptions{})
	hadCM := false
	var prevCM *corev1.ConfigMap
	if apierrors.IsNotFound(err) {
		hadCM = false
	} else {
		Expect(err).NotTo(HaveOccurred())
		hadCM = true
		prevCM = existing.DeepCopy()
	}

	DeferCleanup(func(ctx context.Context) {
		By("Restoring env-overrides ConfigMap and restarting ovnkube-node pods")
		cms := f.ClientSet.CoreV1().ConfigMaps(ovnNamespace)
		if !hadCM {
			err := cms.Delete(ctx, envOverridesCMName, metav1.DeleteOptions{})
			if err != nil {
				Expect(apierrors.IsNotFound(err)).To(BeTrue(),
					"failed to delete env-overrides during cleanup: %v", err)
			}
		} else if prevCM != nil {
			current, getErr := cms.Get(ctx, envOverridesCMName, metav1.GetOptions{})
			toApply := prevCM.DeepCopy()
			if apierrors.IsNotFound(getErr) {
				toApply.ResourceVersion = ""
				_, err := cms.Create(ctx, toApply, metav1.CreateOptions{})
				Expect(err).NotTo(HaveOccurred(), "failed to recreate env-overrides")
			} else {
				Expect(getErr).NotTo(HaveOccurred())
				toApply.ResourceVersion = current.ResourceVersion
				_, err := cms.Update(ctx, toApply, metav1.UpdateOptions{})
				Expect(err).NotTo(HaveOccurred(), "failed to restore env-overrides")
			}
		}
		Expect(restartOVNKubeNodePods(ctx, f)).To(Succeed())
	})
}

func deleteEnvOverrides(ctx context.Context, f *framework.Framework) {
	err := f.ClientSet.CoreV1().ConfigMaps(ovnNamespace).Delete(ctx, envOverridesCMName, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		Expect(err).NotTo(HaveOccurred(), "failed to delete env-overrides")
	}
}

func applyEnvOverrides(ctx context.Context, f *framework.Framework, data map[string]string) {
	cms := f.ClientSet.CoreV1().ConfigMaps(ovnNamespace)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      envOverridesCMName,
			Namespace: ovnNamespace,
		},
		Data: data,
	}
	existing, err := cms.Get(ctx, envOverridesCMName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = cms.Create(ctx, cm, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred(), "failed to create env-overrides")
		return
	}
	Expect(err).NotTo(HaveOccurred())
	cm.ResourceVersion = existing.ResourceVersion
	_, err = cms.Update(ctx, cm, metav1.UpdateOptions{})
	Expect(err).NotTo(HaveOccurred(), "failed to update env-overrides")
}

// listReadyOVNKubeNodePods waits until every expected node has a Ready
// ovnkube-node pod, then returns those pods. When expectedNodes is empty, the
// expected set is derived from NodeNames currently present on ovnkube-node pods
// (including NotReady/terminating ones), so assertions cannot pass on a nonempty
// subset while some nodes are still missing.
func listReadyOVNKubeNodePods(ctx context.Context, f *framework.Framework, expectedNodes ...string) []corev1.Pod {
	var ready []corev1.Pod
	Eventually(ctx, func(g Gomega) {
		pods, err := f.ClientSet.CoreV1().Pods(ovnNamespace).List(ctx, metav1.ListOptions{LabelSelector: ovnkubeNodeLabel})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(pods.Items).NotTo(BeEmpty(), "no ovnkube-node pods found")

		expected := map[string]struct{}{}
		if len(expectedNodes) > 0 {
			for _, node := range expectedNodes {
				if node != "" {
					expected[node] = struct{}{}
				}
			}
		} else {
			for _, pod := range pods.Items {
				if pod.Spec.NodeName != "" {
					expected[pod.Spec.NodeName] = struct{}{}
				}
			}
		}
		g.Expect(expected).NotTo(BeEmpty(), "no expected ovnkube-node nodes")

		readyByNode := map[string]corev1.Pod{}
		for _, pod := range pods.Items {
			if pod.Spec.NodeName == "" || pod.DeletionTimestamp != nil || !podReady(&pod) {
				continue
			}
			readyByNode[pod.Spec.NodeName] = pod
		}

		missing := make([]string, 0)
		for node := range expected {
			if _, ok := readyByNode[node]; !ok {
				missing = append(missing, node)
			}
		}
		g.Expect(missing).To(BeEmpty(), "Ready ovnkube-node pod missing on nodes %v", missing)

		out := make([]corev1.Pod, 0, len(expected))
		for node := range expected {
			out = append(out, readyByNode[node])
		}
		ready = out
	}).WithTimeout(5 * time.Minute).WithPolling(2 * time.Second).Should(Succeed())
	return ready
}

func distinctNodes(pods []corev1.Pod) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(pods))
	for _, pod := range pods {
		if pod.Spec.NodeName == "" {
			continue
		}
		if _, ok := seen[pod.Spec.NodeName]; ok {
			continue
		}
		seen[pod.Spec.NodeName] = struct{}{}
		out = append(out, pod.Spec.NodeName)
	}
	return out
}

// ovnKubeControllerLogLevel waits until ovnkube-controller has started (Pod Ready
// alone is not enough — the entrypoint may still be sourcing /env).
func ovnKubeControllerLogLevel(ctx context.Context, f *framework.Framework, podName string) string {
	var level string
	Eventually(ctx, func(g Gomega) {
		proc, err := execInPod(ctx, f, ovnNamespace, podName, ovnkubeController,
			[]string{"pgrep", "-a", "-f", "init-ovnkube-controller"})
		g.Expect(err).NotTo(HaveOccurred(), "pod %s: pgrep", podName)
		m := loglevelRE.FindStringSubmatch(proc)
		g.Expect(m).NotTo(BeNil(), "pod %s: no loglevel in %q", podName, proc)
		level = m[1]
	}).WithTimeout(2 * time.Minute).WithPolling(2 * time.Second).Should(Succeed())
	return level
}

func logLevelsByNode(ctx context.Context, f *framework.Framework) map[string]string {
	out := map[string]string{}
	for _, pod := range listReadyOVNKubeNodePods(ctx, f) {
		out[pod.Spec.NodeName] = ovnKubeControllerLogLevel(ctx, f, pod.Name)
		framework.Logf("baseline node %s (%s) loglevel=%s", pod.Spec.NodeName, pod.Name, out[pod.Spec.NodeName])
	}
	return out
}

func restartOVNKubeNodePods(ctx context.Context, f *framework.Framework) error {
	pods, err := f.ClientSet.CoreV1().Pods(ovnNamespace).List(ctx, metav1.ListOptions{LabelSelector: ovnkubeNodeLabel})
	if err != nil {
		return err
	}
	if len(pods.Items) == 0 {
		return fmt.Errorf("no ovnkube-node pods found to restart")
	}

	// Restart one pod at a time so cluster networking stays available on the
	// remaining nodes (matches OCP guidance / DaemonSet maxUnavailable=1).
	for _, pod := range pods.Items {
		if pod.Spec.NodeName == "" {
			return fmt.Errorf("ovnkube-node pod %s has no assigned NodeName", pod.Name)
		}
		oldUID := pod.UID
		nodeName := pod.Spec.NodeName
		if err := f.ClientSet.CoreV1().Pods(ovnNamespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete pod %s: %w", pod.Name, err)
		}
		if err := waitForOVNKubeNodeReplacement(ctx, f, nodeName, oldUID); err != nil {
			return err
		}
	}
	return nil
}

func waitForOVNKubeNodeReplacement(ctx context.Context, f *framework.Framework, nodeName string, oldUID types.UID) error {
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		current, err := f.ClientSet.CoreV1().Pods(ovnNamespace).List(ctx, metav1.ListOptions{LabelSelector: ovnkubeNodeLabel})
		if err != nil {
			return err
		}
		for _, pod := range current.Items {
			if pod.Spec.NodeName != nodeName || pod.UID == oldUID {
				continue
			}
			if pod.DeletionTimestamp == nil && podReady(&pod) {
				return nil
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timed out waiting for replacement ovnkube-node pod on node %s", nodeName)
}

func podReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// execInPod uses f.ClientConfig() so API and exec share credentials.
func execInPod(ctx context.Context, f *framework.Framework, namespace, podName, container string, command []string) (string, error) {
	req := f.ClientSet.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   command,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	executer, err := remotecommand.NewSPDYExecutor(f.ClientConfig(), "POST", req.URL())
	if err != nil {
		return "", err
	}
	var stdout, stderr bytes.Buffer
	err = executer.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		return "", fmt.Errorf("exec stream: %w: %s", err, stderr.String())
	}
	return stdout.String(), nil
}
