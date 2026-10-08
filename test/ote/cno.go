package ote

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	admissionapi "k8s.io/pod-security-admission/api"

	e2e "k8s.io/kubernetes/test/e2e/framework"
	e2enode "k8s.io/kubernetes/test/e2e/framework/node"
)

var _ = Describe("[sig-network] CNO", func() {
	var oc *CLI

	BeforeEach(func() {
		oc = NewCLIWithPodSecurityLevel("networking-cno", admissionapi.LevelPrivileged)
		oc.SetupNamespace()
	})

	AfterEach(func() {
		oc.TeardownNamespace()
	})

	Specify("[JIRA:Networking][OTP] 72817- internalJoinSubnet and internalTransitSwitchSubnet should be configurable post install as a Day 2 operation", Serial, func(ctx context.Context) {
		var (
			pod1Name          = "hello-pod1"
			pod2Name          = "hello-pod2"
			podLabel          = "hello-pod"
			serviceName       = "test-service-72817"
			servicePort       = 27017
			serviceTargetPort = 8080
		)
		ipStackType := checkIPStackType(oc)
		Expect(ipStackType).NotTo(BeEmpty())

		nodeList, err := e2enode.GetReadySchedulableNodes(ctx, oc.KubeFramework().ClientSet)
		Expect(err).NotTo(HaveOccurred())
		if len(nodeList.Items) < 2 {
			Skip("This case requires 2 nodes, but the cluster has less than two nodes")
		}

		createPingPodOnNode(ctx, oc, pod1Name, oc.Namespace(), podLabel, nodeList.Items[0].Name)
		createPingPodOnNode(ctx, oc, pod2Name, oc.Namespace(), podLabel, nodeList.Items[1].Name)

		var ipFamilyPolicy string
		if ipStackType == "ipv4single" {
			ipFamilyPolicy = "SingleStack"
		} else {
			ipFamilyPolicy = "PreferDualStack"
		}
		createGenericService(ctx, oc, serviceName, oc.Namespace(), "TCP", podLabel, "ClusterIP", ipFamilyPolicy, "Cluster", "", servicePort, serviceTargetPort)

		customPatchIPv4 := `{"spec":{"defaultNetwork":{"ovnKubernetesConfig":{"ipv4":{"internalJoinSubnet": "100.99.0.0/16","internalTransitSwitchSubnet": "100.69.0.0/16"}}}}}`
		customPatchIPv6 := `{"spec":{"defaultNetwork":{"ovnKubernetesConfig":{"ipv6":{"internalJoinSubnet": "ab98::/64","internalTransitSwitchSubnet": "ab97::/64"}}}}}`
		customPatchDualstack := `{"spec":{"defaultNetwork":{"ovnKubernetesConfig":{"ipv4":{"internalJoinSubnet": "100.99.0.0/16","internalTransitSwitchSubnet": "100.69.0.0/16"},"ipv6": {"internalJoinSubnet": "ab98::/64","internalTransitSwitchSubnet": "ab97::/64"}}}}}`

		currentinternalJoinSubnetIPv4Value := oc.AsAdmin().WithoutNamespace().Run("get").Args("Network.operator.openshift.io/cluster", "-o=jsonpath={.spec.defaultNetwork.ovnKubernetesConfig.ipv4.internalJoinSubnet}").MustOutput()
		currentinternalTransitSwSubnetIPv4Value := oc.AsAdmin().WithoutNamespace().Run("get").Args("Network.operator.openshift.io/cluster", "-o=jsonpath={.spec.defaultNetwork.ovnKubernetesConfig.ipv4.internalTransitSwitchSubnet}").MustOutput()
		currentinternalJoinSubnetIPv6Value := oc.AsAdmin().WithoutNamespace().Run("get").Args("Network.operator.openshift.io/cluster", "-o=jsonpath={.spec.defaultNetwork.ovnKubernetesConfig.ipv6.internalJoinSubnet}").MustOutput()
		currentinternalTransitSwSubnetIPv6Value := oc.AsAdmin().WithoutNamespace().Run("get").Args("Network.operator.openshift.io/cluster", "-o=jsonpath={.spec.defaultNetwork.ovnKubernetesConfig.ipv6.internalTransitSwitchSubnet}").MustOutput()

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
			DeferCleanup(func(ctx context.Context) {
				patchResourceAsAdmin(oc, "Network.operator.openshift.io/cluster", originalPatch)
				err := checkOVNKState(ctx, oc)
				Expect(err).NotTo(HaveOccurred(), "OVNkube didn't rollout successfully after restoring original configuration")
			})
			patchResourceAsAdmin(oc, "Network.operator.openshift.io/cluster", customPatch)
			err := checkOVNKState(ctx, oc)
			Expect(err).NotTo(HaveOccurred(), "OVNkube didn't rollout successfully after applying patch")
		}

		switch ipStackType {
		case "ipv4single":
			applyPatchWithCleanup(customPatchIPv4, patchIPv4original)
		case "ipv6single":
			applyPatchWithCleanup(customPatchIPv6, patchIPv6original)
		default:
			applyPatchWithCleanup(customPatchDualstack, patchDualstackoriginal)
		}
		err = checkOVNKState(ctx, oc)
		Expect(err).NotTo(HaveOccurred(), "OVNkube never trigger or rolled out successfully post oc patch")
		curlPod2PodPass(oc, oc.Namespace(), pod1Name, oc.Namespace(), pod2Name, serviceTargetPort)
		curlPod2SvcPass(ctx, oc, oc.Namespace(), oc.Namespace(), pod1Name, serviceName, servicePort)
	})

	Specify("[JIRA:Networking][OTP] 51727-ovsdb-server and northd should not core dump on node restart", Serial, func(ctx context.Context) {
		By("Get one node to reboot")
		workerList, err := e2enode.GetReadySchedulableNodes(ctx, oc.KubeFramework().ClientSet)
		Expect(err).NotTo(HaveOccurred())
		if len(workerList.Items) < 1 {
			Skip("This case requires 1 node, but the cluster has none")
		}
		worker := workerList.Items[0].Name
		DeferCleanup(checkNodeStatus, oc, worker, "Ready")
		rebootNode(oc, worker)
		checkNodeStatus(ctx, oc, worker, "NotReady")
		checkNodeStatus(ctx, oc, worker, "Ready")

		By("Check the node core dump output")
		mustgatherDir := "/tmp/must-gather-51727"
		DeferCleanup(func() {
			_ = os.RemoveAll(mustgatherDir)
		})
		err = oc.AsAdmin().WithoutNamespace().Run("adm").Args("must-gather", "--dest-dir="+mustgatherDir, "--", "/usr/bin/gather_core_dumps").Execute()
		Expect(err).NotTo(HaveOccurred())

		match, err := filepath.Glob(filepath.Join(mustgatherDir, "*", "node_core_dumps"))
		Expect(err).NotTo(HaveOccurred())
		Expect(len(match)).Should(Equal(1))
		files, err := os.ReadDir(match[0])
		Expect(err).NotTo(HaveOccurred())
		Expect(files).Should(BeEmpty())
	})

	Specify("[JIRA:Networking][OTP] 72028-Join switch IP and management port IP for newly added node should be synced correctly into NBDB, pod on new node can communicate with old pod on old node", Serial, func(ctx context.Context) {
		ipStackType := checkIPStackType(oc)
		Expect(ipStackType).NotTo(BeEmpty())

		platform := checkPlatform(oc)
		if platform == "baremetal" || platform == "none" || platform == "ovirt" {
			Skip("Skipping on platform " + platform + " - MachineSet creation not supported")
		}

		topology := getControlPlaneTopology(oc)
		if topology == "SingleReplicaTopologyMode" {
			Skip("Skipping on SNO - MachineSet scaling not supported")
		}

		By("Get an existing schedulable node")
		currentNodeList, err := e2enode.GetReadySchedulableNodes(ctx, oc.KubeFramework().ClientSet)
		Expect(err).NotTo(HaveOccurred())
		oldNode := currentNodeList.Items[0].Name

		By("Create a network policy in the namespace")
		createNetworkPolicy(ctx, oc, oc.Namespace())
		output := oc.Run("get").Args("networkpolicy").MustOutput()
		Expect(output).To(ContainSubstring("allow-from-all-namespaces"))

		By("Create a test pod on the existing node")
		createPingPodOnNode(ctx, oc, "hello-pod1", oc.Namespace(), "hello-pod", oldNode)

		By("Create a new machineset, get the new node")
		infrastructureName := getInfrastructureName(oc)
		machineSetName := infrastructureName + "-72028"
		DeferCleanup(waitForMachinesDisappear, oc, machineSetName)
		DeferCleanup(deleteMachineSet, oc, machineSetName)
		createMachineSet(ctx, oc, machineSetName)

		waitForMachineSetRunning(ctx, oc, machineSetName, 1)
		newNodeName := getNodeNameFromMachineSet(oc, machineSetName)
		e2e.Logf("Get new node name: %s", newNodeName)

		By("Create second namespace and another test pod on the new node")
		ns2 := fmt.Sprintf("e2e-test-72028-%s", getRandomString())
		err = oc.AsAdmin().WithoutNamespace().Run("create").Args("namespace", ns2).Execute()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			_ = oc.AsAdmin().WithoutNamespace().Run("delete").Args("namespace", ns2, "--wait=false").Execute()
		})
		err = oc.AsAdmin().WithoutNamespace().Run("label").Args("namespace", ns2,
			"pod-security.kubernetes.io/enforce=privileged",
			"pod-security.kubernetes.io/warn=privileged",
			"pod-security.kubernetes.io/audit=privileged",
			"security.openshift.io/scc.podSecurityLabelSync=false",
			"--overwrite",
		).Execute()
		Expect(err).NotTo(HaveOccurred())

		createPingPodOnNode(ctx, oc, "hello-pod2", ns2, "hello-pod", newNodeName)

		By("Get management IP(s) and join switch IP(s) for the new node")
		var nodeOVNK8sMgmtIPv4, nodeOVNK8sMgmtIPv6 string
		if ipStackType == "dualstack" || ipStackType == "ipv6single" {
			nodeOVNK8sMgmtIPv6 = getOVNK8sNodeMgmtIPv6(ctx, oc, newNodeName)
		}
		if ipStackType == "dualstack" || ipStackType == "ipv4single" {
			nodeOVNK8sMgmtIPv4 = getOVNK8sNodeMgmtIPv4(ctx, oc, newNodeName)
		}
		e2e.Logf("ipStack type: %s, nodeOVNK8sMgmtIPv4: %s, nodeOVNK8sMgmtIPv6: %s", ipStackType, nodeOVNK8sMgmtIPv4, nodeOVNK8sMgmtIPv6)

		joinSwitchIPv4, joinSwitchIPv6 := getJoinSwitchIPofNode(ctx, oc, newNodeName)
		e2e.Logf("Got joinSwitchIPv4: %v, joinSwitchIPv6: %v", joinSwitchIPv4, joinSwitchIPv6)

		By("Check host network addresses for the newly added node in northdb")
		ovnKubePod, podErr := getPodNameOnNode(oc, "openshift-ovn-kubernetes", "app=ovnkube-node", newNodeName)
		Expect(podErr).NotTo(HaveOccurred())
		Expect(ovnKubePod).NotTo(BeEmpty())
		if ipStackType == "dualstack" || ipStackType == "ipv4single" {
			hostNetworkIPsv4 := getHostNetworkIPsinNBDB(ctx, oc, newNodeName, "v4")
			e2e.Logf("Got hostNetworkIPsv4 for node %s : %v", newNodeName, hostNetworkIPsv4)
			Expect(slices.Contains(hostNetworkIPsv4, nodeOVNK8sMgmtIPv4)).Should(BeTrue(), fmt.Sprintf("New node's mgmt IPv4 is not updated in node %s NBDB!", newNodeName))
			Expect(hostNetworkIPsv4).To(ContainElements(joinSwitchIPv4), fmt.Sprintf("New node's join switch IPv4 is not updated in node %s NBDB!", newNodeName))
		}
		if ipStackType == "dualstack" || ipStackType == "ipv6single" {
			hostNetworkIPsv6 := getHostNetworkIPsinNBDB(ctx, oc, newNodeName, "v6")
			e2e.Logf("Got hostNetworkIPsv6 for node %s : %v", newNodeName, hostNetworkIPsv6)
			Expect(slices.Contains(hostNetworkIPsv6, nodeOVNK8sMgmtIPv6)).Should(BeTrue(), fmt.Sprintf("New node's mgmt IPv6 is not updated in node %s NBDB!", newNodeName))
			Expect(hostNetworkIPsv6).To(ContainElements(joinSwitchIPv6), fmt.Sprintf("New node's join switch IPv6 is not updated in node %s NBDB!", newNodeName))
		}

		By("Verify that new pod on new node can communicate with old pod on old node")
		curlPod2PodPass(oc, oc.Namespace(), "hello-pod1", ns2, "hello-pod2", 8080)
		curlPod2PodPass(oc, ns2, "hello-pod2", oc.Namespace(), "hello-pod1", 8080)
	})
})
