package ote

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/util/wait"
	e2e "k8s.io/kubernetes/test/e2e/framework"
	e2eoutput "k8s.io/kubernetes/test/e2e/framework/pod/output"
	netutils "k8s.io/utils/net"
)

func init() {
	if kubeconfig := os.Getenv("KUBECONFIG"); kubeconfig != "" {
		e2e.TestContext.KubeConfig = kubeconfig
	}
	e2e.TestContext.KubectlPath = "kubectl"
	e2e.TestContext.CloudConfig.Provider = e2e.NullProvider{}
	e2e.TestContext.DeleteNamespace = os.Getenv("DELETE_NAMESPACE") != "false"
}

func getRandomString() string {
	chars := "abcdefghijklmnopqrstuvwxyz0123456789"
	buffer := make([]byte, 8)
	for index := range buffer {
		buffer[index] = chars[rand.Intn(len(chars))]
	}
	return string(buffer)
}

// applyManifest writes content to a temporary file and applies it, retrying on
// transient failures. The temporary file is removed via DeferCleanup when the
// spec finishes.
func applyManifest(ctx context.Context, oc *CLI, content string) {
	tmpFile := fmt.Sprintf("/tmp/manifest-%s.json", getRandomString())

	err := os.WriteFile(tmpFile, []byte(content), 0644)
	Expect(err).NotTo(HaveOccurred())

	DeferCleanup(func() {
		if err := os.Remove(tmpFile); err != nil {
			e2e.Logf("warning: failed to remove temporary file %s: %v", tmpFile, err)
		}
	})

	err = wait.PollUntilContextTimeout(ctx, 3*time.Second, 20*time.Second, true, func(ctx context.Context) (bool, error) {
		if err := oc.AsAdmin().WithoutNamespace().Run("apply").Args("-f", tmpFile).Execute(); err != nil {
			e2e.Logf("Failed to apply manifest: %v, retrying...", err)
			return false, nil
		}
		return true, nil
	})
	Expect(err).NotTo(HaveOccurred(), "failed to apply manifest")
}

func checkIPStackType(oc *CLI) string {
	svcNetwork := oc.WithoutNamespace().AsAdmin().Run("get").Args("network.operator", "cluster", "-o=jsonpath={.spec.serviceNetwork}").MustOutput()
	if strings.Count(svcNetwork, ":") >= 2 && strings.Count(svcNetwork, ".") >= 2 {
		return "dualstack"
	} else if strings.Count(svcNetwork, ":") >= 2 {
		return "ipv6single"
	} else if strings.Count(svcNetwork, ".") >= 2 {
		return "ipv4single"
	}
	return ""
}

func getPodStatus(oc *CLI, namespace string, podName string) (string, error) {
	podStatus, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("pod", "-n", namespace, podName, "-o=jsonpath={.status.phase}").Output()
	if err != nil {
		return "", err
	}
	e2e.Logf("The pod  %s status in namespace %s is %q", podName, namespace, podStatus)
	return podStatus, nil
}

func checkPodReady(oc *CLI, namespace string, podName string) (bool, error) {
	podOutPut, err := getPodStatus(oc, namespace, podName)
	status := []string{"Running", "Ready", "Complete", "Succeeded"}
	return slices.Contains(status, podOutPut), err
}

func awaitPodReady(ctx context.Context, oc *CLI, namespace string, podName string) {
	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, 60*time.Second, true, func(ctx context.Context) (bool, error) {
		status, err := checkPodReady(oc, namespace, podName)
		if err != nil {
			e2e.Logf("the err:%v, wait for pod %v to become ready.", err, podName)
			return false, nil
		}
		return status, nil
	})

	if err != nil {
		podDescribe := describePod(oc, namespace, podName)
		e2e.Logf("oc describe pod %v.", podName)
		e2e.Logf("%s", podDescribe)
	}
	Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("pod %v is not ready", podName))
}

func describePod(oc *CLI, namespace string, podName string) string {
	podDescribe := oc.WithoutNamespace().Run("describe").Args("pod", "-n", namespace, podName).MustOutput()
	e2e.Logf("The pod  %s status is %q", podName, podDescribe)
	return podDescribe
}

