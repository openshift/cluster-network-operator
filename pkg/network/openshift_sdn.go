package network

import (
	"log"
	"net"
	"os"
	"path/filepath"
	"reflect"

	"github.com/ghodss/yaml"
	"github.com/pkg/errors"

	"github.com/openshift/cluster-network-operator/pkg/bootstrap"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	configv1 "github.com/openshift/api/config/v1"
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
func renderOpenShiftSDN(conf *operv1.NetworkSpec, bootstrapResult *bootstrap.BootstrapResult, manifestDir string) ([]*uns.Unstructured, bool, error) {
	var progressing bool
	c := conf.DefaultNetwork.OpenShiftSDNConfig

	objs := []*uns.Unstructured{}

	data := render.MakeRenderData()
	data.Data["ReleaseVersion"] = os.Getenv("RELEASE_VERSION")
	data.Data["SDNImage"] = os.Getenv("SDN_IMAGE")
	data.Data["CNIPluginsImage"] = os.Getenv("CNI_PLUGINS_IMAGE")
	data.Data["KubeRBACProxyImage"] = os.Getenv("KUBE_RBAC_PROXY_IMAGE")
	data.Data["KUBERNETES_SERVICE_HOST"] = bootstrapResult.Infra.APIServers[bootstrap.APIServerDefault].Host
	data.Data["KUBERNETES_SERVICE_PORT"] = bootstrapResult.Infra.APIServers[bootstrap.APIServerDefault].Port
	data.Data["Mode"] = c.Mode
	data.Data["CNIConfDir"] = pluginCNIConfDir(conf)
	data.Data["CNIBinDir"] = CNIBinDir
	data.Data["PlatformType"] = bootstrapResult.Infra.PlatformType
	data.Data["HTTP_PROXY"] = bootstrapResult.Infra.Proxy.HTTPProxy
	data.Data["HTTPS_PROXY"] = bootstrapResult.Infra.Proxy.HTTPSProxy
	data.Data["NO_PROXY"] = bootstrapResult.Infra.Proxy.NoProxy
	if bootstrapResult.Infra.PlatformType == configv1.AzurePlatformType {
		data.Data["SDNPlatformAzure"] = true
	} else {
		data.Data["SDNPlatformAzure"] = false
	}
	data.Data["ExternalControlPlane"] = bootstrapResult.Infra.ExternalControlPlane
	data.Data["RoutableMTU"] = nil
	data.Data["MTU"] = nil

	if conf.Migration != nil && conf.Migration.MTU != nil {
		if *conf.Migration.MTU.Network.From > *conf.Migration.MTU.Network.To {
			data.Data["MTU"] = conf.Migration.MTU.Network.From
			data.Data["RoutableMTU"] = conf.Migration.MTU.Network.To
		} else {
			data.Data["MTU"] = conf.Migration.MTU.Network.To
			data.Data["RoutableMTU"] = conf.Migration.MTU.Network.From
		}

		// c.MTU is used to set the applied network configuration MTU
		// MTU migration procedure:
		//  1. User sets the MTU they want to migrate to
		//  2. CNO sets the MTU as applied
		//  3. User can then set the MTU as configured
		c.MTU = conf.Migration.MTU.Network.To
	}

	clusterNetwork, err := clusterNetwork(conf)
	if err != nil {
		return nil, progressing, errors.Wrap(err, "failed to build ClusterNetwork")
	}
	data.Data["ClusterNetwork"] = clusterNetwork

	kpcDefaults := map[string]operv1.ProxyArgumentList{
		"metrics-bind-address":    {"127.0.0.1"},
		"healthz-port":            {"10256"},
		"proxy-mode":              {"iptables"},
		"iptables-masquerade-bit": {"0"},
		"enable-profiling":        {"true"},
	}
	// For backward compatibility we allow conf to specify `metrics-port: 9101` but
	// the daemonset always configures 9101 as the secure metrics port and 29101 as
	// the insecure metrics port exposed by kube-proxy itself. So just override
	// the value from conf (which we know is either "9101" or unspecified).
	kpcOverrides := map[string]operv1.ProxyArgumentList{
		"metrics-port": {"29101"},
	}
	if *c.EnableUnidling {
		// We already validated that proxy-mode was either unset or iptables.
		kpcOverrides["proxy-mode"] = operv1.ProxyArgumentList{"unidling+iptables"}
	} else if *conf.DeployKubeProxy {
		kpcOverrides["proxy-mode"] = operv1.ProxyArgumentList{"disabled"}
	}

	kpc, err := kubeProxyConfiguration(kpcDefaults, conf, kpcOverrides)
	if err != nil {
		return nil, progressing, errors.Wrap(err, "failed to build kube-proxy config")
	}
	data.Data["KubeProxyConfig"] = kpc

	manifests, err := render.RenderDir(filepath.Join(manifestDir, "network/openshift-sdn"), &data)
	if err != nil {
		return nil, progressing, errors.Wrap(err, "failed to render manifests")
	}

	objs = append(objs, manifests...)

	return objs, progressing, nil
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

		if sc.MTU != nil && (*sc.MTU < MinMTUIPv4 || *sc.MTU > MaxMTU) {
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

	if conf.DeployKubeProxy != nil && *conf.DeployKubeProxy {
		// We allow deploying an external kube-proxy with openshift-sdn in very
		// limited circumstances, for testing purposes. The error here
		// intentionally lies about this.
		if sc == nil || sc.EnableUnidling == nil || *sc.EnableUnidling || !noKubeProxyConfig(conf) {
			out = append(out, errors.Errorf("openshift-sdn does not support 'deployKubeProxy: true'"))
		}
	}

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

	if reflect.DeepEqual(pn, nn) && reflect.DeepEqual(prev.Migration, next.Migration) {
		return errs
	}

	if pn.Mode != nn.Mode {
		errs = append(errs, errors.Errorf("cannot change openshift-sdn mode"))
	}

	// deepequal is nil-safe
	if !reflect.DeepEqual(pn.VXLANPort, nn.VXLANPort) {
		errs = append(errs, errors.Errorf("cannot change openshift-sdn vxlanPort"))
	}

	if next.Migration != nil && next.Migration.MTU != nil {
		mtuNet := next.Migration.MTU.Network
		mtuMach := next.Migration.MTU.Machine

		// For MTU values provided for migration, verify that:
		//  - The current and target MTUs for the CNI are provided
		//  - The machine target MTU is provided
		//  - The current MTU actually matches the MTU known as current
		//  - The machine target MTU has a valid overhead with the CNI target MTU
		sdnOverhead := uint32(50) // 50 byte VXLAN header
		if mtuNet == nil || mtuMach == nil || mtuNet.From == nil || mtuNet.To == nil || mtuMach.To == nil {
			errs = append(errs, errors.Errorf("invalid Migration.MTU, at least one of the required fields is missing"))
		} else {
			// Only check next.Migration.MTU.Network.From when it changes
			checkPrevMTU := prev.Migration == nil || prev.Migration.MTU == nil || prev.Migration.MTU.Network == nil || !reflect.DeepEqual(prev.Migration.MTU.Network.From, next.Migration.MTU.Network.From)
			if checkPrevMTU && !reflect.DeepEqual(next.Migration.MTU.Network.From, pn.MTU) {
				errs = append(errs, errors.Errorf("invalid Migration.MTU.Network.From(%d) not equal to the currently applied MTU(%d)", *next.Migration.MTU.Network.From, *pn.MTU))
			}

			if *next.Migration.MTU.Network.To < MinMTUIPv4 || *next.Migration.MTU.Network.To > MaxMTU {
				errs = append(errs, errors.Errorf("invalid Migration.MTU.Network.To(%d), has to be in range: %d-%d", *next.Migration.MTU.Network.To, MinMTUIPv4, MaxMTU))
			}
			if *next.Migration.MTU.Machine.To < MinMTUIPv4 || *next.Migration.MTU.Machine.To > MaxMTU {
				errs = append(errs, errors.Errorf("invalid Migration.MTU.Machine.To(%d), has to be in range: %d-%d", *next.Migration.MTU.Machine.To, MinMTUIPv4, MaxMTU))
			}
			if (*next.Migration.MTU.Network.To + sdnOverhead) > *next.Migration.MTU.Machine.To {
				errs = append(errs, errors.Errorf("invalid Migration.MTU.Machine.To(%d), has to be at least %d", *next.Migration.MTU.Machine.To, *next.Migration.MTU.Network.To+sdnOverhead))
			}
		}
	} else if !reflect.DeepEqual(pn.MTU, nn.MTU) {
		errs = append(errs, errors.Errorf("cannot change openshift-sdn mtu without migration"))
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
	// If it's not supplied, we infer it by probing a node's interface via the mtu-prober job.
	// However, this can never change, so we always prefer previous.
	if sc.MTU == nil {
		var mtu uint32
		if previous != nil &&
			previous.DefaultNetwork.Type == operv1.NetworkTypeOpenShiftSDN &&
			previous.DefaultNetwork.OpenShiftSDNConfig != nil &&
			previous.DefaultNetwork.OpenShiftSDNConfig.MTU != nil {
			mtu = *previous.DefaultNetwork.OpenShiftSDNConfig.MTU
		} else {
			// utter paranoia
			// somehow we didn't probe the MTU in the controller, but we need it.
			// This might be wrong in cases where the CNO is not local (e.g. Hypershift).
			if hostMTU == 0 {
				log.Printf("BUG: Probed MTU wasn't supplied, but was needed. Falling back to host MTU")
				hostMTU, _ = GetDefaultMTU()
				if hostMTU == 0 { // this is beyond unlikely.
					panic("BUG: Probed MTU wasn't supplied, host MTU invalid")
				}
			}
			mtu = uint32(hostMTU) - 50 // 50 byte VXLAN header
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
