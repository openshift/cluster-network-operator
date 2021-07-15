package proxyconfig

import (
	"fmt"
	"net"
	"net/url"
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
	// TODO: This will be flexible when master machine management is more dynamic.
	type machineNetworkEntry struct {
		// CIDR is the IP block address pool for machines within the cluster.
		CIDR string `json:"cidr"`
	}
	type installConfig struct {
		ControlPlane struct {
			Replicas string `json:"replicas"`
		} `json:"controlPlane"`
		Networking struct {
			MachineCIDR    string                `json:"machineCIDR"`
			MachineNetwork []machineNetworkEntry `json:"machineNetwork,omitempty"`
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

	set := sets.NewString(
		"127.0.0.1",
		"localhost",
		".svc",
		".cluster.local",
	)
	if ic.Networking.MachineCIDR != "" {
		if _, _, err := net.ParseCIDR(ic.Networking.MachineCIDR); err != nil {
			return "", fmt.Errorf("MachineCIDR has an invalid CIDR: %s", ic.Networking.MachineCIDR)
		}
		set.Insert(ic.Networking.MachineCIDR)
	}

	for _, mc := range ic.Networking.MachineNetwork {
		if _, _, err := net.ParseCIDR(mc.CIDR); err != nil {
			return "", fmt.Errorf("MachineNetwork has an invalid CIDR: %s", mc.CIDR)
		}
		set.Insert(mc.CIDR)
	}

	if len(infra.Status.APIServerInternalURL) > 0 {
		internalAPIServer, err := url.Parse(infra.Status.APIServerInternalURL)
		if err != nil {
			return "", fmt.Errorf("failed to parse internal api server internal url")
		}
		set.Insert(internalAPIServer.Hostname())
	} else {
		return "", fmt.Errorf("internal api server url missing from infrastructure config '%s'", infra.Name)
	}

	if len(network.Status.ServiceNetwork) > 0 {
		for _, nss := range network.Status.ServiceNetwork {
			set.Insert(nss)
		}
	} else {
		return "", fmt.Errorf("serviceNetwork missing from network '%s' status", network.Name)
	}

	if infra.Status.PlatformStatus != nil {
		switch infra.Status.PlatformStatus.Type {
		case configv1.AWSPlatformType, configv1.GCPPlatformType, configv1.AzurePlatformType, configv1.OpenStackPlatformType:
			set.Insert("169.254.169.254")
		}

		// Construct the node sub domain.
		// TODO: Add support for additional cloud providers.
		switch infra.Status.PlatformStatus.Type {
		case configv1.AWSPlatformType:
			region := infra.Status.PlatformStatus.AWS.Region
			if region == "us-east-1" {
				set.Insert(".ec2.internal")
			} else {
				set.Insert(fmt.Sprintf(".%s.compute.internal", region))
			}
		case configv1.GCPPlatformType:
			// From https://cloud.google.com/vpc/docs/special-configurations add GCP metadata.
			// "metadata.google.internal." added due to https://bugzilla.redhat.com/show_bug.cgi?id=1754049
			set.Insert("metadata", "metadata.google.internal", "metadata.google.internal.")
		}
	}

	if len(network.Status.ClusterNetwork) > 0 {
		for _, clusterNetwork := range network.Status.ClusterNetwork {
			set.Insert(clusterNetwork.CIDR)
		}
	} else {
		return "", fmt.Errorf("clusterNetwork missing from network `%s` status", network.Name)
	}

	if len(proxy.Spec.NoProxy) > 0 {
		for _, userValue := range strings.Split(proxy.Spec.NoProxy, ",") {
			if userValue != "" {
				set.Insert(userValue)
			}
		}
	}

	return strings.Join(set.List(), ","), nil
}
