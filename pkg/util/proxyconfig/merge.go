package proxyconfig

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/ghodss/yaml"

	configv1 "github.com/openshift/api/config/v1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

// MergeUserSystemNoProxy merges user supplied noProxy settings from proxy
// with cluster-wide noProxy settings. It returns a merged, comma-separated
// string of noProxy settings. If no user supplied noProxy settings are
// provided, a comma-separated string of cluster-wide noProxy settings
// are returned.
func MergeUserSystemNoProxy(proxy *configv1.Proxy, infra *configv1.Infrastructure, network *configv1.Network, cluster *corev1.ConfigMap) (string, error) {
	apiServerURL, err := url.Parse(infra.Status.APIServerURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse API server URL")
	}

	internalAPIServer, err := url.Parse(infra.Status.APIServerInternalURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse API server internal URL")
	}

	set := sets.NewString(
		"127.0.0.1",
		"localhost",
		network.Status.ServiceNetwork[0],
		apiServerURL.Hostname(),
		internalAPIServer.Hostname(),
	)

	// TODO: This will be flexible when master machine management is more dynamic.
	type installConfig struct {
		ControlPlane struct {
			Replicas string `json:"replicas"`
		} `json:"controlPlane"`
		Networking struct {
			MachineCIDR string `json:"machineCIDR"`
		} `json:"networking"`
	}
	var ic installConfig
	data, ok := cluster.Data["install-config"]
	if !ok {
		return "", fmt.Errorf("missing install-config in configmap")
	}
	if err := yaml.Unmarshal([]byte(data), &ic); err != nil {
		return "", fmt.Errorf("invalid install-config: %v\njson:\n%s", err, data)
	}

	switch infra.Status.PlatformStatus.Type {
	case configv1.AWSPlatformType, configv1.GCPPlatformType, configv1.AzurePlatformType, configv1.OpenStackPlatformType:
		set.Insert("169.254.169.254", ic.Networking.MachineCIDR)
	}

	replicas, err := strconv.Atoi(ic.ControlPlane.Replicas)
	if err != nil {
		return "", fmt.Errorf("failed to parse install config replicas: %v", err)
	}

	for i := int64(0); i < int64(replicas); i++ {
		etcdHost := fmt.Sprintf("etcd-%d.%s", i, infra.Status.EtcdDiscoveryDomain)
		set.Insert(etcdHost)
	}

	for _, clusterNetwork := range network.Status.ClusterNetwork {
		set.Insert(clusterNetwork.CIDR)
	}

	if len(proxy.Spec.NoProxy) > 0 {
		for _, userValue := range strings.Split(proxy.Spec.NoProxy, ",") {
			if userValue == "" {
				return "", fmt.Errorf("failed to parse noProxy from proxy spec")
			}
			set.Insert(userValue)
		}
	}

	return strings.Join(set.List(), ","), nil
}
