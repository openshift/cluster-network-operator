package network

import (
	"testing"

	. "github.com/onsi/gomega"
	operv1 "github.com/openshift/api/operator/v1"
)

var NoIPAMConfig = operv1.Network{
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

var WhereaboutsConfig = operv1.Network{
	Spec: operv1.NetworkSpec{
		AdditionalNetworks: []operv1.AdditionalNetworkDefinition{
			{
				Type:         operv1.NetworkTypeRaw,
				Name:         "net-attach-whereabouts",
				RawCNIConfig: "{\"cniVersion\":\"0.3.0\",\"type\":\"macvlan\",\"master\":\"eth0\",\"mode\":\"bridge\",\"ipam\":{\"type\":\"whereabouts\",\"range\": \"192.168.2.225/28\"}}",
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

var InvalidIPAMConfig = operv1.Network{
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

var DHCPConfigSimpleMacvlan = operv1.Network{
	Spec: operv1.NetworkSpec{
		AdditionalNetworks: []operv1.AdditionalNetworkDefinition{
			{
				Type: operv1.NetworkTypeSimpleMacvlan,
				Name: "net-attach-1",
				SimpleMacvlanConfig: &operv1.SimpleMacvlanConfig{
					IPAMConfig: &operv1.IPAMConfig{
						Type: operv1.IPAMTypeDHCP,
					},
					Master: "eth0",
					Mode:   operv1.MacvlanModeBridge,
				},
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

var NoDHCPConfigSimpleMacvlan = operv1.Network{
	Spec: operv1.NetworkSpec{
		AdditionalNetworks: []operv1.AdditionalNetworkDefinition{
			{
				Type: operv1.NetworkTypeSimpleMacvlan,
				Name: "net-attach-1",
				SimpleMacvlanConfig: &operv1.SimpleMacvlanConfig{
					IPAMConfig: &operv1.IPAMConfig{
						Type: operv1.IPAMTypeStatic,
						StaticIPAMConfig: &operv1.StaticIPAMConfig{
							Addresses: []operv1.StaticIPAMAddresses{
								{
									Address: "10.1.1.2/24",
									Gateway: "10.1.1.1",
								},
							},
							Routes: []operv1.StaticIPAMRoutes{
								{
									Destination: "0.0.0.0/0",
									Gateway:     "10.1.1.1",
								},
							},
							DNS: &operv1.StaticIPAMDNS{
								Nameservers: []string{"10.1.1.1"},
								Domain:      "macvlantest.example",
								Search: []string{
									"testdomain1.example",
									"testdomain2.example",
								},
							},
						},
					},
					Master: "eth0",
					Mode:   operv1.MacvlanModeBridge,
				},
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
	fillDefaults(config, nil)

	objs, err := renderMultus(config, fakeBootstrapResult(), manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "dhcp-daemon")))
}

// TestRenderWithWhereabouts tests a rendering with the ip reconciler
func TestRenderWithWhereabouts(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := WhereaboutsConfig.DeepCopy()
	config := &crd.Spec
	fillDefaults(config, nil)

	objs, err := renderMultus(config, fakeBootstrapResult(), manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).To(ContainElement(HaveKubernetesID("CronJob", "openshift-multus", "ip-reconciler")))
}

// TestRenderNoIPAM tests a rendering WITHOUT an IPAM configured.
func TestRenderNoIPAM(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := NoIPAMConfig.DeepCopy()
	config := &crd.Spec
	fillDefaults(config, nil)

	objs, err := renderMultus(config, fakeBootstrapResult(), manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).NotTo(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "dhcp-daemon")))
	g.Expect(objs).NotTo(ContainElement(HaveKubernetesID("CronJob", "openshift-multus", "ip-reconciler")))
}

// TestRenderInvalidIPAMConfig tests a rendering without auxiliary IPAM, due to an invalid IPAM configuration.
func TestRenderInvalidIPAMConfig(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := InvalidIPAMConfig.DeepCopy()
	config := &crd.Spec
	fillDefaults(config, nil)

	objs, err := renderMultus(config, fakeBootstrapResult(), manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).NotTo(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "dhcp-daemon")))
	g.Expect(objs).NotTo(ContainElement(HaveKubernetesID("CronJob", "openshift-multus", "ip-reconciler")))
}

// TestRenderWithDHCPSimpleMacvlan tests a rendering with the DHCP daemonset SimpleMacvlan.
func TestRenderWithDHCPSimpleMacvlan(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := DHCPConfigSimpleMacvlan.DeepCopy()
	config := &crd.Spec
	fillDefaults(config, nil)

	objs, err := renderMultus(config, fakeBootstrapResult(), manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "dhcp-daemon")))
}

// TestRenderNoDHCPSimpleMacvlan tests a rendering WITHOUT the DHCP daemonset SimpleMacvlan.
func TestRenderNoDHCPSimpleMacvlan(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := NoDHCPConfigSimpleMacvlan.DeepCopy()
	config := &crd.Spec
	fillDefaults(config, nil)

	objs, err := renderMultus(config, fakeBootstrapResult(), manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).NotTo(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "dhcp-daemon")))
}