func patchResourceAsAdmin(oc *CLI, resource, patch string, nameSpace ...string) {
	var cargs []string
	if len(nameSpace) > 0 {
		cargs = []string{resource, "-p", patch, "-n", nameSpace[0], "--type=merge"}
	} else {
		cargs = []string{resource, "-p", patch, "--type=merge"}
	}
	err := oc.AsAdmin().WithoutNamespace().Run("patch").Args(cargs...).Execute()
	Expect(err).NotTo(HaveOccurred())
}

func isHypershiftHostedCluster(oc *CLI) bool {
	topology := oc.WithoutNamespace().AsAdmin().Run("get").Args("infrastructures.config.openshift.io", "cluster", "-o=jsonpath={.status.controlPlaneTopology}").MustOutput()
	e2e.Logf("topology is %s", topology)
	if topology == "" {
		status, _ := oc.WithoutNamespace().AsAdmin().Run("get").Args("infrastructures.config.openshift.io", "cluster", "-o=jsonpath={.status}").Output()
		e2e.Logf("cluster status %s", status)
		Fail("failure: controlPlaneTopology returned empty")
	}
	return topology == "External"
}

func checkOVNKState(ctx context.Context, oc *CLI) error {
	err := awaitPodWithLabelReady(ctx, oc, "openshift-ovn-kubernetes", "app=ovnkube-node")
	Expect(err).NotTo(HaveOccurred())

	if !isHypershiftHostedCluster(oc) {
		err = awaitPodWithLabelReady(ctx, oc, "openshift-ovn-kubernetes", "app=ovnkube-control-plane")
		Expect(err).NotTo(HaveOccurred())
	}
	return wait.PollUntilContextTimeout(ctx, 10*time.Second, 10*time.Minute, true, func(ctx context.Context) (bool, error) {
		status, err := oc.AsAdmin().WithoutNamespace().Run("rollout").Args("status", "-n", "openshift-ovn-kubernetes", "ds", "ovnkube-node", "--timeout", "1m").Output()
		if err != nil {
			e2e.Logf("ovnkube-node rollout not complete yet (%v), retrying...", err)
			return false, nil
		}
		if strings.Contains(status, "successfully rolled out") {
			e2e.Logf("ovnkube rollout was triggerred and rolled out successfully")
			return true, nil
		}
		e2e.Logf("ovnkube rollout trigger hasn't happened yet. Trying again")
		return false, nil
	})
}

func awaitPodWithLabelReady(ctx context.Context, oc *CLI, ns, label string) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		status, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("pod", "-n", ns, "-l", label, "-ojsonpath={.items[*].status.conditions[?(@.type==\"Ready\")].status}").Output()
		e2e.Logf("the Ready status of pod is %v", status)
		if err != nil || status == "" {
			e2e.Logf("failed to get pod status: %v, retrying...", err)
			return false, nil
		}
		if strings.Contains(status, "False") {
			e2e.Logf("the pod Ready status not met; wanted True but got %v, retrying...", status)
			return false, nil
		}
		return true, nil
	})
}

func curlPod2PodPass(oc *CLI, namespaceSrc string, podNameSrc string, namespaceDst string, podNameDst string, podPort int) {
	podIP1, podIP2 := getPodIP(oc, namespaceDst, podNameDst)
	if podIP2 != "" {
		_, err := e2eoutput.RunHostCmd(namespaceSrc, podNameSrc, "curl --connect-timeout 5 -s "+net.JoinHostPort(podIP1, strconv.Itoa(podPort)))
		Expect(err).NotTo(HaveOccurred())
		_, err = e2eoutput.RunHostCmd(namespaceSrc, podNameSrc, "curl --connect-timeout 5 -s "+net.JoinHostPort(podIP2, strconv.Itoa(podPort)))
		Expect(err).NotTo(HaveOccurred())
	} else {
		_, err := e2eoutput.RunHostCmd(namespaceSrc, podNameSrc, "curl --connect-timeout 5 -s "+net.JoinHostPort(podIP1, strconv.Itoa(podPort)))
		Expect(err).NotTo(HaveOccurred())
	}
}

