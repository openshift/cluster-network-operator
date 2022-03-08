package network

import (
	"net"

	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/platform"
	iputil "github.com/openshift/cluster-network-operator/pkg/util/ip"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	"k8s.io/apimachinery/pkg/util/sets"
	utilnet "k8s.io/utils/net"

	"github.com/pkg/errors"
)

// list of known plugins that require hostPrefix to be set
var pluginsUsingHostPrefix = sets.NewString(string(operv1.NetworkTypeOpenShiftSDN), string(operv1.NetworkTypeOVNKubernetes))

// ValidateClusterConfig ensures the cluster config is valid.
func ValidateClusterConfig(clusterConfig configv1.NetworkSpec, client crclient.Client) error {
	// Check all networks for overlaps
	pool := iputil.IPPool{}

	var ipv4Service, ipv6Service, ipv4Cluster, ipv6Cluster bool

	// Validate ServiceNetwork values
	for _, snet := range clusterConfig.ServiceNetwork {
		_, cidr, err := net.ParseCIDR(snet)
		if err != nil {
			return errors.Wrapf(err, "could not parse spec.serviceNetwork %s", snet)
		}
		if utilnet.IsIPv6CIDR(cidr) {
			ipv6Service = true
		} else {
			ipv4Service = true
		}
		if err := pool.Add(*cidr); err != nil {
			return err
		}
	}

	// Validate count / dual-stack-ness
	if len(clusterConfig.ServiceNetwork) == 0 {
		return errors.Errorf("spec.serviceNetwork must have at least 1 entry")
	} else if len(clusterConfig.ServiceNetwork) == 2 && !(ipv4Service && ipv6Service) {
		return errors.Errorf("spec.serviceNetwork must contain at most one IPv4 and one IPv6 network")
	} else if len(clusterConfig.ServiceNetwork) > 2 {
		return errors.Errorf("spec.serviceNetwork must contain at most one IPv4 and one IPv6 network")
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
		if utilnet.IsIPv6CIDR(cidr) {
			ipv6Cluster = true
		} else {
			ipv4Cluster = true
		}
		// ignore hostPrefix if the plugin does not use it and has it unset
		if pluginsUsingHostPrefix.Has(clusterConfig.NetworkType) || (cnet.HostPrefix != 0) {
			ones, bits := cidr.Mask.Size()
			// The comparison is inverted; smaller number is larger block
			if cnet.HostPrefix < uint32(ones) {
				return errors.Errorf("hostPrefix %d is larger than its cidr %s",
					cnet.HostPrefix, cnet.CIDR)
			}
			if int(cnet.HostPrefix) > bits-2 {
				return errors.Errorf("hostPrefix %d is too small, must be a /%d or larger",
					cnet.HostPrefix, bits-2)
			}
		}
		if err := pool.Add(*cidr); err != nil {
			return err
		}
	}

	if len(clusterConfig.ClusterNetwork) < 1 {
		return errors.Errorf("spec.clusterNetwork must have at least 1 entry")
	}
	if ipv4Cluster != ipv4Service || ipv6Cluster != ipv6Service {
		return errors.Errorf("spec.clusterNetwork and spec.serviceNetwork must either both be IPv4-only, both be IPv6-only, or both be dual-stack")
	}

	if clusterConfig.NetworkType == "" {
		return errors.Errorf("spec.networkType is required")
	}

	// If for whatever reason it is not possible to get the platform type, fail
	infraRes, err := platform.BootstrapInfra(client)
	if err != nil {
		return err
	}

	// Validate that this is either a BareMetal or None PlatformType. For all other
	// PlatformTypes, migration to DualStack is prohibited
	if ipv4Service && ipv6Service || ipv4Cluster && ipv6Cluster {
		if !isSupportedDualStackPlatform(infraRes.PlatformType) {
			return errors.Errorf("DualStack deployments are allowed only for the BareMetal Platform type or the None Platform type")
		}
	}

	return nil
}

// MergeClusterConfig merges the cluster configuration into the real
// CRD configuration.
func MergeClusterConfig(operConf *operv1.NetworkSpec, clusterConf configv1.NetworkSpec) {
	operConf.ServiceNetwork = make([]string, len(clusterConf.ServiceNetwork))
	copy(operConf.ServiceNetwork, clusterConf.ServiceNetwork)

	operConf.ClusterNetwork = []operv1.ClusterNetworkEntry{}
	for _, cnet := range clusterConf.ClusterNetwork {
		operConf.ClusterNetwork = append(operConf.ClusterNetwork, operv1.ClusterNetworkEntry{
			CIDR:       cnet.CIDR,
			HostPrefix: cnet.HostPrefix,
		})
	}

	// OpenShiftSDN (default), OVNKubernetes
	operConf.DefaultNetwork.Type = operv1.NetworkType(clusterConf.NetworkType)
	if operConf.ManagementState == "" {
		operConf.ManagementState = "Managed"
	}
}

// StatusFromOperatorConfig generates the cluster NetworkStatus from the
// currently applied operator configuration.
func StatusFromOperatorConfig(operConf *operv1.NetworkSpec, oldStatus *configv1.NetworkStatus) *configv1.NetworkStatus {
	knownNetworkType := true
	status := configv1.NetworkStatus{}

	switch operConf.DefaultNetwork.Type {
	case operv1.NetworkTypeOpenShiftSDN:
		// continue
	case operv1.NetworkTypeOVNKubernetes:
		// continue
	case operv1.NetworkTypeKuryr:
		// continue
	default:
		knownNetworkType = false
		// Preserve any status fields set by the unknown network plugin
		status = *oldStatus
	}

	if oldStatus.NetworkType == "" || knownNetworkType {
		status.NetworkType = string(operConf.DefaultNetwork.Type)
	}

	// TODO: when we support expanding the service cidr or cluster cidr,
	// don't actually update the status until the changes are rolled out.

	if len(oldStatus.ServiceNetwork) == 0 || knownNetworkType {
		status.ServiceNetwork = operConf.ServiceNetwork
	}
	if len(oldStatus.ClusterNetwork) == 0 || knownNetworkType {
		for _, cnet := range operConf.ClusterNetwork {
			status.ClusterNetwork = append(status.ClusterNetwork,
				configv1.ClusterNetworkEntry{
					CIDR:       cnet.CIDR,
					HostPrefix: cnet.HostPrefix,
				})
		}
	}

	// Determine the MTU from the provider
	switch operConf.DefaultNetwork.Type {
	case operv1.NetworkTypeOpenShiftSDN:
		status.ClusterNetworkMTU = int(*operConf.DefaultNetwork.OpenShiftSDNConfig.MTU)
	case operv1.NetworkTypeOVNKubernetes:
		status.ClusterNetworkMTU = int(*operConf.DefaultNetwork.OVNKubernetesConfig.MTU)
	case operv1.NetworkTypeKuryr:
		status.ClusterNetworkMTU = int(*operConf.DefaultNetwork.KuryrConfig.MTU)
	}

	// Set migration in the config status
	if operConf.Migration != nil {
		status.Migration = &configv1.NetworkMigration{
			NetworkType: string(operConf.Migration.NetworkType),
		}
		if operConf.Migration.MTU != nil {
			status.Migration.MTU = &configv1.MTUMigration{
				Network: (*configv1.MTUMigrationValues)(operConf.Migration.MTU.Network),
				Machine: (*configv1.MTUMigrationValues)(operConf.Migration.MTU.Machine),
			}
		}
	} else {
		status.Migration = nil
	}
	return &status
}
