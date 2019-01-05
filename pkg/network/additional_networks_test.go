package network

import (
	"encoding/json"
	"os"
	"testing"

	netv1 "github.com/openshift/cluster-network-operator/pkg/apis/networkoperator/v1"

	. "github.com/onsi/gomega"
)

var NetworkAttachmentConfig = netv1.NetworkConfig{
	Spec: netv1.NetworkConfigSpec{
		DefaultNetwork: netv1.DefaultNetworkDefinition{
			Type: netv1.NetworkTypeOpenShiftSDN,
		},
		AdditionalNetworks: []netv1.AdditionalNetworkDefinition{
			{Type: netv1.NetworkTypeRaw, Name: "net-attach1", Namespace: "multus", RawCNIConfig: "{}"},
			{Type: netv1.NetworkTypeRaw, Name: "net-attach2", Namespace: "multus", RawCNIConfig: "{}"},
			{
				Type:         netv1.NetworkTypeRaw,
				Name:         "net-attach3",
				Namespace:    "multus",
				RawCNIConfig: "{}",
				Annotations:  map[string]string{"k8s.v1.cni.cncf.io/resourceName": "com/sriov"},
			},
		},
	},
}

func TestRenderMultusConfig(t *testing.T) {
	g := NewGomegaWithT(t)

	objs, err := renderMultusConfig(&NetworkAttachmentConfig.Spec, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).To(HaveLen(11))
}

func TestRenderRawCNIConfig(t *testing.T) {
	g := NewGomegaWithT(t)

	for _, cfg := range NetworkAttachmentConfig.Spec.AdditionalNetworks {
		objs, err := renderRawCNIConfig(&cfg, manifestDir)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(objs).To(HaveLen(1))
		g.Expect(objs).To(
			ContainElement(HaveKubernetesID(
				"NetworkAttachmentDefinition", "multus", cfg.Name)))
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

func TestMultusNodeConfig(t *testing.T) {
	g := NewGomegaWithT(t)

	os.Setenv("HOST_KUBECONFIG", "/etc/kubernetes/kubeconfig")
	cniConfig := &netv1.MultusCNIConfig{}
	cfg, err := multusNodeConfig(&NetworkAttachmentConfig.Spec)
	g.Expect(err).NotTo(HaveOccurred())

	err = json.Unmarshal([]byte(cfg), cniConfig)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(cniConfig.Name).To(Equal("multus-cni-network"))
	g.Expect(cniConfig.Type).To(Equal("multus"))
	g.Expect(cniConfig.Delegates[0].Name).To(Equal("openshift-sdn"))
	g.Expect(cniConfig.Delegates[0].Type).To(Equal("openshift-sdn"))
	g.Expect(cniConfig.Kubeconfig).To(Equal("/etc/kubernetes/kubeconfig"))
}