func getPodIP(oc *CLI, namespace string, podName string) (string, string) {
	ipStack := checkIPStackType(oc)
	switch ipStack {
	case "ipv6single", "ipv4single":
		podIP := oc.AsAdmin().WithoutNamespace().Run("get").Args("pod", "-n", namespace, podName, "-o=jsonpath={.status.podIPs[0].ip}").MustOutput()
		e2e.Logf("The pod  %s IP in namespace %s is %q", podName, namespace, podIP)
		return podIP, ""
	case "dualstack":
		podIP1 := oc.AsAdmin().WithoutNamespace().Run("get").Args("pod", "-n", namespace, podName, "-o=jsonpath={.status.podIPs[1].ip}").MustOutput()
		e2e.Logf("The pod's %s 1st IP in namespace %s is %q", podName, namespace, podIP1)
		podIP2 := oc.AsAdmin().WithoutNamespace().Run("get").Args("pod", "-n", namespace, podName, "-o=jsonpath={.status.podIPs[0].ip}").MustOutput()
		e2e.Logf("The pod's %s 2nd IP in namespace %s is %q", podName, namespace, podIP2)
		if netutils.IsIPv6String(podIP1) {
			e2e.Logf("This is IPv4 primary dual stack cluster")
			return podIP1, podIP2
		}
		e2e.Logf("This is IPv6 primary dual stack cluster")
		return podIP2, podIP1
	}
	return "", ""
}

func curlPod2SvcPass(ctx context.Context, oc *CLI, namespaceSrc string, namespaceSvc string, podNameSrc string, svcName string, svcPort int) {
	svcIP1, svcIP2 := getSvcIP(ctx, oc, namespaceSvc, svcName)
	if svcIP2 != "" {
		_, err := e2eoutput.RunHostCmdWithRetries(namespaceSrc, podNameSrc, "curl --connect-timeout 5 -s "+net.JoinHostPort(svcIP1, strconv.Itoa(svcPort)), 3*time.Second, 15*time.Second)
		Expect(err).NotTo(HaveOccurred())
		_, err = e2eoutput.RunHostCmdWithRetries(namespaceSrc, podNameSrc, "curl --connect-timeout 5 -s "+net.JoinHostPort(svcIP2, strconv.Itoa(svcPort)), 3*time.Second, 15*time.Second)
		Expect(err).NotTo(HaveOccurred())
	} else {
		_, err := e2eoutput.RunHostCmdWithRetries(namespaceSrc, podNameSrc, "curl --connect-timeout 5 -s "+net.JoinHostPort(svcIP1, strconv.Itoa(svcPort)), 3*time.Second, 15*time.Second)
		Expect(err).NotTo(HaveOccurred())
	}
}

