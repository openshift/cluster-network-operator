package operator

import (
	"path/filepath"

	"github.com/pkg/errors"

	netv1 "github.com/openshift/api/network/v1"
	"github.com/operator-framework/operator-sdk/pkg/util/k8sutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

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
func (h *Handler) renderOpenshiftSDN(c *v1.OpenshiftSDNConfig) ([]*uns.Unstructured, error) {
	operConfig := h.config.Spec
	objs := []*uns.Unstructured{}

	// generate master network configuration
	ippools := []netv1.ClusterNetworkEntry{}
	for _, net := range operConfig.ClusterNetworks {
		ippools = append(ippools, netv1.ClusterNetworkEntry{CIDR: net.CIDR, HostSubnetLength: net.HostSubnetLength})
	}

	clusterNet := netv1.ClusterNetwork{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "network.openshift.io/v1",
			Kind:       "ClusterNetwork",
		},
		ObjectMeta: metav1.ObjectMeta{Name: netv1.ClusterNetworkDefault},

		ServiceNetwork:  operConfig.ServiceNetwork,
		PluginName:      sdnPluginName(c.Mode),
		ClusterNetworks: ippools,
		VXLANPort:       c.VXLANPort,

		Network:          ippools[0].CIDR,
		HostSubnetLength: ippools[0].HostSubnetLength,
	}
	obj, err := k8sutil.UnstructuredFromRuntimeObject(&clusterNet)
	if err != nil {
		// This is very unlikely
		return nil, errors.Wrap(err, "failed to transmutate ClusterNetwork")
	}
	objs = append(objs, obj)

	// render the manifests on disk
	data := render.MakeRenderData()
	data.Data["InstallOVS"] = (c.UseExternalOpenvswitch == nil || *c.UseExternalOpenvswitch == false)
	// TODO: figure out where the image locations come from
	data.Data["Image"] = "openshift/node:v3.10"

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
