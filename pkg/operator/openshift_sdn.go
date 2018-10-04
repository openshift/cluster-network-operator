package operator

import (
	"os"
	"path/filepath"

	"github.com/ghodss/yaml"
	"github.com/pkg/errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	configv1 "github.com/openshift/api/config/v1"
	cpv1 "github.com/openshift/api/openshiftcontrolplane/v1"
	"github.com/openshift/cluster-network-operator/pkg/apis/networkoperator/v1"
	"github.com/openshift/cluster-network-operator/pkg/render"
)

// renderOpenshiftSDN returns the manifests for the openshift-sdn.
// This creates
// - the ClusterNetwork object
// - the sdn namespace
// - the sdn daemonset
// - the openvswitch daemonset
// and some other small things.
func (h *Handler) renderOpenshiftSDN() ([]*uns.Unstructured, error) {
	operConfig := h.config.Spec
	c := operConfig.DefaultNetwork.OpenshiftSDNConfig

	objs := []*uns.Unstructured{}

	// render the manifests on disk
	data := render.MakeRenderData()
	data.Data["InstallOVS"] = (c.UseExternalOpenvswitch == nil || *c.UseExternalOpenvswitch == false)
	data.Data["NodeImage"] = os.Getenv("NODE_IMAGE")
	data.Data["HypershiftImage"] = os.Getenv("HYPERSHIFT_IMAGE")
	mtu := uint32(1450)
	if c.MTU != nil {
		mtu = *c.MTU
	}
	data.Data["MTU"] = mtu
	data.Data["PluginName"] = sdnPluginName(c.Mode)

	operCfg, err := h.controllerConfig()
	if err != nil {
		return nil, errors.Wrap(err, "failed to build controller config")
	}
	data.Data["NetworkControllerConfig"] = operCfg

	manifests, err := render.RenderDir(filepath.Join(h.ManifestDir, "network/openshift-sdn"), &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render manifests")
	}

	objs = append(objs, manifests...)
	return objs, nil
}

// validateOpenshiftSDN checks that the openshift-sdn specific configuration
// is basically sane.
func (h *Handler) validateOpenshiftSDN() []error {
	out := []error{}
	c := h.config.Spec
	sc := c.DefaultNetwork.OpenshiftSDNConfig
	if sc == nil {
		out = append(out, errors.Errorf("OpenshiftSDNConfig cannot be nil"))
		return out
	}

	if len(c.ClusterNetworks) == 0 {
		out = append(out, errors.Errorf("ClusterNetworks cannot be empty"))
	}

	if sdnPluginName(sc.Mode) == "" {
		out = append(out, errors.Errorf("invalid openshift-sdn mode %q", sc.Mode))
	}

	if sc.VXLANPort != nil && (*sc.VXLANPort < 1 || *sc.VXLANPort > 65535) {
		out = append(out, errors.Errorf("invalid VXLANPort %d", *sc.VXLANPort))
	}

	if sc.MTU != nil && (*sc.MTU < 576 || *sc.MTU > 65536) {
		out = append(out, errors.Errorf("invalid MTU %d", *sc.MTU))
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

// controllerConfig builds the contents of controller-config.yaml
// for the controller
func (h *Handler) controllerConfig() (string, error) {
	c := h.config.Spec.DefaultNetwork.OpenshiftSDNConfig

	// generate master network configuration
	ippools := []cpv1.ClusterNetworkEntry{}
	for _, net := range h.config.Spec.ClusterNetworks {
		ippools = append(ippools, cpv1.ClusterNetworkEntry{CIDR: net.CIDR, HostSubnetLength: net.HostSubnetLength})
	}

	var vxlanPort uint32 = 4789
	if c.VXLANPort != nil {
		vxlanPort = *c.VXLANPort
	}

	cfg := cpv1.OpenShiftControllerManagerConfig{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "openshiftcontrolplane.config.openshift.io/v1",
			Kind:       "OpenShiftControllerManagerConfig",
		},
		// no ObjectMeta - not an API object

		Controllers: []string{"openshift.io/sdn"},
		Network: cpv1.NetworkControllerConfig{
			NetworkPluginName:  sdnPluginName(c.Mode),
			ClusterNetworks:    ippools,
			ServiceNetworkCIDR: h.config.Spec.ServiceNetwork,
			VXLANPort:          vxlanPort,
		},

		LeaderElection: configv1.LeaderElection{
			Namespace: "openshift-sdn",
			Name:      "openshift-sdn-controller",
		},
	}

	buf, err := yaml.Marshal(cfg)
	return string(buf), err
}
