package e2e

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/util/wait"
	e2e "k8s.io/kubernetes/test/e2e/framework"
	e2eoutput "k8s.io/kubernetes/test/e2e/framework/pod/output"
	netutils "k8s.io/utils/net"
)

func init() {
	// Initialize the Kubernetes e2e framework's TestContext with the kubeconfig
	if kubeconfig := os.Getenv("KUBECONFIG"); kubeconfig != "" {
		e2e.TestContext.KubeConfig = kubeconfig
	}
	// Set kubectl path for the e2e framework
	e2e.TestContext.KubectlPath = "kubectl"
	// Initialize the cloud provider to prevent nil pointer dereference
	e2e.TestContext.CloudConfig.Provider = e2e.NullProvider{}
	// Set namespace deletion policy
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

func checkIPStackType(oc *CLI) string {
	svcNetwork, err := oc.WithoutNamespace().AsAdmin().Run("get").Args("network.operator", "cluster", "-o=jsonpath={.spec.serviceNetwork}").Output()
	o.Expect(err).NotTo(o.HaveOccurred())
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
	o.Expect(err).NotTo(o.HaveOccurred())
	e2e.Logf("The pod  %s status in namespace %s is %q", podName, namespace, podStatus)
	return podStatus, err
}

func checkPodReady(oc *CLI, namespace string, podName string) (bool, error) {
	podOutPut, err := getPodStatus(oc, namespace, podName)
	status := []string{"Running", "Ready", "Complete", "Succeeded"}
	return contains(status, podOutPut), err
}

func contains(s []string, str string) bool {
	for _, v := range s {
		if v == str {
			return true
		}
	}

	return false
}
func waitPodReady(oc *CLI, namespace string, podName string) {
	err := wait.PollUntilContextTimeout(context.Background(), 5*time.Second, 60*time.Second, true, func(ctx context.Context) (bool, error) {
		status, err1 := checkPodReady(oc, namespace, podName)
		if err1 != nil {
			e2e.Logf("the err:%v, wait for pod %v to become ready.", err1, podName)
			return status, err1
		}
		if !status {
			return status, nil
		}
		return status, nil
	})

	if err != nil {
		podDescribe := describePod(oc, namespace, podName)
		e2e.Logf("oc describe pod %v.", podName)
		e2e.Logf("%s", podDescribe)
	}
	o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("pod %v is not ready", podName))
}
func describePod(oc *CLI, namespace string, podName string) string {
	podDescribe, err := oc.WithoutNamespace().Run("describe").Args("pod", "-n", namespace, podName).Output()
	o.Expect(err).NotTo(o.HaveOccurred())
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
	o.Expect(err).NotTo(o.HaveOccurred())
}

// IsHypershiftHostedCluster
func isHypershiftHostedCluster(oc *CLI) bool {
	topology, err := oc.WithoutNamespace().AsAdmin().Run("get").Args("infrastructures.config.openshift.io", "cluster", "-o=jsonpath={.status.controlPlaneTopology}").Output()
	o.Expect(err).NotTo(o.HaveOccurred())
	e2e.Logf("topology is %s", topology)
	if topology == "" {
		status, _ := oc.WithoutNamespace().AsAdmin().Run("get").Args("infrastructures.config.openshift.io", "cluster", "-o=jsonpath={.status}").Output()
		e2e.Logf("cluster status %s", status)
		e2e.Failf("failure: controlPlaneTopology returned empty")
	}
	return strings.Compare(topology, "External") == 0
}