func getSvcIP(ctx context.Context, oc *CLI, namespace string, svcName string) (string, string) {
	ipStack := checkIPStackType(oc)
	svctype := oc.AsAdmin().WithoutNamespace().Run("get").Args("service", "-n", namespace, svcName, "-o=jsonpath={.spec.type}").MustOutput()
	ipFamilyType := oc.AsAdmin().WithoutNamespace().Run("get").Args("service", "-n", namespace, svcName, "-o=jsonpath={.spec.ipFamilyPolicy}").MustOutput()
	if (svctype == "ClusterIP") || (svctype == "NodePort") {
		if (ipStack == "ipv6single") || (ipStack == "ipv4single") {
			svcIP := oc.AsAdmin().WithoutNamespace().Run("get").Args("service", "-n", namespace, svcName, "-o=jsonpath={.spec.clusterIPs[0]}").MustOutput()
			if svctype == "ClusterIP" {
				e2e.Logf("The service %s IP in namespace %s is %q", svcName, namespace, svcIP)
				return svcIP, ""
			}
			nodePort := oc.AsAdmin().WithoutNamespace().Run("get").Args("service", "-n", namespace, svcName, "-o=jsonpath={.spec.ports[*].nodePort}").MustOutput()
			e2e.Logf("The NodePort service %s IP and NodePort in namespace %s is %s %s", svcName, namespace, svcIP, nodePort)
			return svcIP, nodePort

		} else if (ipStack == "dualstack" && ipFamilyType == "PreferDualStack") || (ipStack == "dualstack" && ipFamilyType == "RequireDualStack") {
			ipFamilyPrecedence := oc.AsAdmin().WithoutNamespace().Run("get").Args("service", "-n", namespace, svcName, "-o=jsonpath={.spec.ipFamilies[0]}").MustOutput()
			svcIPv4 := oc.AsAdmin().WithoutNamespace().Run("get").Args("service", "-n", namespace, svcName, "-o=jsonpath={.spec.clusterIPs[0]}").MustOutput()
			e2e.Logf("The service %s IP in namespace %s is %q", svcName, namespace, svcIPv4)
			svcIPv6 := oc.AsAdmin().WithoutNamespace().Run("get").Args("service", "-n", namespace, svcName, "-o=jsonpath={.spec.clusterIPs[1]}").MustOutput()
			e2e.Logf("The service %s IP in namespace %s is %q", svcName, namespace, svcIPv6)
			if ipFamilyPrecedence == "IPv4" {
				e2e.Logf("The ipFamilyPrecedence is Ipv4, Ipv6")
				switch svctype {
				case "NodePort":
					nodePort := oc.AsAdmin().WithoutNamespace().Run("get").Args("service", "-n", namespace, svcName, "-o=jsonpath={.spec.ports[*].nodePort}").MustOutput()
					e2e.Logf("The Dual Stack NodePort service %s IP and NodePort in namespace %s is %s %s", svcName, namespace, svcIPv4, nodePort)
					return svcIPv4, nodePort
				default:
					return svcIPv6, svcIPv4
				}
			} else {
				e2e.Logf("The ipFamilyPrecedence is Ipv6, Ipv4")
				switch svctype {
				case "NodePort":
					nodePort := oc.AsAdmin().WithoutNamespace().Run("get").Args("service", "-n", namespace, svcName, "-o=jsonpath={.spec.ports[*].nodePort}").MustOutput()
					e2e.Logf("The Dual Stack NodePort service %s IP and NodePort in namespace %s is %s %s", svcName, namespace, svcIPv6, nodePort)
					return svcIPv6, nodePort
				default:
					svcIPv4, svcIPv6 = svcIPv6, svcIPv4
					return svcIPv6, svcIPv4
				}
			}
		} else {
			svcIP := oc.AsAdmin().WithoutNamespace().Run("get").Args("service", "-n", namespace, svcName, "-o=jsonpath={.spec.clusterIPs[0]}").MustOutput()
			e2e.Logf("The service %s IP in namespace %s is %q", svcName, namespace, svcIP)
			return svcIP, ""
		}
	} else {
		e2e.Logf("The serviceType is LoadBalancer")
		platform := checkPlatform(oc)
		var jsonString string
		if platform == "aws" {
			jsonString = "-o=jsonpath={.status.loadBalancer.ingress[0].hostname}"
		} else {
			jsonString = "-o=jsonpath={.status.loadBalancer.ingress[0].ip}"
		}

		err := wait.PollUntilContextTimeout(ctx, 3*time.Second, 300*time.Second, true, func(ctx context.Context) (bool, error) {
			svcIP, er := oc.AsAdmin().WithoutNamespace().Run("get").Args("service", "-n", namespace, svcName, jsonString).Output()
			if er != nil {
				e2e.Logf("Failed to get lb service IP: %v, retrying...", er)
				return false, nil
			}
			if svcIP == "" {
				e2e.Logf("Waiting for lb service IP assignment. Trying again...")
				return false, nil
			}
			return true, nil
		})
		Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("fail to assign lb svc IP to %v", svcName))
		lbSvcIP := oc.AsAdmin().WithoutNamespace().Run("get").Args("service", "-n", namespace, svcName, jsonString).MustOutput()
		e2e.Logf("The %s lb service Ingress VIP in namespace %s is %q", svcName, namespace, lbSvcIP)
		return lbSvcIP, ""
	}
}

// checkPlatform returns the lowercased cluster platform type, or "" if the
// infrastructure resource does not exist. Any other error fails the test so a
// transient API failure is not silently treated as "no platform".
func checkPlatform(oc *CLI) string {
	output, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("infrastructure", "cluster", "-o=jsonpath={.status.platformStatus.type}").Output()
	if err != nil {
		if strings.Contains(err.Error(), "NotFound") {
			return ""
		}
		Expect(err).NotTo(HaveOccurred(), "failed to get infrastructure platform type")
	}
	return strings.ToLower(output)
}

