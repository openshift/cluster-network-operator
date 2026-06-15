package ote

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"

	admissionapi "k8s.io/pod-security-admission/api"

	e2e "k8s.io/kubernetes/test/e2e/framework"
	e2enode "k8s.io/kubernetes/test/e2e/framework/node"
)

var _ = g.Describe("[sig-network][Suite:openshift/conformance/serial] CNO", func() {
	var oc *CLI

	g.BeforeEach(func() {
		oc = NewCLIWithPodSecurityLevel("networking-cno", admissionapi.LevelPrivileged)
		oc.SetupNamespace()
	})

	g.AfterEach(func() {
		oc.TeardownNamespace()
	})

	g.It("[JIRA:Networking][OTP] 72817-Make sure internalJoinSubnet and internalTransitSwitchSubnet is configurable post install as a Day 2 operation", func() {
		var (
			pod1Name          = "hello-pod1"
			pod2Name          = "hello-pod2"
			podLabel          = "hello-pod"
			serviceName       = "test-service-72817"
			servicePort       = 27017
			serviceTargetPort = 8080
		)
		ipStackType := checkIPStackType(oc)
		o.Expect(ipStackType).NotTo(o.BeEmpty())

		nodeList, err := e2enode.GetReadySchedulableNodes(context.TODO(), oc.KubeFramework().ClientSet)
		o.Expect(err).NotTo(o.HaveOccurred())
		if len(nodeList.Items) < 2 {
			g.Skip("This case requires 2 nodes, but the cluster has less than two nodes")
		}

		createPingPodOnNode(oc, pod1Name, oc.Namespace(), podLabel, nodeList.Items[0].Name)
		createPingPodOnNode(oc, pod2Name, oc.Namespace(), podLabel, nodeList.Items[1].Name)

		var ipFamilyPolicy string
		if ipStackType == "ipv4single" {
			ipFamilyPolicy = "SingleStack"
		} else {
			ipFamilyPolicy = "PreferDualStack"
		}
		createGenericService(oc, serviceName, oc.Namespace(), "TCP", podLabel, "ClusterIP", ipFamilyPolicy, "Cluster", "", servicePort, serviceTargetPort)

		customPatchIPv4 := `{"spec":{"defaultNetwork":{"ovnKubernetesConfig":{"ipv4":{"internalJoinSubnet": "100.99.0.0/16","internalTransitSwitchSubnet": "100.69.0.0/16"}}}}}`
		customPatchIPv6 := `{"spec":{"defaultNetwork":{"ovnKubernetesConfig":{"ipv6":{"internalJoinSubnet": "ab98::/64","internalTransitSwitchSubnet": "ab97::/64"}}}}}`
		customPatchDualstack := `{"spec":{"defaultNetwork":{"ovnKubernetesConfig":{"ipv4":{"internalJoinSubnet": "100.99.0.0/16","internalTransitSwitchSubnet": "100.69.0.0/16"},"ipv6": {"internalJoinSubnet": "ab98::/64","internalTransitSwitchSubnet": "ab97::/64"}}}}}`

		currentinternalJoinSubnetIPv4Value, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("Network.operator.openshift.io/cluster", "-o=jsonpath={.spec.defaultNetwork.ovnKubernetesConfig.ipv4.internalJoinSubnet}").Output()
		o.Expect(err).NotTo(o.HaveOccurred())
		currentinternalTransitSwSubnetIPv4Value, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("Network.operator.openshift.io/cluster", "-o=jsonpath={.spec.defaultNetwork.ovnKubernetesConfig.ipv4.internalTransitSwitchSubnet}").Output()
		o.Expect(err).NotTo(o.HaveOccurred())
		currentinternalJoinSubnetIPv6Value, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("Network.operator.openshift.io/cluster", "-o=jsonpath={.spec.defaultNetwork.ovnKubernetesConfig.ipv6.internalJoinSubnet}").Output()
		o.Expect(err).NotTo(o.HaveOccurred())
		currentinternalTransitSwSubnetIPv6Value, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("Network.operator.openshift.io/cluster", "-o=jsonpath={.spec.defaultNetwork.ovnKubernetesConfig.ipv6.internalTransitSwitchSubnet}").Output()
		o.Expect(err).NotTo(o.HaveOccurred())

		if currentinternalJoinSubnetIPv4Value == "" {
			currentinternalJoinSubnetIPv4Value = "100.64.0.0/16"
		}
		if currentinternalJoinSubnetIPv6Value == "" {
			currentinternalJoinSubnetIPv6Value = "fd98::/64"
		}
		if currentinternalTransitSwSubnetIPv4Value == "" {
			currentinternalTransitSwSubnetIPv4Value = "100.88.0.0/16"
		}
		if currentinternalTransitSwSubnetIPv6Value == "" {
			currentinternalTransitSwSubnetIPv6Value = "fd97::/64"
		}

		patchIPv4original := `{"spec":{"defaultNetwork":{"ovnKubernetesConfig":{"ipv4":{"internalJoinSubnet": "` + currentinternalJoinSubnetIPv4Value + `","internalTransitSwitchSubnet": "` + currentinternalTransitSwSubnetIPv4Value + `"}}}}}`
		patchIPv6original := `{"spec":{"defaultNetwork":{"ovnKubernetesConfig":{"ipv6":{"internalJoinSubnet": "` + currentinternalJoinSubnetIPv6Value + `","internalTransitSwitchSubnet": "` + currentinternalTransitSwSubnetIPv6Value + `"}}}}}`
		patchDualstackoriginal := `{"spec":{"defaultNetwork":{"ovnKubernetesConfig":{"ipv4":{"internalJoinSubnet": "` + currentinternalJoinSubnetIPv4Value + `","internalTransitSwitchSubnet": "` + currentinternalTransitSwSubnetIPv4Value + `"},"ipv6": {"internalJoinSubnet": "` + currentinternalJoinSubnetIPv6Value + `","internalTransitSwitchSubnet": "` + currentinternalTransitSwSubnetIPv6Value + `"}}}}}`

		applyPatchWithCleanup := func(customPatch, originalPatch string) {
			g.DeferCleanup(func() {
				patchResourceAsAdmin(oc, "Network.operator.openshift.io/cluster", originalPatch)
				err := checkOVNKState(oc)
				o.Expect(err).NotTo(o.HaveOccurred(), "OVNkube didn't rollout successfully after restoring original configuration")
			})
			patchResourceAsAdmin(oc, "Network.operator.openshift.io/cluster", customPatch)
		}

		switch ipStackType {
		case "ipv4single":
			applyPatchWithCleanup(customPatchIPv4, patchIPv4original)
		case "ipv6single":
			applyPatchWithCleanup(customPatchIPv6, patchIPv6original)
		default:
			applyPatchWithCleanup(customPatchDualstack, patchDualstackoriginal)
		}
		err = checkOVNKState(oc)
		o.Expect(err).NotTo(o.HaveOccurred(), "OVNkube never trigger or rolled out successfully post oc patch")
		curlPod2PodPass(oc, oc.Namespace(), pod1Name, oc.Namespace(), pod2Name, serviceTargetPort)
		curlPod2SvcPass(oc, oc.Namespace(), oc.Namespace(), pod1Name, serviceName, servicePort)
	})

	g.It("[JIRA:Networking][OTP] 51727-ovsdb-server and northd should not core dump on node restart", func() {
		g.By("Get one node to reboot")
		workerList, err := e2enode.GetReadySchedulableNodes(context.TODO(), oc.KubeFramework().ClientSet)
		o.Expect(err).NotTo(o.HaveOccurred())
		if len(workerList.Items) < 1 {
			g.Skip("This case requires 1 node, but the cluster has none")
		}
		worker := workerList.Items[0].Name
		defer checkNodeStatus(oc, worker, "Ready")
		rebootNode(oc, worker)
		checkNodeStatus(oc, worker, "NotReady")
		checkNodeStatus(oc, worker, "Ready")

		g.By("Check the node core dump output")
		mustgatherDir := "/tmp/must-gather-51727"
		defer os.RemoveAll(mustgatherDir)
		_, err = oc.AsAdmin().WithoutNamespace().Run("adm").Args("must-gather", "--dest-dir="+mustgatherDir, "--", "/usr/bin/gather_core_dumps").Output()
		o.Expect(err).NotTo(o.HaveOccurred())

		match, err := filepath.Glob(filepath.Join(mustgatherDir, "*", "node_core_dumps"))
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(len(match)).Should(o.Equal(1))
		files, err := os.ReadDir(match[0])
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(files).Should(o.BeEmpty())
	})

	g.It("[JIRA:Networking][OTP] 72028-Join switch IP and management port IP for newly added node should be synced correctly into NBDB, pod on new node can communicate with old pod on old node", func() {
		ipStackType := checkIPStackType(oc)
		o.Expect(ipStackType).NotTo(o.BeEmpty())

		platform := checkPlatform(oc)
		if platform == "baremetal" || platform == "none" || platform == "ovirt" {
			g.Skip("Skipping on platform " + platform + " - MachineSet creation not supported")
		}

		topology := getControlPlaneTopology(oc)
		if topology == "SingleReplicaTopologyMode" {
			g.Skip("Skipping on SNO - MachineSet scaling not supported")
		}

		g.By("Get an existing schedulable node")
		currentNodeList, err := e2enode.GetReadySchedulableNodes(context.TODO(), oc.KubeFramework().ClientSet)
		o.Expect(err).NotTo(o.HaveOccurred())
		oldNode := currentNodeList.Items[0].Name

		g.By("Create a network policy in the namespace")
		createNetworkPolicy(oc, oc.Namespace())
		output, err := oc.Run("get").Args("networkpolicy").Output()
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(output).To(o.ContainSubstring("allow-from-all-namespaces"))

		g.By("Create a test pod on the existing node")
		createPingPodOnNode(oc, "hello-pod1", oc.Namespace(), "hello-pod", oldNode)

		g.By("Create a new machineset, get the new node")
		infrastructureName := getInfrastructureName(oc)
		machineSetName := infrastructureName + "-72028"
		defer waitForMachinesDisappear(oc, machineSetName)
		defer deleteMachineSet(oc, machineSetName)
		createMachineSet(oc, machineSetName)

		waitForMachineSetRunning(oc, machineSetName, 1)
		newNodeName := getNodeNameFromMachineSet(oc, machineSetName)
		e2e.Logf("Get new node name: %s", newNodeName)

		g.By("Create second namespace and another test pod on the new node")
		ns2 := fmt.Sprintf("e2e-test-72028-%s", getRandomString())
		_, err = oc.AsAdmin().WithoutNamespace().Run("create").Args("namespace", ns2).Output()
		o.Expect(err).NotTo(o.HaveOccurred())
		defer func() {
			_, _ = oc.AsAdmin().WithoutNamespace().Run("delete").Args("namespace", ns2, "--wait=false").Output()
		}()
		_, err = oc.AsAdmin().WithoutNamespace().Run("label").Args("namespace", ns2,
			"pod-security.kubernetes.io/enforce=privileged",
			"pod-security.kubernetes.io/warn=privileged",
			"pod-security.kubernetes.io/audit=privileged",
			"security.openshift.io/scc.podSecurityLabelSync=false",
			"--overwrite",
		).Output()
		o.Expect(err).NotTo(o.HaveOccurred())

		createPingPodOnNode(oc, "hello-pod2", ns2, "hello-pod", newNodeName)

		g.By("Get management IP(s) and join switch IP(s) for the new node")
		var nodeOVNK8sMgmtIPv4, nodeOVNK8sMgmtIPv6 string
		if ipStackType == "dualstack" || ipStackType == "ipv6single" {
			nodeOVNK8sMgmtIPv6 = getOVNK8sNodeMgmtIPv6(oc, newNodeName)
		}
		if ipStackType == "dualstack" || ipStackType == "ipv4single" {
			nodeOVNK8sMgmtIPv4 = getOVNK8sNodeMgmtIPv4(oc, newNodeName)
		}
		e2e.Logf("ipStack type: %s, nodeOVNK8sMgmtIPv4: %s, nodeOVNK8sMgmtIPv6: %s", ipStackType, nodeOVNK8sMgmtIPv4, nodeOVNK8sMgmtIPv6)

		joinSwitchIPv4, joinSwitchIPv6 := getJoinSwitchIPofNode(oc, newNodeName)
		e2e.Logf("Got joinSwitchIPv4: %v, joinSwitchIPv6: %v", joinSwitchIPv4, joinSwitchIPv6)

		g.By("Check host network addresses in each node's northdb")
		allNodeList, nodeErr := getAllNodes(oc)
		o.Expect(nodeErr).NotTo(o.HaveOccurred())
		o.Expect(len(allNodeList)).NotTo(o.BeEquivalentTo(0))

		for _, eachNodeName := range allNodeList {
			ovnKubePod, podErr := getPodNameOnNode(oc, "openshift-ovn-kubernetes", "app=ovnkube-node", eachNodeName)
			o.Expect(podErr).NotTo(o.HaveOccurred())
			o.Expect(ovnKubePod).ShouldNot(o.Equal(""))
			if ipStackType == "dualstack" || ipStackType == "ipv4single" {
				hostNetworkIPsv4 := getHostNetworkIPsinNBDB(oc, eachNodeName, "v4")
				e2e.Logf("Got hostNetworkIPsv4 for node %s : %v", eachNodeName, hostNetworkIPsv4)
				o.Expect(contains(hostNetworkIPsv4, nodeOVNK8sMgmtIPv4)).Should(o.BeTrue(), fmt.Sprintf("New node's mgmt IPv4 is not updated to node %s in NBDB!", eachNodeName))
				o.Expect(unorderedContains(hostNetworkIPsv4, joinSwitchIPv4)).Should(o.BeTrue(), fmt.Sprintf("New node's join switch IPv4 is not updated to node %s in NBDB!", eachNodeName))
			}
			if ipStackType == "dualstack" || ipStackType == "ipv6single" {
				hostNetworkIPsv6 := getHostNetworkIPsinNBDB(oc, eachNodeName, "v6")
				e2e.Logf("Got hostNetworkIPsv6 for node %s : %v", eachNodeName, hostNetworkIPsv6)
				o.Expect(contains(hostNetworkIPsv6, nodeOVNK8sMgmtIPv6)).Should(o.BeTrue(), fmt.Sprintf("New node's mgmt IPv6 is not updated to node %s in NBDB!", eachNodeName))
				o.Expect(unorderedContains(hostNetworkIPsv6, joinSwitchIPv6)).Should(o.BeTrue(), fmt.Sprintf("New node's join switch IPv6 is not updated to node %s in NBDB!", eachNodeName))
			}
		}

		g.By("Verify that new pod on new node can communicate with old pod on old node")
		curlPod2PodPass(oc, oc.Namespace(), "hello-pod1", ns2, "hello-pod2", 8080)
		curlPod2PodPass(oc, ns2, "hello-pod2", oc.Namespace(), "hello-pod1", 8080)
	})
})
