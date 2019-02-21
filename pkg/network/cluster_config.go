package network

import (
	"net"

	configv1 "github.com/openshift/api/config/v1"
	netopv1 "github.com/openshift/cluster-network-operator/pkg/apis/networkoperator/v1"
	iputil "github.com/openshift/cluster-network-operator/pkg/util/ip"

	"github.com/pkg/errors"
)

// ValidateClusterConfig ensures the cluster config is valid.
func ValidateClusterConfig(clusterConfig configv1.NetworkSpec) error {
	// Check all networks for overlaps
	pool := iputil.IPPool{}

	if len(clusterConfig.ServiceNetwork) == 0 {
		// Right now we only support a single service network
		return errors.Errorf("spec.serviceNetwork must have at least 1 entry")
	}
	for _, snet := range clusterConfig.ServiceNetwork {
		_, cidr, err := net.ParseCIDR(snet)
		if err != nil {
			return errors.Wrapf(err, "could not parse spec.serviceNetwork %s", snet)
		}
		if err := pool.Add(*cidr); err != nil {
			return err
		}
	}

	// validate clusternetwork
	// - has an entry
	// - it is a valid ip
	// - has a reasonable cidr
	// - they do not overlap and do not overlap with the service cidr
	for _, cnet := range clusterConfig.ClusterNetwork {
		_, cidr, err := net.ParseCIDR(cnet.CIDR)
		if err != nil {
			return errors.Errorf("could not parse spec.clusterNetwork %s", cnet.CIDR)
		}
		size, _ := cidr.Mask.Size()
		// The comparison is inverted; smaller number is larger block
		if cnet.HostPrefix < uint32(size) {
			return errors.Errorf("hostPrefix %d is larger than its cidr %s",
				cnet.HostPrefix, cnet.CIDR)
		}
		if cnet.HostPrefix > 30 {
			return errors.Errorf("hostPrefix %d is too small, must be a /30 or larger",
				cnet.HostPrefix)
		}
		if err := pool.Add(*cidr); err != nil {
			return err
		}
	}

	if len(clusterConfig.ClusterNetwork) < 1 {
		return errors.Errorf("spec.clusterNetwork must have at least 1 entry")
	}

	if clusterConfig.NetworkType == "" {
		return errors.Errorf("spec.networkType is required")
	}

	return nil
}

// MergeClusterConfig merges the cluster configuration in to the real
// CRD configuration.
func MergeClusterConfig(operConf *netopv1.NetworkConfigSpec, clusterConf configv1.NetworkSpec) {
	operConf.ServiceNetwork = clusterConf.ServiceNetwork

	operConf.ClusterNetwork = []netopv1.ClusterNetworkEntry{}
	for _, cnet := range clusterConf.ClusterNetwork {
		operConf.ClusterNetwork = append(operConf.ClusterNetwork, netopv1.ClusterNetworkEntry{
			CIDR:       cnet.CIDR,
			HostPrefix: cnet.HostPrefix,
		})
	}

	operConf.DefaultNetwork.Type = netopv1.NetworkType(clusterConf.NetworkType)
}

// StatusFromOperatorConfig generates the cluster NetworkStatus from the currently applied operator configuration.
func StatusFromOperatorConfig(operConf *netopv1.NetworkConfigSpec) configv1.NetworkStatus {
	status := configv1.NetworkStatus{
		ServiceNetwork: operConf.ServiceNetwork,
		NetworkType:    string(operConf.DefaultNetwork.Type),
	}

	for _, cnet := range operConf.ClusterNetwork {
		status.ClusterNetwork = append(status.ClusterNetwork,
			configv1.ClusterNetworkEntry{
				CIDR:       cnet.CIDR,
				HostPrefix: cnet.HostPrefix,
			})
	}

	// Determine the MTU from the provider
	switch operConf.DefaultNetwork.Type {
	case netopv1.NetworkTypeOpenShiftSDN:
		status.ClusterNetworkMTU = int(*operConf.DefaultNetwork.OpenShiftSDNConfig.MTU)
	}

	return status
}