// Check OVNK health: OVNK pods health and ovnkube-node DS health
func checkOVNKState(oc *CLI) error {
	// check all OVNK pods
	err := waitForPodWithLabelReady(oc, "openshift-ovn-kubernetes", "app=ovnkube-node")
	o.Expect(err).NotTo(o.HaveOccurred())

	if !isHypershiftHostedCluster(oc) {
		err = waitForPodWithLabelReady(oc, "openshift-ovn-kubernetes", "app=ovnkube-control-plane")
		o.Expect(err).NotTo(o.HaveOccurred())
	}
	// check ovnkube-node ds rollout status and confirm if rollout has triggered
	return wait.PollUntilContextTimeout(context.Background(), 10*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		status, err := oc.AsAdmin().WithoutNamespace().Run("rollout").Args("status", "-n", "openshift-ovn-kubernetes", "ds", "ovnkube-node", "--timeout", "5m").Output()
		o.Expect(err).NotTo(o.HaveOccurred())
		if strings.Contains(status, "rollout to finish") && strings.Contains(status, "successfully rolled out") {
			e2e.Logf("ovnkube rollout was triggerred and rolled out successfully")
			return true, nil
		}
		e2e.Logf("ovnkube rollout trigger hasn't happened yet. Trying again")
		return false, nil
	})
}
func waitForPodWithLabelReady(oc *CLI, ns, label string) error {
	return wait.PollUntilContextTimeout(context.Background(), 5*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
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

// CurlPod2PodPass checks connectivity across pods regardless of network addressing type on cluster
func curlPod2PodPass(oc *CLI, namespaceSrc string, podNameSrc string, namespaceDst string, podNameDst string, podPort int) {
	podIP1, podIP2 := getPodIP(oc, namespaceDst, podNameDst)
	if podIP2 != "" {
		_, err := e2eoutput.RunHostCmd(namespaceSrc, podNameSrc, "curl --connect-timeout 5 -s "+net.JoinHostPort(podIP1, strconv.Itoa(podPort)))
		o.Expect(err).NotTo(o.HaveOccurred())
		_, err = e2eoutput.RunHostCmd(namespaceSrc, podNameSrc, "curl --connect-timeout 5 -s "+net.JoinHostPort(podIP2, strconv.Itoa(podPort)))
		o.Expect(err).NotTo(o.HaveOccurred())
	} else {
		_, err := e2eoutput.RunHostCmd(namespaceSrc, podNameSrc, "curl --connect-timeout 5 -s "+net.JoinHostPort(podIP1, strconv.Itoa(podPort)))
		o.Expect(err).NotTo(o.HaveOccurred())
	}
}

// getPodIP returns IPv6 and IPv4 in vars in order on dual stack respectively and main IP in case of single stack (v4 or v6) in 1st var, and nil in 2nd var
func getPodIP(oc *CLI, namespace string, podName string) (string, string) {
	ipStack := checkIPStackType(oc)
	switch ipStack {
	case "ipv6single", "ipv4single":
		podIP, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("pod", "-n", namespace, podName, "-o=jsonpath={.status.podIPs[0].ip}").Output()
		o.Expect(err).NotTo(o.HaveOccurred())
		e2e.Logf("The pod  %s IP in namespace %s is %q", podName, namespace, podIP)
		return podIP, ""
	case "dualstack":
		podIP1, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("pod", "-n", namespace, podName, "-o=jsonpath={.status.podIPs[1].ip}").Output()
		o.Expect(err).NotTo(o.HaveOccurred())
		e2e.Logf("The pod's %s 1st IP in namespace %s is %q", podName, namespace, podIP1)
		podIP2, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("pod", "-n", namespace, podName, "-o=jsonpath={.status.podIPs[0].ip}").Output()
		o.Expect(err).NotTo(o.HaveOccurred())
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

// CurlPod2SvcPass checks pod to svc connectivity regardless of network addressing type on cluster
func curlPod2SvcPass(oc *CLI, namespaceSrc string, namespaceSvc string, podNameSrc string, svcName string, svcPort int) {
	svcIP1, svcIP2 := getSvcIP(oc, namespaceSvc, svcName)
	if svcIP2 != "" {
		_, err := e2eoutput.RunHostCmdWithRetries(namespaceSrc, podNameSrc, "curl --connect-timeout 5 -s "+net.JoinHostPort(svcIP1, strconv.Itoa(svcPort)), 3*time.Second, 15*time.Second)
		o.Expect(err).NotTo(o.HaveOccurred())
		_, err = e2eoutput.RunHostCmdWithRetries(namespaceSrc, podNameSrc, "curl --connect-timeout 5 -s "+net.JoinHostPort(svcIP2, strconv.Itoa(svcPort)), 3*time.Second, 15*time.Second)
		o.Expect(err).NotTo(o.HaveOccurred())
	} else {
		_, err := e2eoutput.RunHostCmdWithRetries(namespaceSrc, podNameSrc, "curl --connect-timeout 5 -s "+net.JoinHostPort(svcIP1, strconv.Itoa(svcPort)), 3*time.Second, 15*time.Second)
		o.Expect(err).NotTo(o.HaveOccurred())
	}
}

/*
getSvcIP returns IPv6 and IPv4 in vars in order on dual stack respectively and main Svc IP in case of single stack (v4 or v6) in 1st var, and nil in 2nd var.
LoadBalancer svc will return Ingress VIP in var1, v4 or v6 and NodePort svc will return Ingress SvcIP in var1 and NodePort in var2
*/
func getSvcIP(oc *CLI, namespace string, svcName string) (string, string) {
	ipStack := checkIPStackType(oc)
	svctype, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("service", "-n", namespace, svcName, "-o=jsonpath={.spec.type}").Output()
	o.Expect(err).NotTo(o.HaveOccurred())
	ipFamilyType, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("service", "-n", namespace, svcName, "-o=jsonpath={.spec.ipFamilyPolicy}").Output()
	o.Expect(err).NotTo(o.HaveOccurred())
	if (svctype == "ClusterIP") || (svctype == "NodePort") {
		if (ipStack == "ipv6single") || (ipStack == "ipv4single") {
			svcIP, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("service", "-n", namespace, svcName, "-o=jsonpath={.spec.clusterIPs[0]}").Output()
			o.Expect(err).NotTo(o.HaveOccurred())
			if svctype == "ClusterIP" {
				e2e.Logf("The service %s IP in namespace %s is %q", svcName, namespace, svcIP)
				return svcIP, ""
			}
			nodePort, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("service", "-n", namespace, svcName, "-o=jsonpath={.spec.ports[*].nodePort}").Output()
			o.Expect(err).NotTo(o.HaveOccurred())
			e2e.Logf("The NodePort service %s IP and NodePort in namespace %s is %s %s", svcName, namespace, svcIP, nodePort)
			return svcIP, nodePort

		} else if (ipStack == "dualstack" && ipFamilyType == "PreferDualStack") || (ipStack == "dualstack" && ipFamilyType == "RequireDualStack") {
			ipFamilyPrecedence, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("service", "-n", namespace, svcName, "-o=jsonpath={.spec.ipFamilies[0]}").Output()
			o.Expect(err).NotTo(o.HaveOccurred())
			//if IPv4 is listed first in ipFamilies then clustrIPs allocation will take order as Ipv4 first and then Ipv6 else reverse
			svcIPv4, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("service", "-n", namespace, svcName, "-o=jsonpath={.spec.clusterIPs[0]}").Output()
			o.Expect(err).NotTo(o.HaveOccurred())
			e2e.Logf("The service %s IP in namespace %s is %q", svcName, namespace, svcIPv4)
			svcIPv6, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("service", "-n", namespace, svcName, "-o=jsonpath={.spec.clusterIPs[1]}").Output()
			o.Expect(err).NotTo(o.HaveOccurred())
			e2e.Logf("The service %s IP in namespace %s is %q", svcName, namespace, svcIPv6)
			/*As stated Nodeport type svc will return node port value in 2nd var. We don't care about what svc address is coming in 1st var as we evetually going to get
			node IPs later and use that in curl operation to node_ip:nodeport*/
			if ipFamilyPrecedence == "IPv4" {
				e2e.Logf("The ipFamilyPrecedence is Ipv4, Ipv6")
				switch svctype {
				case "NodePort":
					nodePort, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("service", "-n", namespace, svcName, "-o=jsonpath={.spec.ports[*].nodePort}").Output()
					o.Expect(err).NotTo(o.HaveOccurred())
					e2e.Logf("The Dual Stack NodePort service %s IP and NodePort in namespace %s is %s %s", svcName, namespace, svcIPv4, nodePort)
					return svcIPv4, nodePort
				default:
					return svcIPv6, svcIPv4
				}
			} else {
				e2e.Logf("The ipFamilyPrecedence is Ipv6, Ipv4")
				switch svctype {
				case "NodePort":
					nodePort, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("service", "-n", namespace, svcName, "-o=jsonpath={.spec.ports[*].nodePort}").Output()
					o.Expect(err).NotTo(o.HaveOccurred())
					e2e.Logf("The Dual Stack NodePort service %s IP and NodePort in namespace %s is %s %s", svcName, namespace, svcIPv6, nodePort)
					return svcIPv6, nodePort
				default:
					svcIPv4, svcIPv6 = svcIPv6, svcIPv4
					return svcIPv6, svcIPv4
				}
			}
		} else {
			//Its a Dual Stack Cluster with SingleStack ipFamilyPolicy
			svcIP, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("service", "-n", namespace, svcName, "-o=jsonpath={.spec.clusterIPs[0]}").Output()
			o.Expect(err).NotTo(o.HaveOccurred())
			e2e.Logf("The service %s IP in namespace %s is %q", svcName, namespace, svcIP)
			return svcIP, ""
		}
	} else {
		//Loadbalancer will be supported for single stack Ipv4 here for mostly GCP,Azure. We can take further enhancements wrt Metal platforms in Metallb utils later
		e2e.Logf("The serviceType is LoadBalancer")
		platform := checkPlatform(oc)
		var jsonString string
		if platform == "aws" {
			jsonString = "-o=jsonpath={.status.loadBalancer.ingress[0].hostname}"
		} else {
			jsonString = "-o=jsonpath={.status.loadBalancer.ingress[0].ip}"
		}

		err := wait.PollUntilContextTimeout(context.Background(), 30*time.Second, 300*time.Second, true, func(ctx context.Context) (bool, error) {
			svcIP, er := oc.AsAdmin().WithoutNamespace().Run("get").Args("service", "-n", namespace, svcName, jsonString).Output()
			o.Expect(er).NotTo(o.HaveOccurred())
			if svcIP == "" {
				e2e.Logf("Waiting for lb service IP assignment. Trying again...")
				return false, nil
			}
			return true, nil
		})
		o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("fail to assign lb svc IP to %v", svcName))
		lbSvcIP, _ := oc.AsAdmin().WithoutNamespace().Run("get").Args("service", "-n", namespace, svcName, jsonString).Output()
		e2e.Logf("The %s lb service Ingress VIP in namespace %s is %q", svcName, namespace, lbSvcIP)
		return lbSvcIP, ""
	}
}

// CheckPlatform check the cluster's platform
func checkPlatform(oc *CLI) string {
	output, _ := oc.AsAdmin().WithoutNamespace().Run("get").Args("infrastructure", "cluster", "-o=jsonpath={.status.platformStatus.type}").Output()
	return strings.ToLower(output)
}

// createPingPodOnNode creates a simple hello pod on a specific node and waits for it to be ready
// The pod runs a simple HTTP server on port 8080
func createPingPodOnNode(oc *CLI, podName, namespace, label, nodeName string) {
	// Create pod definition using JSON
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

	// Write pod definition to a temporary file
	tmpFile := fmt.Sprintf("/tmp/pod-%s-%s.json", podName, getRandomString())
	err := os.WriteFile(tmpFile, []byte(podJSON), 0644)
	o.Expect(err).NotTo(o.HaveOccurred())

	// Clean up the temporary file after use
	defer func() {
		if err := os.Remove(tmpFile); err != nil {
			e2e.Logf("warning: failed to remove temporary file %s: %v", tmpFile, err)
		}
	}()

	// Apply the pod definition
	g.By(fmt.Sprintf("Creating pod %s on node %s", podName, nodeName))
	err = wait.PollUntilContextTimeout(context.Background(), 3*time.Second, 20*time.Second, true, func(ctx context.Context) (bool, error) {
		err1 := oc.AsAdmin().WithoutNamespace().Run("apply").Args("-f", tmpFile).Execute()
		if err1 != nil {
			e2e.Logf("Failed to create pod: %v, retrying...", err1)
			return false, nil
		}
		return true, nil
	})
	o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("failed to create pod %v", podName))

	// Wait for the pod to be ready
	waitPodReady(oc, namespace, podName)
	e2e.Logf("Pod %s is ready on node %s", podName, nodeName)
}

// createGenericService creates a service with the specified parameters using JSON definition
func createGenericService(oc *CLI, serviceName, namespace, protocol, selector, serviceType, ipFamilyPolicy, internalTrafficPolicy, externalTrafficPolicy string, servicePort, serviceTargetPort int) {
	// Build the service JSON definition
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

	// Write service definition to a temporary file
	tmpFile := fmt.Sprintf("/tmp/service-%s-%s.json", serviceName, getRandomString())
	err := os.WriteFile(tmpFile, []byte(serviceJSON), 0644)
	o.Expect(err).NotTo(o.HaveOccurred())

	// Clean up the temporary file after use
	defer func() {
		if err := os.Remove(tmpFile); err != nil {
			e2e.Logf("warning: failed to remove temporary file %s: %v", tmpFile, err)
		}
	}()

	// Apply the service definition
	g.By(fmt.Sprintf("Creating service %s in namespace %s", serviceName, namespace))
	err = wait.PollUntilContextTimeout(context.Background(), 3*time.Second, 20*time.Second, true, func(ctx context.Context) (bool, error) {
		err1 := oc.AsAdmin().WithoutNamespace().Run("apply").Args("-f", tmpFile).Execute()
		if err1 != nil {
			e2e.Logf("Failed to create service: %v, retrying...", err1)
			return false, nil
		}
		return true, nil
	})
	o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("failed to create service %v", serviceName))
	e2e.Logf("Service %s created successfully in namespace %s", serviceName, namespace)
}
