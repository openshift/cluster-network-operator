package network

import (
	"encoding/json"
	"net"
	"testing"

	. "github.com/onsi/gomega"
	operv1 "github.com/openshift/api/operator/v1"
)

var NetworkAttachmentConfigRaw = operv1.Network{
	Spec: operv1.NetworkSpec{
		AdditionalNetworks: []operv1.AdditionalNetworkDefinition{
			{Type: operv1.NetworkTypeRaw, Namespace: "foobar", Name: "net-attach-1", RawCNIConfig: "{}"},
			{Type: operv1.NetworkTypeRaw, Name: "net-attach-2", RawCNIConfig: "{}"},
		},
	},
}

var NetworkAttachmentConfigSimpleMacvlan = operv1.Network{
	Spec: operv1.NetworkSpec{
		AdditionalNetworks: []operv1.AdditionalNetworkDefinition{
			{
				Type:      operv1.NetworkTypeSimpleMacvlan,
				Name:      "net-attach-1",
				Namespace: "foobar",
				SimpleMacvlanConfig: &operv1.SimpleMacvlanConfig{
					IPAMConfig: &operv1.IPAMConfig{
						Type: operv1.IPAMTypeDHCP,
					},
					Master: "eth0",
					Mode:   operv1.MacvlanModeBridge,
				},
			},
		},
	},
}

var DHCPIPAMConfig = operv1.IPAMConfig{
	Type: operv1.IPAMTypeDHCP,
}

var StaticIPAMConfig = operv1.IPAMConfig{
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
}

func TestRenderAdditionalNetworksCRD(t *testing.T) {
	g := NewGomegaWithT(t)

	objs, err := renderAdditionalNetworksCRD(manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).To(HaveLen(2))
}

func TestRenderRawCNIConfig(t *testing.T) {
	g := NewGomegaWithT(t)

	for _, cfg := range NetworkAttachmentConfigRaw.Spec.AdditionalNetworks {
		objs, err := renderRawCNIConfig(&cfg, manifestDir)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(objs).To(HaveLen(1))

		expectedNamespace := cfg.Namespace
		if expectedNamespace == "" {
			expectedNamespace = "default"
		}
		g.Expect(objs).To(
			ContainElement(HaveKubernetesID(
				"NetworkAttachmentDefinition", expectedNamespace, cfg.Name)))
	}
}

func TestValidateRaw(t *testing.T) {
	g := NewGomegaWithT(t)

	for _, cfg := range NetworkAttachmentConfigRaw.Spec.AdditionalNetworks {
		err := validateRaw(&cfg)
		g.Expect(err).To(BeEmpty())
	}

	rawConfig := NetworkAttachmentConfigRaw.Spec.AdditionalNetworks[0]

	errExpect := func(substr string) {
		t.Helper()
		g.Expect(validateRaw(&rawConfig)).To(
			ContainElement(MatchError(
				ContainSubstring(substr))))
	}

	rawConfig.RawCNIConfig = "wrongCNIConfig"
	errExpect("Failed to Unmarshal RawCNIConfig")

	rawConfig.Name = ""
	errExpect("Additional Network Name cannot be nil")
}

func TestRenderSimpleMacvlanConfig(t *testing.T) {
	g := NewGomegaWithT(t)

	for _, cfg := range NetworkAttachmentConfigSimpleMacvlan.Spec.AdditionalNetworks {
		objs, err := renderSimpleMacvlanConfig(&cfg, manifestDir)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(objs).To(HaveLen(1))
		g.Expect(objs).To(
			ContainElement(HaveKubernetesID(
				"NetworkAttachmentDefinition", "foobar", cfg.Name)))
		expected := `
{
	"apiVersion": "k8s.cni.cncf.io/v1",
	"kind": "NetworkAttachmentDefinition",
	"metadata": {
		"namespace": "foobar",
		"name": "net-attach-1"
	},
	"spec": {
		"config": "{ \"cniVersion\": \"0.3.1\", \"type\": \"macvlan\",\n\"master\": \"eth0\",\n\"mode\": \"bridge\",\n\"ipam\":       { \"type\": \"dhcp\" } }"
	}
}`
		g.Expect(objs[0].MarshalJSON()).To(MatchJSON(expected))
	}
}

