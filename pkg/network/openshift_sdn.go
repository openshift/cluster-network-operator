package network

import (
	"net"
	"os"
	"path/filepath"
	"reflect"

	"github.com/ghodss/yaml"
	"github.com/pkg/errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	netv1 "github.com/openshift/api/network/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/render"
)

// renderOpenShiftSDN returns the manifests for the openshift-sdn.
// This creates
// - the ClusterNetwork object
// - the sdn namespace
// - the sdn daemonset
// - the openvswitch daemonset
// and some other small things.
func renderOpenShiftSDN(conf *operv1.NetworkSpec, manifestDir string) ([]*uns.Unstructured, error) {
	c := conf.DefaultNetwork.OpenShiftSDNConfig

	objs := []*uns.Unstructured{}

	data := render.MakeRenderData()
	data.Data["ReleaseVersion"] = os.Getenv("RELEASE_VERSION")
	data.Data["InstallOVS"] = (c.UseExternalOpenvswitch == nil || *c.UseExternalOpenvswitch == false)
	data.Data["SDNImage"] = os.Getenv("SDN_IMAGE")
	data.Data["CNIPluginsImage"] = os.Getenv("CNI_PLUGINS_IMAGE")
	data.Data["KUBERNETES_SERVICE_HOST"] = os.Getenv("KUBERNETES_SERVICE_HOST")
	data.Data["KUBERNETES_SERVICE_PORT"] = os.Getenv("KUBERNETES_SERVICE_PORT")
	data.Data["Mode"] = c.Mode
	data.Data["CNIConfDir"] = pluginCNIConfDir(conf)
	data.Data["CNIBinDir"] = CNIBinDir

	clusterNetwork, err := clusterNetwork(conf)
	if err != nil {
		return nil, errors.Wrap(err, "failed to build ClusterNetwork")
	}
	data.Data["ClusterNetwork"] = clusterNetwork

	kpcDefaults := map[string]operv1.ProxyArgumentList{
		"metrics-bind-address":    {"0.0.0.0"},
		"metrics-port":            {"9101"},
		"healthz-port":            {"10256"},
		"proxy-mode":              {"iptables"},
		"iptables-masquerade-bit": {"0"},
	}

	kpcOverrides := map[string]operv1.ProxyArgumentList{}
	if *c.EnableUnidling {
		// We already validated that proxy-mode was either unset or iptables.
		kpcOverrides["proxy-mode"] = operv1.ProxyArgumentList{"unidling+iptables"}
	}

	kpc, err := kubeProxyConfiguration(kpcDefaults, conf, kpcOverrides)
	if err != nil {
		return nil, errors.Wrap(err, "failed to build kube-proxy config")
	}
	data.Data["KubeProxyConfig"] = kpc

	manifests, err := render.RenderDir(filepath.Join(manifestDir, "network/openshift-sdn"), &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render manifests")
	}

	objs = append(objs, manifests...)
	return objs, nil
}

// validateOpenShiftSDN checks that the openshift-sdn specific configuration
// is basically sane.
func validateOpenShiftSDN(conf *operv1.NetworkSpec) []error {
	out := []error{}

	if len(conf.ClusterNetwork) == 0 {
		out = append(out, errors.Errorf("ClusterNetwork cannot be empty"))
	}

	if len(conf.ServiceNetwork) != 1 {
		out = append(out, errors.Errorf("ServiceNetwork must have exactly 1 entry"))
	}

	sc := conf.DefaultNetwork.OpenShiftSDNConfig
	if sc != nil {
		if sc.Mode != "" && sdnPluginName(sc.Mode) == "" {
			out = append(out, errors.Errorf("invalid openshift-sdn mode %q", sc.Mode))
		}

		if sc.VXLANPort != nil && (*sc.VXLANPort < 1 || *sc.VXLANPort > 65535) {
			out = append(out, errors.Errorf("invalid VXLANPort %d", *sc.VXLANPort))
		}

		if sc.MTU != nil && (*sc.MTU < 576 || *sc.MTU > 65536) {
			out = append(out, errors.Errorf("invalid MTU %d", *sc.MTU))
		}

		// the proxy mode must be unset or iptables for unidling to work
		if (sc.EnableUnidling == nil || *sc.EnableUnidling) &&
			conf.KubeProxyConfig != nil && conf.KubeProxyConfig.ProxyArguments != nil &&
			len(conf.KubeProxyConfig.ProxyArguments["proxy-mode"]) > 0 &&
			conf.KubeProxyConfig.ProxyArguments["proxy-mode"][0] != "iptables" {

			out = append(out, errors.Errorf("invalid proxy-mode - when unidling is enabled, proxy-mode must be \"iptables\""))
		}
	}

	proxyErrs := validateKubeProxy(conf)
	out = append(out, proxyErrs...)

	return out
}

