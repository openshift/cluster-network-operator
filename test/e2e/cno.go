package e2e

import (
	"context"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"

	admissionapi "k8s.io/pod-security-admission/api"

	e2enode "k8s.io/kubernetes/test/e2e/framework/node"
)

var _ = g.Describe("[sig-network][Suite:openshift/cluster-network-operator/disruptive][Serial][Disruptive] CNO", func() {
	var oc *CLI

	g.BeforeEach(func() {
		oc = NewCLIWithPodSecurityLevel("networking-cno", admissionapi.LevelBaseline)
		oc.SetupNamespace()
	})

	g.AfterEach(func() {
		oc.TeardownNamespace()
	})

	g.It("[PolarionID:73205][OTP] Make sure internalJoinSubnet and internalTransitSwitchSubnet is configurable post install as a Day 2 operation", func() {
		var (
			pod1Name          = "hello-pod1"
			pod2Name          = "hello-pod2"
			podLabel          = "hello-pod"
			serviceName       = "test-service-73205"
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

		// Create hello-pod1 on the first node
		createPingPodOnNode(oc, pod1Name, oc.Namespace(), podLabel, nodeList.Items[0].Name)

		// Create hello-pod2 on the second node
		createPingPodOnNode(oc, pod2Name, oc.Namespace(), podLabel, nodeList.Items[1].Name)

		// Determine ipFamilyPolicy based on cluster type
		var ipFamilyPolicy string
		if ipStackType == "ipv4single" {
			ipFamilyPolicy = "SingleStack"
		} else {
			ipFamilyPolicy = "PreferDualStack"
		}
		internalTrafficPolicy := "Cluster"
		externalTrafficPolicy := ""
		// Create service backing both pods
		createGenericService(oc, serviceName, oc.Namespace(), "TCP", podLabel, "ClusterIP", ipFamilyPolicy, internalTrafficPolicy, externalTrafficPolicy, servicePort, serviceTargetPort)
		// custom patches to test depending on type of cluster addressing
		customPatchIPv4 := "{\"spec\":{\"defaultNetwork\":{\"ovnKubernetesConfig\":{\"ipv4\":{\"internalJoinSubnet\": \"100.99.0.0/16\",\"internalTransitSwitchSubnet\": \"100.69.0.0/16\"}}}}}"
		customPatchIPv6 := "{\"spec\":{\"defaultNetwork\":{\"ovnKubernetesConfig\":{\"ipv6\":{\"internalJoinSubnet\": \"ab98::/64\",\"internalTransitSwitchSubnet\": \"ab97::/64\"}}}}}"
		customPatchDualstack := "{\"spec\":{\"defaultNetwork\":{\"ovnKubernetesConfig\":{\"ipv4\":{\"internalJoinSubnet\": \"100.99.0.0/16\",\"internalTransitSwitchSubnet\": \"100.69.0.0/16\"},\"ipv6\": {\"internalJoinSubnet\": \"ab98::/64\",\"internalTransitSwitchSubnet\": \"ab97::/64\"}}}}}"

		// gather original cluster values so that we can defer to them later once test done
		currentinternalJoinSubnetIPv4Value, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("Network.operator.openshift.io/cluster", "-o=jsonpath={.spec.defaultNetwork.ovnKubernetesConfig.ipv4.internalJoinSubnet}").Output()
		o.Expect(err).NotTo(o.HaveOccurred())
		currentinternalTransitSwSubnetIPv4Value, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("Network.operator.openshift.io/cluster", "-o=jsonpath={.spec.defaultNetwork.ovnKubernetesConfig.ipv4.internalTransitSwitchSubnet}").Output()
		o.Expect(err).NotTo(o.HaveOccurred())
		currentinternalJoinSubnetIPv6Value, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("Network.operator.openshift.io/cluster", "-o=jsonpath={.spec.defaultNetwork.ovnKubernetesConfig.ipv6.internalJoinSubnet}").Output()
		o.Expect(err).NotTo(o.HaveOccurred())
		currentinternalTransitSwSubnetIPv6Value, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("Network.operator.openshift.io/cluster", "-o=jsonpath={.spec.defaultNetwork.ovnKubernetesConfig.ipv6.internalTransitSwitchSubnet}").Output()
		o.Expect(err).NotTo(o.HaveOccurred())

		// if any of value is null on exisiting cluster, it indicates that cluster came up with following default values assigned by OVNK
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

		// vars to patch cluster back to original state
		patchIPv4original := "{\"spec\":{\"defaultNetwork\":{\"ovnKubernetesConfig\":{\"ipv4\":{\"internalJoinSubnet\": \"" + currentinternalJoinSubnetIPv4Value + "\",\"internalTransitSwitchSubnet\": \"" + currentinternalTransitSwSubnetIPv4Value + "\"}}}}}"
		patchIPv6original := "{\"spec\":{\"defaultNetwork\":{\"ovnKubernetesConfig\":{\"ipv6\":{\"internalJoinSubnet\": \"" + currentinternalJoinSubnetIPv6Value + "\",\"internalTransitSwitchSubnet\": \"" + currentinternalTransitSwSubnetIPv6Value + "\"}}}}}"
		patchDualstackoriginal := "{\"spec\":{\"defaultNetwork\":{\"ovnKubernetesConfig\":{\"ipv4\":{\"internalJoinSubnet\": \"" + currentinternalJoinSubnetIPv4Value + "\",\"internalTransitSwitchSubnet\": \"" + currentinternalTransitSwSubnetIPv4Value + "\"},\"ipv6\": {\"internalJoinSubnet\": \"" + currentinternalJoinSubnetIPv6Value + "\",\"internalTransitSwitchSubnet\": \"" + currentinternalTransitSwSubnetIPv6Value + "\"}}}}}"

		// Helper function to apply patch and schedule cleanup
		applyPatchWithCleanup := func(customPatch, originalPatch string) {
			g.DeferCleanup(func() {
				patchResourceAsAdmin(oc, "Network.operator.openshift.io/cluster", originalPatch)
				err := checkOVNKState(oc)
				o.Expect(err).NotTo(o.HaveOccurred(), "OVNkube didn't rollout successfully after restoring original configuration")
			})
			patchResourceAsAdmin(oc, "Network.operator.openshift.io/cluster", customPatch)
		}

		// Apply the appropriate patch based on IP stack type
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
		// check usual svc and pod connectivities post migration which also ensures disruption doesn't last post successful rollout
		curlPod2PodPass(oc, oc.Namespace(), pod1Name, oc.Namespace(), pod2Name, serviceTargetPort)
		curlPod2SvcPass(oc, oc.Namespace(), oc.Namespace(), pod1Name, serviceName, servicePort)
	})
})