// getControlPlaneTopology returns the cluster control-plane topology, or "" if
// the infrastructure resource does not exist. Any other error fails the test.
func getControlPlaneTopology(oc *CLI) string {
	output, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("infrastructures.config.openshift.io", "cluster", "-o=jsonpath={.status.controlPlaneTopology}").Output()
	if err != nil {
		if strings.Contains(err.Error(), "NotFound") {
			return ""
		}
		Expect(err).NotTo(HaveOccurred(), "failed to get control plane topology")
	}
	return output
}

func createPingPodOnNode(ctx context.Context, oc *CLI, podName, namespace, label, nodeName string) {
	podJSON := fmt.Sprintf(`{
		"apiVersion": "v1",
		"kind": "Pod",
		"metadata": {
			"name": "%s",
			"namespace": "%s",
			"labels": {
				"name": "%s"
			}
		},
		"spec": {
			"nodeName": "%s",
			"containers": [{
				"name": "hello-pod",
				"image": "quay.io/openshifttest/hello-sdn@sha256:c89445416459e7adea9a5a416b3365ed3d74f2491beb904d61dc8d1eb89a72a4",
				"ports": [{
					"containerPort": 8080
				}]
			}],
			"restartPolicy": "Never"
		}
	}`, podName, namespace, label, nodeName)

	By(fmt.Sprintf("Creating pod %s on node %s", podName, nodeName))
	applyManifest(ctx, oc, podJSON)

	awaitPodReady(ctx, oc, namespace, podName)
	e2e.Logf("Pod %s is ready on node %s", podName, nodeName)
}

func createGenericService(ctx context.Context, oc *CLI, serviceName, namespace, protocol, selector, serviceType, ipFamilyPolicy, internalTrafficPolicy, externalTrafficPolicy string, servicePort, serviceTargetPort int) {
	var ipFamilyPolicyJSON string
	if ipFamilyPolicy != "" {
		ipFamilyPolicyJSON = fmt.Sprintf(`"ipFamilyPolicy": "%s",`, ipFamilyPolicy)
	}

	var internalTrafficPolicyJSON string
	if internalTrafficPolicy != "" {
		internalTrafficPolicyJSON = fmt.Sprintf(`"internalTrafficPolicy": "%s",`, internalTrafficPolicy)
	}

	var externalTrafficPolicyJSON string
	if externalTrafficPolicy != "" {
		externalTrafficPolicyJSON = fmt.Sprintf(`"externalTrafficPolicy": "%s",`, externalTrafficPolicy)
	}

	serviceJSON := fmt.Sprintf(`{
		"apiVersion": "v1",
		"kind": "Service",
		"metadata": {
			"name": "%s",
			"namespace": "%s"
		},
		"spec": {
			"type": "%s",
			%s
			%s
			%s
			"selector": {
				"name": "%s"
			},
			"ports": [{
				"protocol": "%s",
				"port": %d,
				"targetPort": %d
			}]
		}
	}`, serviceName, namespace, serviceType, ipFamilyPolicyJSON, internalTrafficPolicyJSON, externalTrafficPolicyJSON, selector, protocol, servicePort, serviceTargetPort)

	By(fmt.Sprintf("Creating service %s in namespace %s", serviceName, namespace))
	applyManifest(ctx, oc, serviceJSON)
	e2e.Logf("Service %s created successfully in namespace %s", serviceName, namespace)
}

func rebootNode(oc *CLI, nodeName string) {
	e2e.Logf("Rebooting node %s....", nodeName)
	err := oc.AsAdmin().Run("debug").Args("node/"+nodeName, "--", "chroot", "/host", "shutdown", "-r", "+1").Execute()
	Expect(err).NotTo(HaveOccurred())
}

func checkNodeStatus(ctx context.Context, oc *CLI, nodeName string, expectedStatus string) {
	var expectedValue string
	switch expectedStatus {
	case "Ready":
		expectedValue = "True"
	case "NotReady":
		expectedValue = "Unknown"
	default:
		Expect(fmt.Errorf("unsupported node status: %s", expectedStatus)).NotTo(HaveOccurred())
	}
	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, 15*time.Minute, true, func(ctx context.Context) (bool, error) {
		statusOutput, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("nodes", nodeName, "-ojsonpath={.status.conditions[-1].status}").Output()
		if err != nil {
			e2e.Logf("Get node status with error : %v", err)
			return false, nil
		}
		e2e.Logf("Expect Node %s in state %v, kubelet status is %s", nodeName, expectedStatus, statusOutput)
		if statusOutput != expectedValue {
			return false, nil
		}
		return true, nil
	})
	Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Node %s is not in expected status %s", nodeName, expectedStatus))
}

