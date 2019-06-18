package network

import (
	"net"
	"time"

	"github.com/pkg/errors"

	operv1 "github.com/openshift/api/operator/v1"
	k8sutil "github.com/openshift/cluster-network-operator/pkg/util/k8s"
)

// kubeProxyConfiguration builds the (yaml text of) the kube-proxy config object
func kubeProxyConfiguration(conf *operv1.NetworkSpec, pluginDefaults map[string][]string) (string, error) {
	p := conf.KubeProxyConfig

	args := map[string][]string{}
	args["bind-address"] = []string{p.BindAddress}
	if len(conf.ClusterNetwork) == 1 {
		args["cluster-cidr"] = []string{conf.ClusterNetwork[0].CIDR}
	}
	args["iptables-sync-period"] = []string{p.IptablesSyncPeriod}

	args = k8sutil.MergeKubeProxyArguments(args, pluginDefaults)
	args = k8sutil.MergeKubeProxyArguments(args, p.ProxyArguments)

	return k8sutil.GenerateKubeProxyConfiguration(args)
}

// validateKubeProxy checks that the kube-proxy specific configuration
// is basically sane.
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

	return out
}
