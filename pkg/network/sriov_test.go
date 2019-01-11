package network

import (
	"testing"

	netv1 "github.com/openshift/cluster-network-operator/pkg/apis/networkoperator/v1"
	"github.com/openshift/cluster-network-operator/pkg/apply"

	. "github.com/onsi/gomega"
)

var testConfig = netv1.NetworkConfig{
	Spec: netv1.NetworkConfigSpec{
		ServiceNetwork: "172.30.0.0/16",
		ClusterNetworks: []netv1.ClusterNetwork{
			{
				CIDR:             "10.128.0.0/15",
				HostSubnetLength: 9,
			},
		},
		DefaultNetwork: netv1.DefaultNetworkDefinition{
			Type: netv1.NetworkTypeOpenShiftSDN,
			OpenShiftSDNConfig: &netv1.OpenShiftSDNConfig{
				Mode: netv1.SDNModeNetworkPolicy,
			},
		},
	},
}

// TestRenderSRIOV has some simple rendering tests
func TestRenderSRIOV(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := testConfig.DeepCopy()
	config := &crd.Spec
	disabled := true
	config.DisableMultiNetwork = &disabled
	FillDefaults(config, nil)

	// disable Multus (and thus SRIOV)
	objs, err := RenderSRIOVDevicePlugin(config, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).NotTo(ContainElement(HaveKubernetesID("DaemonSet", "sriov-device-plugin", "sriov-device-plugin")))
	g.Expect(objs).NotTo(ContainElement(HaveKubernetesID("DaemonSet", "sriov-device-plugin", "sriov-cni")))

	// enable Multus; but no SRIOV-enabled additional network so we shouldn't
	// render the SRIOV Device Plugin yet
	enabled := false
	config.DisableMultiNetwork = &enabled
	objs, err = RenderSRIOVDevicePlugin(config, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).NotTo(ContainElement(HaveKubernetesID("DaemonSet", "sriov-device-plugin", "sriov-device-plugin")))
	g.Expect(objs).NotTo(ContainElement(HaveKubernetesID("DaemonSet", "sriov-device-plugin", "sriov-cni")))

	// Finally add an additional network for SRIOV
	config.AdditionalNetworks = append(config.AdditionalNetworks,
		netv1.AdditionalNetworkDefinition{
			Type: netv1.NetworkTypeRaw,
			Name: "sriov-test",
			RawCNIConfig: `{
				"name": "sriov-test",
				"type": "sriov"
			}`,
		})
	objs, err = RenderSRIOVDevicePlugin(config, manifestDir)

	// It's important that the Namespace is first
	g.Expect(len(objs)).To(Equal(3))
	g.Expect(objs[0]).To(HaveKubernetesID("Namespace", "", "sriov-device-plugin"))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "sriov-device-plugin", "sriov-device-plugin")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "sriov-device-plugin", "sriov-cni")))

	// Make sure every obj is reasonable:
	// - it is supported
	// - it reconciles to itself (steady state)
	for _, obj := range objs {
		g.Expect(apply.IsObjectSupported(obj)).NotTo(HaveOccurred())
		cur := obj.DeepCopy()
		upd := obj.DeepCopy()

		err = apply.MergeObjectForUpdate(cur, upd)
		g.Expect(err).NotTo(HaveOccurred())

		tweakMetaForCompare(cur)
		g.Expect(cur).To(Equal(upd))
	}
}
