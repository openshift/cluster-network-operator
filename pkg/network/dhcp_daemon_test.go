package network

import (
	. "github.com/onsi/gomega"
	operv1 "github.com/openshift/api/operator/v1"
	"testing"
)

var NoDHCPConfig = operv1.Network{
	Spec: operv1.NetworkSpec{
		AdditionalNetworks: []operv1.AdditionalNetworkDefinition{
			{Type: operv1.NetworkTypeRaw, Name: "net-attach-1", RawCNIConfig: "{}"},
			{Type: operv1.NetworkTypeRaw, Name: "net-attach-2", RawCNIConfig: "{}"},
		},
		ServiceNetwork: []string{"172.30.0.0/16"},
		ClusterNetwork: []operv1.ClusterNetworkEntry{
			{
				CIDR:       "10.128.0.0/15",
				HostPrefix: 23,
			},
		},
		DefaultNetwork: operv1.DefaultNetworkDefinition{
			Type: operv1.NetworkTypeOpenShiftSDN,
			OpenShiftSDNConfig: &operv1.OpenShiftSDNConfig{
				Mode: operv1.SDNModeNetworkPolicy,
			},
		},
	},
}

var DHCPConfig = operv1.Network{
	Spec: operv1.NetworkSpec{
		AdditionalNetworks: []operv1.AdditionalNetworkDefinition{
			{
				Type:         operv1.NetworkTypeRaw,
				Name:         "net-attach-dhcp",
				RawCNIConfig: "{\"cniVersion\":\"0.3.0\",\"type\":\"macvlan\",\"master\":\"eth0\",\"mode\":\"bridge\",\"ipam\":{\"type\":\"dhcp\"}}",
			},
		},
		ServiceNetwork: []string{"172.30.0.0/16"},
		ClusterNetwork: []operv1.ClusterNetworkEntry{
			{
				CIDR:       "10.128.0.0/15",
				HostPrefix: 23,
			},
		},
		DefaultNetwork: operv1.DefaultNetworkDefinition{
			Type: operv1.NetworkTypeOpenShiftSDN,
			OpenShiftSDNConfig: &operv1.OpenShiftSDNConfig{
				Mode: operv1.SDNModeNetworkPolicy,
			},
		},
	},
}

var InvalidDHCPConfig = operv1.Network{
	Spec: operv1.NetworkSpec{
		AdditionalNetworks: []operv1.AdditionalNetworkDefinition{
			{
				Type:         operv1.NetworkTypeRaw,
				Name:         "net-attach-dhcp",
				RawCNIConfig: "{\"cniVersion\":\"0.3.0\",\"type\":\"macvlan\",\"master\":\"eth0\",\"mode\":\"bridge\",\"ipam\":\"invalid\"}",
			},
		},
		ServiceNetwork: []string{"172.30.0.0/16"},
		ClusterNetwork: []operv1.ClusterNetworkEntry{
			{
				CIDR:       "10.128.0.0/15",
				HostPrefix: 23,
			},
		},
		DefaultNetwork: operv1.DefaultNetworkDefinition{
			Type: operv1.NetworkTypeOpenShiftSDN,
			OpenShiftSDNConfig: &operv1.OpenShiftSDNConfig{
				Mode: operv1.SDNModeNetworkPolicy,
			},
		},
	},
}

// TestRenderWithDHCP tests a rendering with the DHCP daemonset.
func TestRenderWithDHCP(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := DHCPConfig.DeepCopy()
	config := &crd.Spec
	FillDefaults(config, nil)

	objs, err := RenderMultus(config, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "dhcp-daemon")))
}

// TestRenderNoDHCP tests a rendering WITHOUT the DHCP daemonset.
func TestRenderNoDHCP(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := NoDHCPConfig.DeepCopy()
	config := &crd.Spec
	FillDefaults(config, nil)

	objs, err := RenderMultus(config, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).NotTo(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "dhcp-daemon")))
}

// TestRenderInvalidDHCP tests a rendering with the DHCP daemonset.
func TestRenderInvalidDHCP(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := InvalidDHCPConfig.DeepCopy()
	config := &crd.Spec
	FillDefaults(config, nil)

	objs, err := RenderMultus(config, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).NotTo(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "dhcp-daemon")))
}
