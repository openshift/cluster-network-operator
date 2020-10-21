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

// acceptsKubeProxyConfig determines if the desired network type allows
// conf.KubeProxyConfig to be set. OpenShiftSDN deploys its own kube-proxy.
// OVNKubernetes and Kuryr do not allow Kubernetes to be used. All other
// types are assumed to use an external kube-proxy.
func acceptsKubeProxyConfig(conf *operv1.NetworkSpec) bool {
	switch conf.DefaultNetwork.Type {
	case operv1.NetworkTypeOpenShiftSDN:
		return true
	case operv1.NetworkTypeOVNKubernetes:
		return false
	case operv1.NetworkTypeKuryr:
		return false
	default:
		return true
	}
}

func noKubeProxyConfig(conf *operv1.NetworkSpec) bool {
	p := conf.KubeProxyConfig
	if p == nil {
		return true
	}
	if p.IptablesSyncPeriod != "" || len(p.ProxyArguments) > 0 {
		return false
	}
	// Accept either no value or the value from fillKubeProxyDefaults()
	if p.BindAddress != "" && p.BindAddress != "0.0.0.0" && p.BindAddress != "::" {
		return false
	}
	return true
}

// validateKubeProxy checks that the kube-proxy specific configuration is basically sane.
func validateKubeProxy(conf *operv1.NetworkSpec) []error {
	out := []error{}
	p := conf.KubeProxyConfig
	if p == nil {
		return out
	}
	if !acceptsKubeProxyConfig(conf) {
		if noKubeProxyConfig(conf) {
			return out
		}
		out = append(out, errors.Errorf("network type %q does not allow specifying kube-proxy options", conf.DefaultNetwork.Type))
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

	// Don't allow ports to be overridden. For backward compatibility, we allow
	// explicitly specifying the (old) default values, though we prefer for them to be
	// left blank.
	if p.ProxyArguments != nil {
		if val, ok := p.ProxyArguments["metrics-port"]; ok {
			if len(val) != 1 || val[0] != "9101" {
				out = append(out, errors.Errorf("kube-proxy --metrics-port cannot be overridden"))
			}
		}
		if val, ok := p.ProxyArguments["healthz-port"]; ok {
			if len(val) != 1 || val[0] != "10256" {
				out = append(out, errors.Errorf("kube-proxy --healthz-port cannot be overridden"))
			}
		}
	}

	return out
}

// defaultDeployKubeProxy determines if kube-proxy is deployed by default for the given
// network type. OpenShiftSDN deploys its own kube-proxy. OVNKubernetes and Kuryr handle
// services on their own. All other network providers are assumed to require a
// standalone kube-proxy
func defaultDeployKubeProxy(conf *operv1.NetworkSpec) bool {
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

// fillKubeProxyDefaults inserts kube-proxy defaults, if kube-proxy will be deployed
// explicitly.
func fillKubeProxyDefaults(conf, previous *operv1.NetworkSpec) {
	if conf.DeployKubeProxy == nil {
		v := defaultDeployKubeProxy(conf)
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

	metricsPort := "9102"
	healthzPort := "10255"
	if val, ok := conf.KubeProxyConfig.ProxyArguments["metrics-port"]; ok {
		metricsPort = val[0]
	}
	if val, ok := conf.KubeProxyConfig.ProxyArguments["healthz-port"]; ok {
		healthzPort = val[0]
	}

	kpcDefaults := map[string]operv1.ProxyArgumentList{
		"metrics-bind-address": {"0.0.0.0"},
		"metrics-port":         {"9102"},
		"healthz-port":         {"10255"},
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
	data.Data["MetricsPort"] = metricsPort
	data.Data["HealthzPort"] = healthzPort

	manifests, err := render.RenderDir(filepath.Join(manifestDir, "kube-proxy"), &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render kube-proxy manifests")
	}

	return manifests, nil
}
