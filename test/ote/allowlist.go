package ote

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/util/retry"
	e2enode "k8s.io/kubernetes/test/e2e/framework/node"
	e2epod "k8s.io/kubernetes/test/e2e/framework/pod"
	admissionapi "k8s.io/pod-security-admission/api"
)

var _ = Describe("[sig-network] CNO multus", func() {
	It("given a modified sysctl allowlist ConfigMap, Multus deploys it on startup [apigroup:machine.openshift.io] [apigroup:k8s.cni.cncf.io]", Label("Disruptive"), Serial, func(ctx context.Context) {
		oc := NewCLIWithPodSecurityLevel("cno-allowlist", admissionapi.LevelPrivileged)
		platform := checkPlatform(ctx, oc)
		if platform != "aws" && platform != "azure" && platform != "gcp" {
			Skip("Requires MachineSet provisioning on AWS, Azure, or GCP")
		}
		if isHypershiftHostedCluster(ctx, oc) || getControlPlaneTopology(ctx, oc) == "SingleReplica" {
			Skip("Requires a self-managed cluster with worker MachineSets")
		}
		sourceMachineSet := linuxWorkerMachineSet(ctx, oc)
		if sourceMachineSet == "" {
			Skip("Requires a MachineSet with an existing Linux worker Machine to clone")
		}

		DeferCleanup(oc.TeardownNamespace)
		oc.SetupNamespace(ctx)
		oc.AsAdmin().Run("annotate").Args("namespace", oc.Namespace(), "openshift.io/node-selector=", "--overwrite").MustOutput(ctx)
		client := oc.KubeFramework().ClientSet
		nodeSelector := map[string]string{"kubernetes.io/os": "linux", "node-role.kubernetes.io/worker": ""}
		workers := labels.SelectorFromSet(nodeSelector)
		nodes, nodesErr := e2enode.GetReadySchedulableNodes(ctx, client)
		Expect(nodesErr).NotTo(HaveOccurred(), "list existing worker nodes")
		e2enode.Filter(nodes, func(node corev1.Node) bool {
			return workers.Matches(labels.Set(node.Labels))
		})
		Expect(nodes.Items).NotTo(BeEmpty(), "requires an existing worker to verify allowlist distribution")

		const namespace = "openshift-multus"
		const configMapName = "cni-sysctl-allowlist"
		configMaps := client.CoreV1().ConfigMaps(namespace)
		// The allowlist controller creates this ConfigMap from the default on startup.
		var original *corev1.ConfigMap
		Eventually(ctx, func() error {
			var readErr error
			original, readErr = configMaps.Get(ctx, configMapName, metav1.GetOptions{})
			return readErr
		}).WithTimeout(2*time.Minute).WithPolling(5*time.Second).Should(Succeed(), "wait for the controller-created allowlist ConfigMap")
		Expect(original.Data).To(HaveKey("allowlist.conf"), "the controller-created ConfigMap must contain the allowlist file")
		// A per-run capture group identifies our rule even if the same sysctl is already allowed.
		allowlistEntry := fmt.Sprintf(`^net\.ipv4\.conf\.IFNAME\.(?P<cno%s>accept_local)$`, getRandomString())
		readAllowlist := func(ctx context.Context, nodeName string) (string, error) {
			return oc.AsAdmin().Run("debug").Args("node/"+nodeName, "--quiet", "--",
				"chroot", "/host", "cat", "/etc/cni/tuning/allowlist.conf").Output(ctx)
		}

		By("Distribute a custom allowlist before adding a worker")
		custom := original.DeepCopy()
		custom.Data["allowlist.conf"] += "\n" + allowlistEntry
		_, updateErr := configMaps.Update(ctx, custom, metav1.UpdateOptions{})
		Expect(updateErr).NotTo(HaveOccurred(), "customize the allowlist")
		DeferCleanup(func(ctx context.Context) {
			restoreErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				current, getErr := configMaps.Get(ctx, configMapName, metav1.GetOptions{})
				if apierrors.IsNotFound(getErr) {
					return nil
				}
				if getErr != nil {
					return fmt.Errorf("get allowlist ConfigMap for cleanup: %w", getErr)
				}
				contents := current.Data["allowlist.conf"]
				restored, err := removeAllowlistEntry(contents, allowlistEntry)
				if err != nil || restored == contents {
					return err
				}
				current.Data["allowlist.conf"] = restored
				if _, writeErr := configMaps.Update(ctx, current, metav1.UpdateOptions{}); writeErr != nil {
					return fmt.Errorf("remove test rule from allowlist ConfigMap: %w", writeErr)
				}
				return nil
			})
			Expect(restoreErr).NotTo(HaveOccurred(), "remove the test rule from the allowlist")
			Eventually(ctx, func(g Gomega, ctx context.Context) {
				current, err := configMaps.Get(ctx, configMapName, metav1.GetOptions{})
				g.Expect(err).NotTo(HaveOccurred(), "read the current allowlist policy")
				survivors, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: workers.String()})
				g.Expect(err).NotTo(HaveOccurred(), "list surviving Linux workers")
				for _, worker := range survivors.Items {
					if worker.DeletionTimestamp != nil {
						continue
					}
					contents, readErr := readAllowlist(ctx, worker.Name)
					g.Expect(readErr).NotTo(HaveOccurred(), "read restored allowlist on %s", worker.Name)
					g.Expect(contents).NotTo(ContainSubstring(allowlistEntry), "test rule still present on %s", worker.Name)
					if policy, ok := current.Data["allowlist.conf"]; ok {
						g.Expect(contents).To(Equal(strings.TrimSpace(policy)), "allowlist on %s must match the current policy", worker.Name)
					}
				}
			}).WithTimeout(5 * time.Minute).WithPolling(10 * time.Second).Should(Succeed())
		})
		Eventually(ctx, func(g Gomega) {
			contents, readErr := readAllowlist(ctx, nodes.Items[0].Name)
			g.Expect(readErr).NotTo(HaveOccurred(), "read the existing worker's allowlist")
			g.Expect(contents).To(ContainSubstring(allowlistEntry), "the existing worker must receive the custom sysctl rule")
			_, dsErr := client.AppsV1().DaemonSets(namespace).Get(ctx, "cni-sysctl-allowlist-ds", metav1.GetOptions{})
			g.Expect(apierrors.IsNotFound(dsErr)).To(BeTrue(), "wait for the temporary allowlist DaemonSet to disappear: %v", dsErr)
		}).WithTimeout(5 * time.Minute).WithPolling(10 * time.Second).Should(Succeed())

		By("Run a DaemonSet using the custom sysctl on existing Linux workers")
		applyManifest(ctx, oc, fmt.Sprintf(`apiVersion: k8s.cni.cncf.io/v1
kind: NetworkAttachmentDefinition
metadata:
  name: allowlist-tuning
  namespace: %s
spec:
  config: |
    {
      "cniVersion": "0.4.0",
      "name": "allowlist-tuning",
      "plugins": [
        {"type": "bridge", "bridge": "cnoallowlist", "ipam": {}},
        {"type": "tuning", "sysctl": {"net.ipv4.conf.IFNAME.accept_local": "1"}}
      ]
    }
`, oc.Namespace()))
		// Cleanups run in reverse order: remove the workload before its worker.
		var machineSetName string
		DeferCleanup(func(ctx context.Context) {
			if machineSetName != "" {
				Expect(deleteMachineSet(ctx, oc, machineSetName)).To(Succeed(), "remove the test MachineSet and its Machines")
			}
		})
		podLabels := map[string]string{"app": "allowlist-tuning"}
		podSelector := labels.SelectorFromSet(podLabels).String()
		pods := client.CoreV1().Pods(oc.Namespace())
		daemonSets := client.AppsV1().DaemonSets(oc.Namespace())
		DeferCleanup(func(ctx context.Context) {
			propagation := metav1.DeletePropagationForeground
			deleteErr := daemonSets.Delete(ctx, "allowlist-tuning", metav1.DeleteOptions{PropagationPolicy: &propagation})
			Expect(deleteErr == nil || apierrors.IsNotFound(deleteErr)).To(BeTrue(), "delete the test DaemonSet: %v", deleteErr)
			Eventually(ctx, func(g Gomega) {
				remaining, listErr := pods.List(ctx, metav1.ListOptions{LabelSelector: podSelector})
				g.Expect(listErr).NotTo(HaveOccurred(), "list remaining test Pods")
				g.Expect(remaining.Items).To(BeEmpty(), "remove test Pods before deleting the worker")
			}).WithTimeout(5 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())
		})
		pod := e2epod.NewAgnhostPod(oc.Namespace(), "allowlist-tuning", nil, nil, nil)
		pod.Spec.NodeSelector = nodeSelector
		_, createErr := daemonSets.Create(ctx, &appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: "allowlist-tuning", Namespace: oc.Namespace()},
			Spec: appsv1.DaemonSetSpec{
				Selector: &metav1.LabelSelector{MatchLabels: podLabels},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels:      podLabels,
						Annotations: map[string]string{"k8s.v1.cni.cncf.io/networks": "allowlist-tuning@net1"},
					},
					Spec: pod.Spec,
				},
			},
		}, metav1.CreateOptions{})
		Expect(createErr).NotTo(HaveOccurred(), "create the test DaemonSet")
		verifyPodOnNode := func(nodeName string) {
			var readyPod *corev1.Pod
			Eventually(ctx, func(g Gomega) {
				list, listErr := pods.List(ctx, metav1.ListOptions{LabelSelector: podSelector, FieldSelector: "spec.nodeName=" + nodeName})
				g.Expect(listErr).NotTo(HaveOccurred(), "find the test Pod on %s", nodeName)
				g.Expect(list.Items).To(HaveLen(1), "expect one DaemonSet Pod on %s", nodeName)
				g.Expect(e2epod.RunningReady(&list.Items[0])).To(BeTrue(), "the test Pod on %s must be ready", nodeName)
				readyPod = &list.Items[0]
			}).WithTimeout(5 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())
			value := oc.Run("exec").Args(readyPod.Name, "--", "cat", "/proc/sys/net/ipv4/conf/net1/accept_local").MustOutput(ctx)
			Expect(value).To(Equal("1"), "the tuning plugin must set accept_local on the Pod's secondary interface on %s", nodeName)
		}
		for _, node := range nodes.Items {
			verifyPodOnNode(node.Name)
		}

		By("Add a Linux worker")
		machineSetName = "cno-allowlist-" + getRandomString()
		cloneMachineSet(ctx, oc, sourceMachineSet, machineSetName)
		waitForMachineSetRunning(ctx, oc, machineSetName, 1)
		newNodeName := getNodeNameFromMachineSet(ctx, oc, machineSetName)

		By("Verify Multus installed the custom allowlist on the new worker")
		Eventually(ctx, func(g Gomega) {
			contents, readErr := readAllowlist(ctx, newNodeName)
			g.Expect(readErr).NotTo(HaveOccurred(), "read the new worker's allowlist")
			g.Expect(contents).To(Equal(strings.TrimSpace(custom.Data["allowlist.conf"])), "the installed allowlist must match the ConfigMap")
		}).WithTimeout(5 * time.Minute).WithPolling(10 * time.Second).Should(Succeed())

		By("Verify the DaemonSet Pod on the new worker is ready and uses the custom sysctl")
		verifyPodOnNode(newNodeName)
	})
})

func removeAllowlistEntry(contents, entry string) (string, error) {
	lines := strings.Split(contents, "\n")
	index := slices.Index(lines, entry)
	if index == -1 {
		return contents, nil
	}
	if slices.Contains(lines[index+1:], entry) {
		return contents, fmt.Errorf("test rule %q occurs more than once in the allowlist", entry)
	}
	return strings.Join(slices.Delete(lines, index, index+1), "\n"), nil
}
