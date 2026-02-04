package network

import (
	"os"
	"path/filepath"
	"strconv"

	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"github.com/openshift/cluster-network-operator/pkg/hypershift"
	"github.com/openshift/cluster-network-operator/pkg/render"
	"github.com/pkg/errors"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	SystemCNIConfDir      = "/etc/kubernetes/cni/net.d"
	MultusCNIConfDir      = "/var/run/multus/cni/net.d"
	CNIBinDir             = "/var/lib/cni/bin"
	MultusSocketParentDir = "/run/multus"
)

// renderMultus generates the manifests of Multus
func renderMultus(conf *operv1.NetworkSpec, bootstrapResult *bootstrap.BootstrapResult, manifestDir string) ([]*uns.Unstructured, error) {
	if *conf.DisableMultiNetwork {
		return nil, nil
	}

	out := []*uns.Unstructured{}

	// enabling Multus always renders the CRD since Multus uses it
	objs, err := renderAdditionalNetworksCRD(manifestDir)
	if err != nil {
		return nil, err
	}
	out = append(out, objs...)

	usedhcp, usewhereabouts := detectAuxiliaryIPAM(conf)
	h := bootstrapResult.Infra.APIServers[bootstrap.APIServerDefault].Host
	p := bootstrapResult.Infra.APIServers[bootstrap.APIServerDefault].Port
	objs, err = renderMultusConfig(manifestDir, string(conf.DefaultNetwork.Type), usedhcp, usewhereabouts, h, p, bootstrapResult)
	if err != nil {
		return nil, err
	}
	out = append(out, objs...)

	objs, err = renderNetworkMetricsDaemon(manifestDir)
	if err != nil {
		return nil, err
	}
	out = append(out, objs...)

	return out, nil
}

// renderMultusConfig returns the manifests of Multus
func renderMultusConfig(manifestDir, defaultNetworkType string, useDHCP bool, useWhereabouts bool, apihost, apiport string, bootstrapResult *bootstrap.BootstrapResult) ([]*uns.Unstructured, error) {
	objs := []*uns.Unstructured{}

	// render the manifests on disk
	data := render.MakeRenderData()
	data.Data["ReleaseVersion"] = os.Getenv("RELEASE_VERSION")
	data.Data["MultusImage"] = os.Getenv("MULTUS_IMAGE")
	data.Data["CNIPluginsImage"] = os.Getenv("CNI_PLUGINS_IMAGE")
	data.Data["BondCNIPluginImage"] = os.Getenv("BOND_CNI_PLUGIN_IMAGE")
	data.Data["WhereaboutsImage"] = os.Getenv("WHEREABOUTS_CNI_IMAGE")
	data.Data["EgressRouterImage"] = os.Getenv("EGRESS_ROUTER_CNI_IMAGE")
	data.Data["RouteOverrideImage"] = os.Getenv("ROUTE_OVERRRIDE_CNI_IMAGE")
	hsc := hypershift.NewHyperShiftConfig()
	//TODO (relyt0925): when hypershift appropriately signs kube-apiserver certificate with node local
	//address it is my recommendation to move to using the node local loadbalancer for all workload
	if hsc.Enabled && bootstrapResult.Infra.PlatformType == configv1.IBMCloudPlatformType {
		data.Data["KUBERNETES_SERVICE_HOST"] = bootstrapResult.Infra.HostedControlPlane.AdvertiseAddress
		data.Data["KUBERNETES_SERVICE_PORT"] = strconv.Itoa(bootstrapResult.Infra.HostedControlPlane.AdvertisePort)
	} else {
		data.Data["KUBERNETES_SERVICE_HOST"] = apihost
		data.Data["KUBERNETES_SERVICE_PORT"] = apiport
	}
	data.Data["RenderDHCP"] = useDHCP
	data.Data["RenderWhereaboutsAuxillary"] = useWhereabouts
	data.Data["MultusCNIConfDir"] = MultusCNIConfDir
	data.Data["SystemCNIConfDir"] = SystemCNIConfDir
	data.Data["DefaultNetworkType"] = defaultNetworkType
	data.Data["MultusSocketParentDir"] = MultusSocketParentDir
	data.Data["CNIBinDir"] = CNIBinDir
	data.Data["CniSysctlAllowlist"] = "default-cni-sysctl-allowlist"
	data.Data["HTTP_PROXY"] = ""
	data.Data["HTTPS_PROXY"] = ""
	data.Data["NO_PROXY"] = ""
	if bootstrapResult.Infra.ControlPlaneTopology == configv1.ExternalTopologyMode {
		data.Data["HTTP_PROXY"] = bootstrapResult.Infra.Proxy.HTTPProxy
		data.Data["HTTPS_PROXY"] = bootstrapResult.Infra.Proxy.HTTPSProxy
		data.Data["NO_PROXY"] = bootstrapResult.Infra.Proxy.NoProxy
	}
	data.Data["NETWORK_NODE_IDENTITY_ENABLE"] = bootstrapResult.Infra.NetworkNodeIdentityEnabled
	if bootstrapResult.Infra.NetworkNodeIdentityEnabled {
		data.Data["KubeletKubeconfigPath"] = determineKubeConfigPath()
	}

	manifests, err := render.RenderDir(filepath.Join(manifestDir, "network/multus"), &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render multus manifests")
	}
	objs = append(objs, manifests...)
	return objs, nil
}

// renderNetworkMetricsDaemon returns the manifests of the Network Metrics Daemon
func renderNetworkMetricsDaemon(manifestDir string) ([]*uns.Unstructured, error) {

	objs := []*uns.Unstructured{}

	// render the manifests on disk
	data := render.MakeRenderData()
	data.Data["ReleaseVersion"] = os.Getenv("RELEASE_VERSION")
	data.Data["NetworkMetricsImage"] = os.Getenv("NETWORK_METRICS_DAEMON_IMAGE")
	data.Data["KubeRBACProxyImage"] = os.Getenv("KUBE_RBAC_PROXY_IMAGE")

	manifests, err := render.RenderDir(filepath.Join(manifestDir, "network/network-metrics"), &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render multus admission controller manifests")
	}
	objs = append(objs, manifests...)
	return objs, nil
}

// pluginCNIDir is the directory where plugins should install their CNI
// configuration file. By default, it is where multus looks, unless multus
// is disabled
func pluginCNIConfDir(conf *operv1.NetworkSpec) string {
	if *conf.DisableMultiNetwork {
		return SystemCNIConfDir
	}
	return MultusCNIConfDir
}

// determineKubeConfigPath determines which kubeconfig path to use for certificate generation.
// In some cases, specifically IBM Managed Openshift, the path differs.
func determineKubeConfigPath() string {
	var kubeletKubeconfigPaths = []string{
		"/var/lib/kubelet/kubeconfig",        // Traditional OCP
		"/etc/kubernetes/kubelet-kubeconfig", // IBM Managed OpenShift, see OCPBUGS-19795
	}

	for _, path := range kubeletKubeconfigPaths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	// If none are found, we must assume the default traditional OCP path.
	return kubeletKubeconfigPaths[0]
}
