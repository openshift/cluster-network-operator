package network

import (
	"testing"

	. "github.com/onsi/gomega"
	operv1 "github.com/openshift/api/operator/v1"
)

var NetworkAttachmentConfigRaw = netv1.NetworkConfig{
	Spec: netv1.NetworkConfigSpec{
		AdditionalNetworks: []netv1.AdditionalNetworkDefinition{
			{Type: netv1.NetworkTypeRaw, Name: "net-attach-1", RawCNIConfig: "{}"},
			{Type: netv1.NetworkTypeRaw, Name: "net-attach-2", RawCNIConfig: "{}"},
		},
	},
}

var NetworkAttachmentConfigMacVlan = netv1.NetworkConfig{
	Spec: netv1.NetworkConfigSpec{
		AdditionalNetworks: []netv1.AdditionalNetworkDefinition{
			{
				Type: netv1.NetworkTypeMacVlan,
				Name: "net-attach-1",
				MacVlanConfig: &netv1.MacVlanConfig{
					Master: "eth0",
					Ipam: "dhcp",
				},
			},

		},
	},
}

func TestRenderAdditionalNetworksCRD(t *testing.T) {
	g := NewGomegaWithT(t)

	objs, err := renderAdditionalNetworksCRD(manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).To(HaveLen(1))
}

func TestRenderRawCNIConfig(t *testing.T) {
	g := NewGomegaWithT(t)

	for _, cfg := range NetworkAttachmentConfigRaw.Spec.AdditionalNetworks {
		objs, err := renderRawCNIConfig(&cfg, manifestDir)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(objs).To(HaveLen(1))
		g.Expect(objs).To(
			ContainElement(HaveKubernetesID(
				"NetworkAttachmentDefinition", "default", cfg.Name)))
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

func TestRenderMacVlanConfig(t *testing.T) {
	g := NewGomegaWithT(t)

	for _, cfg := range NetworkAttachmentConfigMacVlan.Spec.AdditionalNetworks {
		objs, err := renderMacVlanConfig(&cfg, manifestDir)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(objs).To(HaveLen(1))
		g.Expect(objs).To(
			ContainElement(HaveKubernetesID(
				"NetworkAttachmentDefinition", "default", cfg.Name)))
	}
}

func TestValidateMacVlan(t *testing.T) {
	g := NewGomegaWithT(t)

	for _, cfg := range NetworkAttachmentConfigMacVlan.Spec.AdditionalNetworks {
		err := validateMacVlanConfig(&cfg)
		g.Expect(err).To(BeEmpty())
	}

	rawConfig := NetworkAttachmentConfigMacVlan.Spec.AdditionalNetworks[0]

	errExpect := func(substr string) {
		t.Helper()
		g.Expect(validateMacVlanConfig(&rawConfig)).To(
			ContainElement(MatchError(
				ContainSubstring(substr))))
	}

	rawConfig.MacVlanConfig = &netv1.MacVlanConfig{ Master: "", }
	errExpect("macVlan master cannot be nil")

	rawConfig.MacVlanConfig = &netv1.MacVlanConfig{ Ipam: "", }
	errExpect("macVlan ipam cannot be nil")

	rawConfig.Name = ""
	errExpect("Additional Network Name cannot be nil")
}

