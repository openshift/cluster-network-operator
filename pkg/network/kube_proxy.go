package network

import (
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/pkg/errors"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/render"
	k8sutil "github.com/openshift/cluster-network-operator/pkg/util/k8s"
)

// shouldDeployKubeProxy determines if the desired network type should
// install kube-proxy.
// openshift-sdn deploys its own kube-proxy. ovn-kubernetes and
// Kuryr-Kubernetes handle services on their own. All other
// network providers are assumed to require kube-proxy
func shouldDeployKubeProxy(conf *operv1.NetworkSpec) bool {
	switch conf.DefaultNetwork.Type {
	case operv1.NetworkTypeOpenShiftSDN:
		return false
	case operv1.NetworkTypeOVNKubernetes:
		return false
	case operv1.NetworkTypeKuryr:
		return false
	default:
		return true
	}
}

// kubeProxyConfiguration builds the (yaml text of) the kube-proxy config object
// It merges multiple sources of arguments. The precedence order is:
// - pluginDefaults
// - conf.KubeProxyConfig.ProxyArguments
// - pluginOverrides
func kubeProxyConfiguration(pluginDefaults map[string]operv1.ProxyArgumentList, conf *operv1.NetworkSpec, pluginOverrides map[string]operv1.ProxyArgumentList) (string, error) {
	p := conf.KubeProxyConfig

	args := map[string]operv1.ProxyArgumentList{}
	args["bind-address"] = []string{p.BindAddress}
	if len(conf.ClusterNetwork) == 1 {
		args["cluster-cidr"] = []string{conf.ClusterNetwork[0].CIDR}
	}
	args["iptables-sync-period"] = []string{p.IptablesSyncPeriod}

	args = k8sutil.MergeKubeProxyArguments(args, pluginDefaults)
	args = k8sutil.MergeKubeProxyArguments(args, p.ProxyArguments)
	args = k8sutil.MergeKubeProxyArguments(args, pluginOverrides)

	return k8sutil.GenerateKubeProxyConfiguration(args)
}

// validateStandaloneKubeProxy validates the kube-proxy configuration if
// installation is requested.
func validateStandaloneKubeProxy(conf *operv1.NetworkSpec) []error {
	if shouldDeployKubeProxy(conf) {
		return validateKubeProxy(conf)
	}
	return nil
}

// validateKubeProxy checks that the kube-proxy specific configuration
// is basically sane.
// This is called either if DeployKubeProxy is true *or* by openshift-sdn
func validateKubeProxy(conf *operv1.NetworkSpec) []error {
	out := []error{}
	p := conf.KubeProxyConfig
	if p == nil {
		return out
	}

	if p.IptablesSyncPeriod != "" {
		_, err := time.ParseDuration(p.IptablesSyncPeriod)
		if err != nil {
			out = append(out, errors.Errorf("IptablesSyncPeriod is not a valid duration (%v)", err))
		}
	}

	if p.BindAddress != "" {
		if net.ParseIP(p.BindAddress) == nil {
			out = append(out, errors.Errorf("BindAddress must be a valid IP address"))
		}
	}

	// Don't allow ports to be overridden
	if p.ProxyArguments != nil {
		if val, ok := p.ProxyArguments["metrics-port"]; ok {
			if !(len(val) == 1 && val[0] == "9101") {
				out = append(out, errors.Errorf("kube-proxy --metrics-port must be 9101"))
			}
		}

		if val, ok := p.ProxyArguments["healthz-port"]; ok {
			if !(len(val) == 1 && val[0] == "10256") {
				out = append(out, errors.Errorf("kube-proxy --healthz-port must be 10256"))
			}
		}
	}

	return out
}

// fillKubeProxyDefaults inserts kube-proxy defaults, but only if
// kube-proxy will be deployed explicitly.
func fillKubeProxyDefaults(conf, previous *operv1.NetworkSpec) {
	if conf.DeployKubeProxy == nil {
		v := shouldDeployKubeProxy(conf)
		conf.DeployKubeProxy = &v
	}

	if !*conf.DeployKubeProxy {
		return
	}

	if conf.KubeProxyConfig == nil {
		conf.KubeProxyConfig = &operv1.ProxyConfig{}
	}

	if conf.KubeProxyConfig.BindAddress == "" {
		// TODO: this will probably need to change for dual stack
		ip, _, err := net.ParseCIDR(conf.ClusterNetwork[0].CIDR)
		if err != nil {
			// this should not happen
			return
		}
		if ip.To4() != nil {
			conf.KubeProxyConfig.BindAddress = "0.0.0.0"
		} else {
			conf.KubeProxyConfig.BindAddress = "::"
		}

	}
}

// isKubeProxyChangeSafe is noop, but it would check if the proposed kube-proxy
// change is safe.
func isKubeProxyChangeSafe(prev, next *operv1.NetworkSpec) []error {
	// At present, all kube-proxy changes are safe to deploy
	return nil
}

// renderStandaloneKubeProxy renders the standalone kube-proxy if installation was
// requested.
func renderStandaloneKubeProxy(conf *operv1.NetworkSpec, manifestDir string) ([]*uns.Unstructured, error) {
	if !*conf.DeployKubeProxy {
		return nil, nil
	}

	kpcDefaults := map[string]operv1.ProxyArgumentList{
		"metrics-bind-address": {"0.0.0.0"},
		"metrics-port":         {"9101"},
		"healthz-port":         {"10256"},
		"proxy-mode":           {"iptables"},
	}

	kpc, err := kubeProxyConfiguration(kpcDefaults, conf, nil)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to generate kube-proxy configuration file")
	}

	data := render.MakeRenderData()
	data.Data["ReleaseVersion"] = os.Getenv("RELEASE_VERSION")
	data.Data["KubeProxyImage"] = os.Getenv("KUBE_PROXY_IMAGE")
	data.Data["KUBERNETES_SERVICE_HOST"] = os.Getenv("KUBERNETES_SERVICE_HOST")
	data.Data["KUBERNETES_SERVICE_PORT"] = os.Getenv("KUBERNETES_SERVICE_PORT")
	data.Data["KubeProxyConfig"] = kpc

	manifests, err := render.RenderDir(filepath.Join(manifestDir, "kube-proxy"), &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render kube-proxy manifests")
	}

	return manifests, nil
}
