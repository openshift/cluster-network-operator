package network

import (
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

var NetworkAttachmentConfigSRIOV = operv1.Network{
	Spec: operv1.NetworkSpec{
		AdditionalNetworks: []operv1.AdditionalNetworkDefinition{
			{Type: operv1.NetworkTypeRaw, Name: "sriov-network", RawCNIConfig: `{"name":"sriov-network", "type": "sriov"}`},
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
	for _, cfg := range NetworkAttachmentConfigSRIOV.Spec.AdditionalNetworks {
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

func TestRenderOpenShiftSRIOV(t *testing.T) {
	g := NewGomegaWithT(t)

	for _, cfg := range NetworkAttachmentConfigRaw.Spec.AdditionalNetworks {
		if isOpenShiftSRIOV(&cfg) {
			t.Fatalf("config %s is OpenShiftSRIOV?", cfg.Name)
		}
	}

	for _, cfg := range NetworkAttachmentConfigSRIOV.Spec.AdditionalNetworks {
		if !isOpenShiftSRIOV(&cfg) {
			t.Fatalf("config %s is not OpenShiftSRIOV", cfg.Name)
		}
		objs, err := renderOpenShiftSRIOV(&cfg, manifestDir)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(objs).To(HaveLen(6))
		g.Expect(objs).To(
			ContainElement(HaveKubernetesID(
				"NetworkAttachmentDefinition", "default", cfg.Name)))
	}
}
