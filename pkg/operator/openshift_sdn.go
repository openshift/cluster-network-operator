package stub

import (
	"path/filepath"

	"github.com/pkg/errors"

	"github.com/openshift/openshift-network-operator/pkg/apis/networkoperator/v1"
	"github.com/openshift/openshift-network-operator/pkg/render"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func (h *Handler) renderOpenshiftSDN(c *v1.OpenshiftSDNConfig) ([]*uns.Unstructured, error) {
	// TODO: actually implement rendering openshift-sdn
	data := render.MakeRenderData()
	return render.RenderDir(filepath.Join(h.ManifestDir, "network/openshift-sdn"), &data)
}

func (h *Handler) validateOpenshiftSDN(c *v1.OpenshiftSDNConfig) []error {
	out := []error{}
	if sdnPluginName(c.Mode) == "" {
		out = append(out, errors.Errorf("invalid openshift-sdn mode %s", c.Mode))
	}

	if c.VXLANPort != nil && (*c.VXLANPort < 1 || *c.VXLANPort > 65535) {
		out = append(out, errors.Errorf("invalid VXLANPort %d", *c.VXLANPort))
	}

	if c.MTU != nil && (*c.MTU < 576 || *c.MTU > 65536) {
		out = append(out, errors.Errorf("invalid MTU %d", *c.MTU))
	}

	return out
}

func sdnPluginName(n v1.SDNMode) string {
	switch n {
	case v1.SDNModeSubnet:
		return "redhat/openshift-ovs-subnet"
	case v1.SDNModeMultitenant:
		return "redhat/openshift-ovs-multitenant"
	case v1.SDNModePolicy:
		return "redhat/openshift-ovs-networkpolicy"
	}
	return ""
}