// isOpenShiftSDNChangeSafe ensures no unsafe changes are applied to the running
// network
// It allows changing only useExternalOpenvswitch and enableUnidling.
// In the future, we may support rolling out MTU or external openvswitch alterations.
// as with all is*ChangeSafe functions, defaults have already been applied.
func isOpenShiftSDNChangeSafe(prev, next *operv1.NetworkSpec) []error {
	pn := prev.DefaultNetwork.OpenShiftSDNConfig
	nn := next.DefaultNetwork.OpenShiftSDNConfig
	errs := []error{}

	if reflect.DeepEqual(pn, nn) {
		return errs
	}

	if pn.Mode != nn.Mode {
		errs = append(errs, errors.Errorf("cannot change openshift-sdn mode"))
	}

	// deepequal is nil-safe
	if !reflect.DeepEqual(pn.VXLANPort, nn.VXLANPort) {
		errs = append(errs, errors.Errorf("cannot change openshift-sdn vxlanPort"))
	}

	if !reflect.DeepEqual(pn.MTU, nn.MTU) {
		errs = append(errs, errors.Errorf("cannot change openshift-sdn mtu"))
	}

	// It is allowed to change useExternalOpenvswitch and enableUnidling
	return errs
}

func fillOpenShiftSDNDefaults(conf, previous *operv1.NetworkSpec, hostMTU int) {
	// NOTE: If you change any defaults, and it's not a safe change to roll out
	// to existing clusters, you MUST use the value from previous instead.
	if conf.DeployKubeProxy == nil {
		prox := false
		conf.DeployKubeProxy = &prox
	}

	if conf.KubeProxyConfig == nil {
		conf.KubeProxyConfig = &operv1.ProxyConfig{}
	}
	if conf.KubeProxyConfig.BindAddress == "" {
		conf.KubeProxyConfig.BindAddress = "0.0.0.0"
	}

	if conf.KubeProxyConfig.ProxyArguments == nil {
		conf.KubeProxyConfig.ProxyArguments = map[string]operv1.ProxyArgumentList{}
	}

	if conf.DefaultNetwork.OpenShiftSDNConfig == nil {
		conf.DefaultNetwork.OpenShiftSDNConfig = &operv1.OpenShiftSDNConfig{}
	}
	sc := conf.DefaultNetwork.OpenShiftSDNConfig

	if sc.VXLANPort == nil {
		var port uint32 = 4789
		sc.VXLANPort = &port
	}

	if sc.EnableUnidling == nil {
		truth := true
		sc.EnableUnidling = &truth
	}

	// MTU is currently the only field we pull from previous.
	// If it's not supplied, we infer it from  the node on which we're running.
	// However, this can never change, so we always prefer previous.
	if sc.MTU == nil {
		var mtu uint32 = uint32(hostMTU) - 50 // 50 byte VXLAN header
		if previous != nil &&
			previous.DefaultNetwork.Type == operv1.NetworkTypeOpenShiftSDN &&
			previous.DefaultNetwork.OpenShiftSDNConfig != nil &&
			previous.DefaultNetwork.OpenShiftSDNConfig.MTU != nil {
			mtu = *previous.DefaultNetwork.OpenShiftSDNConfig.MTU
		}
		sc.MTU = &mtu
	}
	if sc.Mode == "" {
		sc.Mode = operv1.SDNModeNetworkPolicy
	}
}

func sdnPluginName(n operv1.SDNMode) string {
	switch n {
	case operv1.SDNModeSubnet:
		return "redhat/openshift-ovs-subnet"
	case operv1.SDNModeMultitenant:
		return "redhat/openshift-ovs-multitenant"
	case operv1.SDNModeNetworkPolicy:
		return "redhat/openshift-ovs-networkpolicy"
	}
	return ""
}

// clusterNetwork builds the ClusterNetwork used by both the controller and the node
func clusterNetwork(conf *operv1.NetworkSpec) (string, error) {
	c := conf.DefaultNetwork.OpenShiftSDNConfig

	networks := []netv1.ClusterNetworkEntry{}
	for _, entry := range conf.ClusterNetwork {
		_, cidr, err := net.ParseCIDR(entry.CIDR) // already validated
		if err != nil {
			return "", err
		}
		_, size := cidr.Mask.Size()
		hostSubnetLength := uint32(size) - entry.HostPrefix

		networks = append(networks, netv1.ClusterNetworkEntry{CIDR: entry.CIDR, HostSubnetLength: hostSubnetLength})
	}

	cn := netv1.ClusterNetwork{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "network.openshift.io/v1",
			Kind:       "ClusterNetwork",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: netv1.ClusterNetworkDefault,
		},

		PluginName:       sdnPluginName(c.Mode),
		Network:          networks[0].CIDR,
		HostSubnetLength: networks[0].HostSubnetLength,
		ClusterNetworks:  networks,
		ServiceNetwork:   conf.ServiceNetwork[0],
		VXLANPort:        c.VXLANPort,
		MTU:              c.MTU,
	}
	cnBuf, err := yaml.Marshal(cn)
	if err != nil {
		return "", err
	}

	return string(cnBuf), nil
}
