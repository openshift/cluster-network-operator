package stub

import (
	"testing"

	"github.com/openshift/openshift-network-operator/pkg/apis/networkoperator/v1"

	. "github.com/onsi/gomega"
)

var OpenshiftSDNConfig = v1.NetworkConfig{
	Spec: v1.NetworkConfigSpec{
		DefaultNetwork: v1.DefaultNetworkDefinition{
			Type: v1.NetworkTypeOpenshiftSDN,
			OpenshiftSDNConfig: &v1.OpenshiftSDNConfig{
				Mode: v1.SDNModePolicy,
			},
		},
	},
}

var manifestDir = "../../manifests"

func TestRenderOpenshiftSDN(t *testing.T) {
	g := NewGomegaWithT(t)

	h := Handler{
		config:      &OpenshiftSDNConfig,
		ManifestDir: manifestDir,
	}

	errs := h.validateOpenshiftSDN(OpenshiftSDNConfig.Spec.DefaultNetwork.OpenshiftSDNConfig)
	g.Expect(errs).To(HaveLen(0))

	objs, err := h.renderOpenshiftSDN(OpenshiftSDNConfig.Spec.DefaultNetwork.OpenshiftSDNConfig)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(objs).To(HaveLen(1))
}
