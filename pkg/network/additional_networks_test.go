package network

import (
	"testing"

	. "github.com/onsi/gomega"
	operv1 "github.com/openshift/api/operator/v1"
)

var NetworkAttachmentConfig = operv1.Network{
	Spec: operv1.NetworkSpec{
		AdditionalNetworks: []operv1.AdditionalNetworkDefinition{
			{Type: operv1.NetworkTypeRaw, Name: "net-attach-1", RawCNIConfig: "{}"},
			{Type: operv1.NetworkTypeRaw, Name: "net-attach-2", RawCNIConfig: "{}"},
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

	for _, cfg := range NetworkAttachmentConfig.Spec.AdditionalNetworks {
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

	for _, cfg := range NetworkAttachmentConfig.Spec.AdditionalNetworks {
		err := validateRaw(&cfg)
		g.Expect(err).To(BeEmpty())
	}

	rawConfig := NetworkAttachmentConfig.Spec.AdditionalNetworks[0]

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