func getPodNameOnNode(oc *CLI, ns, label, nodeName string) (string, error) {
	return oc.AsAdmin().WithoutNamespace().Run("get").Args("pod", "-n", ns, "-l", label,
		"--field-selector=spec.nodeName="+nodeName, "-o=jsonpath={.items[0].metadata.name}").Output()
}

func execInPodContainer(oc *CLI, ns, podName, container, cmd string) (string, error) {
	return oc.AsAdmin().WithoutNamespace().Run("exec").Args(podName, "-n", ns, "-c", container, "--", "bash", "-c", cmd).Output()
}

func getOVNK8sNodeMgmtIPv4(ctx context.Context, oc *CLI, nodeName string) string {
	var output string
	err := wait.PollUntilContextTimeout(ctx, 10*time.Second, 60*time.Second, true, func(ctx context.Context) (bool, error) {
		out, cmdErr := oc.AsAdmin().Run("debug").Args("node/"+nodeName, "--", "chroot", "/host", "bash", "-c", "/usr/sbin/ip -4 -brief address show | grep ovn-k8s-mp0").Output()
		if out == "" || cmdErr != nil {
			e2e.Logf("Did not get node's management interface, errors: %v, try again", cmdErr)
			return false, nil
		}
		output = out
		return true, nil
	})
	Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("fail to get management interface for node %v", nodeName))

	re := regexp.MustCompile(`(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)(\.(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)){3}`)
	nodeOVNK8sMgmtIP := re.FindAllString(output, -1)[0]
	e2e.Logf("Got ovn-k8s management interface IP for node %v as: %v", nodeName, nodeOVNK8sMgmtIP)
	return nodeOVNK8sMgmtIP
}

func getOVNK8sNodeMgmtIPv6(ctx context.Context, oc *CLI, nodeName string) string {
	var cmdOutput string
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 10*time.Second, true, func(ctx context.Context) (bool, error) {
		out, cmdErr := oc.AsAdmin().Run("debug").Args("node/"+nodeName, "--", "chroot", "/host", "bash", "-c",
			`/usr/sbin/ip -o -6 addr show dev ovn-k8s-mp0 | awk '$3 == "inet6" && $6 == "global" {print $4}' | cut -d'/' -f1`).Output()
		if out == "" || cmdErr != nil {
			e2e.Logf("Did not get node's IPv6 management interface, errors: %v, try again", cmdErr)
			return false, nil
		}
		cmdOutput = out
		return true, nil
	})
	Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to get IPv6 management interface for node %v", nodeName))
	return strings.Split(cmdOutput, "\n")[0]
}

func getJoinSwitchIPofNode(ctx context.Context, oc *CLI, nodeName string) ([]string, []string) {
	ovnKubePod, podErr := getPodNameOnNode(oc, "openshift-ovn-kubernetes", "app=ovnkube-node", nodeName)
	Expect(podErr).NotTo(HaveOccurred())
	Expect(ovnKubePod).NotTo(BeEmpty())

	var cmdOutput string
	var joinSwitchIPv4s, joinSwitchIPv6s []string
	cmd := "ovn-nbctl get logical_router_port rtoj-GR_" + nodeName + " networks"
	err := wait.PollUntilContextTimeout(ctx, 3*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		out, cmdErr := execInPodContainer(oc, "openshift-ovn-kubernetes", ovnKubePod, "northd", cmd)
		if out == "" || cmdErr != nil {
			e2e.Logf("%v, Waiting for expected result to be synced, try again ...", cmdErr)
			return false, nil
		}
		cmdOutput = out
		return true, nil
	})
	Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to get join switch networks for node %v", nodeName))

	rightTrimed := strings.TrimRight(strings.TrimLeft(cmdOutput, "["), "]")
	outputs := strings.Split(rightTrimed, ", ")
	for _, str := range outputs {
		ipv4orv6 := strings.Trim(str, "\"")
		ipOnly := strings.Split(ipv4orv6, "/")[0]
		if isIPv4(ipv4orv6) {
			joinSwitchIPv4s = append(joinSwitchIPv4s, ipOnly)
		}
		if isIPv6(ipv4orv6) {
			joinSwitchIPv6s = append(joinSwitchIPv6s, ipOnly)
		}
	}
	return joinSwitchIPv4s, joinSwitchIPv6s
}

