package ote

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	e2enode "k8s.io/kubernetes/test/e2e/framework/node"
	admissionapi "k8s.io/pod-security-admission/api"
)

var _ = Describe("[sig-network] CNO sysctl allowlist", func() {
	It("installs an unchanged custom allowlist on a new node [apigroup:machine.openshift.io]", Label("Disruptive"), Serial, func(ctx context.Context) {
		oc := NewCLIWithPodSecurityLevel("cno-allowlist", admissionapi.LevelPrivileged)
		platform := checkPlatform(ctx, oc)
		if platform != "aws" && platform != "azure" && platform != "gcp" {
			Skip("Requires MachineSet provisioning on AWS, Azure, or GCP")
		}
		if isHypershiftHostedCluster(ctx, oc) || getControlPlaneTopology(ctx, oc) == "SingleReplica" {
			Skip("Requires a self-managed cluster with worker MachineSets")
		}
		machineSets := oc.AsAdmin().WithoutNamespace().Run("get").
			Args("machinesets", "-n", "openshift-machine-api", "-o=jsonpath={.items[*].metadata.name}").MustOutput(ctx)
		if machineSets == "" {
			Skip("Requires an existing worker MachineSet to clone")
		}

		DeferCleanup(oc.TeardownNamespace)
		oc.SetupNamespace(ctx)
		client := oc.KubeFramework().ClientSet
		nodes, nodesErr := e2enode.GetReadySchedulableNodes(ctx, client)
		Expect(nodesErr).NotTo(HaveOccurred(), "list existing worker nodes")
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
		marker := "^cno_allowlist_test_" + getRandomString() + "$"
		DeferCleanup(func(ctx context.Context) {
			restoreErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				current, readErr := configMaps.Get(ctx, configMapName, metav1.GetOptions{})
				if apierrors.IsNotFound(readErr) {
					return nil
				}
				if readErr != nil {
					return fmt.Errorf("get ConfigMap %s/%s to remove the test marker: %w", namespace, configMapName, readErr)
				}
				contents := current.Data["allowlist.conf"]
				if !strings.Contains(contents, marker+"\n") {
					return nil
				}
				current.Data["allowlist.conf"] = strings.ReplaceAll(contents, marker+"\n", "")
				_, updateErr := configMaps.Update(ctx, current, metav1.UpdateOptions{})
				if updateErr != nil {
					return fmt.Errorf("update ConfigMap %s/%s to remove the test marker: %w", namespace, configMapName, updateErr)
				}
				return nil
			})
			Expect(restoreErr).NotTo(HaveOccurred(), "remove the test marker from the allowlist ConfigMap")
		})

		By("Distribute a custom allowlist before adding a worker")
		custom := original.DeepCopy()
		custom.Data["allowlist.conf"] = marker + "\n" + custom.Data["allowlist.conf"]
		updated, updateErr := configMaps.Update(ctx, custom, metav1.UpdateOptions{})
		Expect(updateErr).NotTo(HaveOccurred(), "customize the allowlist")
		readAllowlist := func(nodeName string) (string, error) {
			return oc.AsAdmin().Run("debug").Args("node/"+nodeName, "--quiet", "--",
				"chroot", "/host", "cat", "/etc/cni/tuning/allowlist.conf").Output(ctx)
		}
		Eventually(ctx, func(g Gomega) {
			contents, readErr := readAllowlist(nodes.Items[0].Name)
			g.Expect(readErr).NotTo(HaveOccurred(), "read the existing worker's allowlist")
			g.Expect(strings.Contains(contents, marker)).To(BeTrue(), "the existing worker must receive the test marker")
			_, dsErr := client.AppsV1().DaemonSets(namespace).Get(ctx, "cni-sysctl-allowlist-ds", metav1.GetOptions{})
			g.Expect(apierrors.IsNotFound(dsErr)).To(BeTrue(), "wait for the temporary allowlist DaemonSet to disappear: %v", dsErr)
		}).WithTimeout(5 * time.Minute).WithPolling(10 * time.Second).Should(Succeed())

		By("Add a worker without changing the ConfigMap or restarting CNO")
		machineSetName := "cno-allowlist-" + getRandomString()
		DeferCleanup(waitForMachinesDisappear, oc, machineSetName)
		DeferCleanup(deleteMachineSet, oc, machineSetName)
		createMachineSet(ctx, oc, machineSetName)
		waitForMachineSetRunning(ctx, oc, machineSetName, 1)
		newNodeName := getNodeNameFromMachineSet(ctx, oc, machineSetName)

		By("Verify the new worker received the existing custom allowlist")
		expectedDigest := sha256.Sum256([]byte(strings.TrimSpace(custom.Data["allowlist.conf"])))
		Eventually(ctx, func(g Gomega) {
			contents, readErr := readAllowlist(newNodeName)
			g.Expect(readErr).NotTo(HaveOccurred(), "read the new worker's allowlist")
			actualDigest := sha256.Sum256([]byte(strings.TrimSpace(contents)))
			g.Expect(actualDigest).To(Equal(expectedDigest), "the new worker's allowlist must match the unchanged ConfigMap")
		}).WithTimeout(5 * time.Minute).WithPolling(10 * time.Second).Should(Succeed())
		current, currentErr := configMaps.Get(ctx, configMapName, metav1.GetOptions{})
		Expect(currentErr).NotTo(HaveOccurred(), "verify that the custom ConfigMap is unchanged")
		Expect(current.ResourceVersion).To(Equal(updated.ResourceVersion), "the ConfigMap must not change while the worker joins")
	})
})