func TestValidateMacvlan(t *testing.T) {
	g := NewGomegaWithT(t)

	for _, cfg := range NetworkAttachmentConfigSimpleMacvlan.Spec.AdditionalNetworks {
		err := validateSimpleMacvlanConfig(&cfg)
		g.Expect(err).To(BeEmpty())
	}

	config := NetworkAttachmentConfigSimpleMacvlan.Spec.AdditionalNetworks[0]

	errExpect := func(substr string) {
		t.Helper()
		g.Expect(validateSimpleMacvlanConfig(&config)).To(
			ContainElement(MatchError(
				ContainSubstring(substr))))
	}

	config.Name = ""
	errExpect("Additional Network Name cannot be nil")

	config.SimpleMacvlanConfig.Mode = "invalidMacvlanMode"
	errExpect("invalid Macvlan mode: invalidMacvlanMode")

	config.SimpleMacvlanConfig.IPAMConfig.Type = "invalidIPAM"
	errExpect("invalid IPAM type: invalidIPAM")
}

func TestGetStaticIPAMConfigJSON(t *testing.T) {
	g := NewGomegaWithT(t)
	cfg, err := getIPAMConfigJSON(&StaticIPAMConfig)
	g.Expect(err).NotTo(HaveOccurred())
	obj := staticIPAMConfig{}
	err = json.Unmarshal([]byte(cfg), &obj)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(obj.Addresses).To(HaveLen(1))
	g.Expect(obj.Addresses[0].AddressStr).To(Equal("10.1.1.2/24"))
	g.Expect(obj.Addresses[0].Gateway).To(Equal(net.ParseIP("10.1.1.1")))
	g.Expect(obj.Routes).To(HaveLen(1))
	g.Expect(obj.Routes[0].Dst.String()).To(Equal("0.0.0.0/0"))
	g.Expect(obj.Routes[0].GW).To(Equal(net.ParseIP("10.1.1.1")))
	g.Expect(obj.DNS.Nameservers).To(HaveLen(1))
	g.Expect(obj.DNS.Nameservers[0]).To(Equal("10.1.1.1"))
	g.Expect(obj.DNS.Domain).To(Equal("macvlantest.example"))
	g.Expect(obj.DNS.Search).To(HaveLen(2))
	g.Expect(obj.DNS.Search[0]).To(Equal("testdomain1.example"))
	g.Expect(obj.DNS.Search[1]).To(Equal("testdomain2.example"))
}

func TestGetDHCPIPAMConfigJSON(t *testing.T) {
	g := NewGomegaWithT(t)
	cfg, err := getIPAMConfigJSON(&DHCPIPAMConfig)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(cfg).To(Equal(`{ "type": "dhcp" }`))
}

func TestValidateStaticIPAM(t *testing.T) {
	g := NewGomegaWithT(t)
	errs := validateIPAMConfig(&StaticIPAMConfig)
	g.Expect(errs).To(BeEmpty())

	confErr1 := StaticIPAMConfig

	confErr1.StaticIPAMConfig.Addresses[0].Address = "AAA"
	errs = validateIPAMConfig(&confErr1)
	g.Expect(errs).To(ContainElement(MatchError(
		ContainSubstring("invalid static address: invalid CIDR address: AAA"))))

	confErr1.StaticIPAMConfig.Addresses[0].Gateway = "BBB"
	errs = validateIPAMConfig(&confErr1)
	g.Expect(errs).To(ContainElement(MatchError(
		ContainSubstring("invalid gateway: BBB"))))

	confErr1.StaticIPAMConfig.Routes[0].Destination = "CCC"
	errs = validateIPAMConfig(&confErr1)
	g.Expect(errs).To(ContainElement(MatchError(
		ContainSubstring("invalid route destination: invalid CIDR address: CCC"))))

	confErr1.StaticIPAMConfig.Routes[0].Gateway = "DDD"
	errs = validateIPAMConfig(&confErr1)
	g.Expect(errs).To(ContainElement(MatchError(
		ContainSubstring("invalid gateway: DDD"))))
}