func isIPv4(addr string) bool {
	return netutils.IsIPv4String(strings.Split(addr, "/")[0])
}

func isIPv6(addr string) bool {
	return netutils.IsIPv6String(strings.Split(addr, "/")[0])
}

// getHostNetworkIPsinNBDB discovers the host-network address set in NBDB and returns its IPs.
// ipFamily must be "v4" or "v6". Handles both pre-5.0 (Namespace-based) and 5.0+ (PodSelector-based) naming.
func getHostNetworkIPsinNBDB(ctx context.Context, oc *CLI, nodeName string, ipFamily string) []string {
	ovnKubePod, podErr := getPodNameOnNode(oc, "openshift-ovn-kubernetes", "app=ovnkube-node", nodeName)
	Expect(podErr).NotTo(HaveOccurred())
	Expect(ovnKubePod).NotTo(BeEmpty())

	candidateExternalIDs := []string{
		`'external_ids:"k8s.ovn.org/id"="default-network-controller:PodSelector:LS{ML:{policy-group.network.openshift.io/host-network: ,},}_LS{}_LNM:` + ipFamily + `"'`,
		`'external_ids:"k8s.ovn.org/id"="default-network-controller:Namespace:openshift-host-network:` + ipFamily + `"'`,
	}

	var cmdOutput string
	var hostNetworkIPs []string
	err := wait.PollUntilContextTimeout(ctx, 3*time.Second, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		for _, extID := range candidateExternalIDs {
			cmd := "ovn-nbctl --column address find address_set " + extID
			out, cmdErr := execInPodContainer(oc, "openshift-ovn-kubernetes", ovnKubePod, "northd", cmd)
			if out != "" && cmdErr == nil {
				e2e.Logf("Found host-network address set (%s) on pod %s using: %s", ipFamily, ovnKubePod, extID)
				cmdOutput = out
				return true, nil
			}
		}
		e2e.Logf("host-network address set (%s) not found yet on pod %s (node %s) — retrying...", ipFamily, ovnKubePod, nodeName)
		return false, nil
	})
	Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to get host network IPs (%s) for node %v", ipFamily, nodeName))

	re := regexp.MustCompile(`"[^",]+"`)
	ipStrs := re.FindAllString(cmdOutput, -1)
	for _, eachIpString := range ipStrs {
		ip := strings.Trim(eachIpString, "\"")
		hostNetworkIPs = append(hostNetworkIPs, ip)
	}
	return hostNetworkIPs
}

func createNetworkPolicy(ctx context.Context, oc *CLI, namespace string) {
	npJSON := fmt.Sprintf(`{
		"apiVersion": "networking.k8s.io/v1",
		"kind": "NetworkPolicy",
		"metadata": {
			"name": "allow-from-all-namespaces",
			"namespace": "%s"
		},
		"spec": {
			"ingress": [{
				"from": [{
					"namespaceSelector": {}
				}]
			}],
			"podSelector": {}
		}
	}`, namespace)

	applyManifest(ctx, oc, npJSON)
}

func getInfrastructureName(oc *CLI) string {
	return oc.AsAdmin().WithoutNamespace().Run("get").Args("infrastructure", "cluster", "-o=jsonpath={.status.infrastructureName}").MustOutput()
}

func createMachineSet(ctx context.Context, oc *CLI, machineSetName string) {
	existingMS := oc.AsAdmin().WithoutNamespace().Run("get").Args("machineset", "-n", "openshift-machine-api", "-o=jsonpath={.items[0].metadata.name}").MustOutput()
	Expect(existingMS).NotTo(BeEmpty(), "No existing MachineSet found to clone")

	msJSON := oc.AsAdmin().WithoutNamespace().Run("get").Args("machineset", existingMS, "-n", "openshift-machine-api", "-o=json").MustOutput()

	msJSON = strings.ReplaceAll(msJSON, existingMS, machineSetName)
	msJSON = regexp.MustCompile(`"uid":\s*"[^"]*"`).ReplaceAllString(msJSON, `"uid": ""`)
	msJSON = regexp.MustCompile(`"resourceVersion":\s*"[^"]*"`).ReplaceAllString(msJSON, `"resourceVersion": ""`)
	msJSON = regexp.MustCompile(`"replicas":\s*\d+`).ReplaceAllString(msJSON, `"replicas": 1`)
	msJSON = regexp.MustCompile(`"creationTimestamp":\s*"[^"]*"`).ReplaceAllString(msJSON, `"creationTimestamp": null`)

	applyManifest(ctx, oc, msJSON)
	e2e.Logf("MachineSet %s created successfully", machineSetName)
}

func deleteMachineSet(oc *CLI, machineSetName string) {
	e2e.Logf("Deleting MachineSet %s", machineSetName)
	err := oc.AsAdmin().WithoutNamespace().Run("delete").Args("machineset", machineSetName, "-n", "openshift-machine-api", "--wait=true").Execute()
	if err != nil {
		e2e.Logf("Warning: failed to delete machineset %s: %v", machineSetName, err)
	}
}

func waitForMachineSetRunning(ctx context.Context, oc *CLI, machineSetName string, replicas int) {
	err := wait.PollUntilContextTimeout(ctx, 10*time.Second, 15*time.Minute, true, func(ctx context.Context) (bool, error) {
		readyReplicas, cmdErr := oc.AsAdmin().WithoutNamespace().Run("get").Args("machineset", machineSetName, "-n", "openshift-machine-api", "-o=jsonpath={.status.readyReplicas}").Output()
		if cmdErr != nil {
			e2e.Logf("Error getting machineset status: %v", cmdErr)
			return false, nil
		}
		if readyReplicas == strconv.Itoa(replicas) {
			e2e.Logf("MachineSet %s has %s ready replicas", machineSetName, readyReplicas)
			return true, nil
		}
		e2e.Logf("Waiting for MachineSet %s: readyReplicas=%s, want=%d", machineSetName, readyReplicas, replicas)
		return false, nil
	})
	Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("MachineSet %s did not reach %d ready replicas", machineSetName, replicas))
}

func getNodeNameFromMachineSet(oc *CLI, machineSetName string) string {
	machineName := oc.AsAdmin().WithoutNamespace().Run("get").Args("machine", "-n", "openshift-machine-api",
		"-l", "machine.openshift.io/cluster-api-machineset="+machineSetName, "-o=jsonpath={.items[0].metadata.name}").MustOutput()
	Expect(machineName).NotTo(BeEmpty())

	nodeName := oc.AsAdmin().WithoutNamespace().Run("get").Args("machine", machineName, "-n", "openshift-machine-api",
		"-o=jsonpath={.status.nodeRef.name}").MustOutput()
	Expect(nodeName).NotTo(BeEmpty(), fmt.Sprintf("Machine %s has no nodeRef", machineName))
	e2e.Logf("Got node name %s from machineset %s", nodeName, machineSetName)
	return nodeName
}

func waitForMachinesDisappear(ctx context.Context, oc *CLI, machineSetName string) {
	err := wait.PollUntilContextTimeout(ctx, 10*time.Second, 10*time.Minute, true, func(ctx context.Context) (bool, error) {
		output, cmdErr := oc.AsAdmin().WithoutNamespace().Run("get").Args("machine", "-n", "openshift-machine-api",
			"-l", "machine.openshift.io/cluster-api-machineset="+machineSetName, "-o=jsonpath={.items}").Output()
		if cmdErr != nil {
			e2e.Logf("Error getting machines: %v", cmdErr)
			return false, nil
		}
		if output == "[]" || output == "" {
			e2e.Logf("All machines from machineset %s have been removed", machineSetName)
			return true, nil
		}
		e2e.Logf("Waiting for machines from machineset %s to disappear...", machineSetName)
		return false, nil
	})
	if err != nil {
		e2e.Logf("Warning: machines from machineset %s did not fully disappear: %v", machineSetName, err)
	}
}
