package network

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"

	configv1 "github.com/openshift/api/config/v1"
	v1 "github.com/openshift/api/network/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/platform"
	iputil "github.com/openshift/cluster-network-operator/pkg/util/ip"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/component-base/metrics"
	"k8s.io/component-base/metrics/legacyregistry"
	utilnet "k8s.io/utils/net"

	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
)

// list of known plugins that require hostPrefix to be set
var pluginsUsingHostPrefix = sets.NewString(string(operv1.NetworkTypeOpenShiftSDN), string(operv1.NetworkTypeOVNKubernetes))

// ValidateClusterConfig ensures the cluster config is valid.
func ValidateClusterConfig(clusterConfig *configv1.Network, client cnoclient.Client) error {
	// If for whatever reason it is not possible to get the platform type, fail
	infraRes, err := platform.InfraStatus(client)
	if err != nil {
		return err
	}
	return validateClusterConfig(clusterConfig, infraRes, client)
}

func validateClusterConfig(clusterConfig *configv1.Network, infraRes *bootstrap.InfraStatus, client cnoclient.Client) error {

	// Check all networks for overlaps
	pool := iputil.IPPool{}

	var ipv4Service, ipv6Service, ipv4Cluster, ipv6Cluster bool

	// Validate ServiceNetwork values
	for _, snet := range clusterConfig.Spec.ServiceNetwork {
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
	if len(clusterConfig.Spec.ServiceNetwork) == 0 {
		return errors.Errorf("spec.serviceNetwork must have at least 1 entry")
	} else if len(clusterConfig.Spec.ServiceNetwork) == 2 && !(ipv4Service && ipv6Service) {
		return errors.Errorf("spec.serviceNetwork must contain at most one IPv4 and one IPv6 network")
	} else if len(clusterConfig.Spec.ServiceNetwork) > 2 {
		return errors.Errorf("spec.serviceNetwork must contain at most one IPv4 and one IPv6 network")
	}

	// validate clusternetwork
	// - has an entry
	// - it is a valid ip
	// - has a reasonable cidr
	// - they do not overlap and do not overlap with the service cidr
	for _, cnet := range clusterConfig.Spec.ClusterNetwork {
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
		if pluginsUsingHostPrefix.Has(clusterConfig.Spec.NetworkType) || (cnet.HostPrefix != 0) {
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

	if len(clusterConfig.Spec.ClusterNetwork) < 1 {
		return errors.Errorf("spec.clusterNetwork must have at least 1 entry")
	}
	if ipv4Cluster != ipv4Service || ipv6Cluster != ipv6Service {
		return errors.Errorf("spec.clusterNetwork and spec.serviceNetwork must either both be IPv4-only, both be IPv6-only, or both be dual-stack")
	}

	if clusterConfig.Spec.NetworkType == "" {
		return errors.Errorf("spec.networkType is required")
	}

	// Validate that this is either a BareMetal or None PlatformType. For all other
	// PlatformTypes, migration to DualStack is prohibited
	if ipv4Service && ipv6Service || ipv4Cluster && ipv6Cluster {
		if !isSupportedDualStackPlatform(infraRes.PlatformType) {
			return errors.Errorf("%s is not one of the supported platforms for dual stack (%s)", infraRes.PlatformType,
				strings.Join(dualStackPlatforms.List(), ", "))
		}
	}

	if _, ok := clusterConfig.Annotations[names.NetworkTypeMigrationAnnotation]; ok {
		return ValidateLiveMigration(clusterConfig, infraRes, client)
	}

	return nil
}

// validateCIDROverlap validates whether any of the ClusterNetwork and ServiceNetwork CIDRs overlap with
// the internal CIDRs used by OVNKubernetes
func validateOVNKubernetesCIDROverlap(clusterCofnig *configv1.Network, operConfig *operv1.Network) error {
	// Verify whether the available subnets conflict between CNIs
	type subnet struct {
		name string
		cidr string
	}
	var subnets []subnet
	for _, cn := range clusterCofnig.Spec.ClusterNetwork {
		subnets = append(subnets, subnet{
			name: "clusterNetwork",
			cidr: cn.CIDR,
		})
	}
	for _, svcNet := range clusterCofnig.Spec.ServiceNetwork {
		subnets = append(subnets, subnet{
			name: "serviceNetwork",
			cidr: svcNet,
		})
	}

	v4InternalSubnet, v6InternalSubnet := GetInternalSubnets(operConfig.Spec.DefaultNetwork.OVNKubernetesConfig)
	subnets = append(subnets, subnet{
		name: "v4InternalSubnet",
		cidr: v4InternalSubnet,
	})
	subnets = append(subnets, subnet{
		name: "v6InternalSubnet",
		cidr: v6InternalSubnet,
	})

	v4InternalTransitSwitchSubnet, v6InternalTransitSwitchSubnet := GetTransitSwitchSubnets(operConfig.Spec.DefaultNetwork.OVNKubernetesConfig)
	subnets = append(subnets, subnet{
		name: "v4InternalTransitSwitchSubnet",
		cidr: v4InternalTransitSwitchSubnet,
	})
	subnets = append(subnets, subnet{
		name: "v6InternalTransitSwitchSubnet",
		cidr: v6InternalTransitSwitchSubnet,
	})

	v4InternalMasqueradeSubnet, v6InternalMasqueradeSubnet := GetMasqueradeSubnet(operConfig.Spec.DefaultNetwork.OVNKubernetesConfig)
	subnets = append(subnets, subnet{
		name: "v4InternalMasqueradeSubnet",
		cidr: v4InternalMasqueradeSubnet,
	})
	subnets = append(subnets, subnet{
		name: "v6InternalMasqueradeSubnet",
		cidr: v6InternalMasqueradeSubnet,
	})

	for i, subnetA := range subnets {
		for j, subnetB := range subnets {
			// do not compare the same elements
			if i == j {
				continue
			}

			_, netA, err := net.ParseCIDR(subnetA.cidr)
			if err != nil {
				return errors.Wrapf(err, "could not parse %s:%s", subnetA.name, subnetA.cidr)
			}
			_, netB, err := net.ParseCIDR(subnetB.cidr)
			if err != nil {
				return errors.Wrapf(err, "could not parse %s:%s", subnetB.name, subnetB.cidr)
			}
			if netA.Contains(netB.IP) || netB.Contains(netA.IP) {
				metricLiveMigrationBlocked.With(prometheus.Labels{metricLiveMigrationBlockedLabelKey: fmt.Sprintf("overlapping_networks_%s_%s", subnetA.name, subnetB.name)}).Set(1)
				return fmt.Errorf("network %s(%s) overlaps with network %s(%s)",
					subnetA.name, subnetA.cidr, subnetB.name, subnetB.cidr)
			}
		}
	}

	return nil
}

const metricLiveMigrationBlockedLabelKey = "reason"

// openshift_network_operator_live_migration_blocked metric 'reason' label key name values
const (
	unsupportedCNI                     = "UnsupportedCNI"
	unsupportedHCPCluster              = "UnsupportedHyperShiftCluster"
	unsupportedSDNNetworkIsolationMode = "UnsupportedSDNNetworkIsolationMode"
	unsupportedMACVlanInterface        = "UnsupportedMACVLANInterface"
)

var metricLiveMigrationBlocked = metrics.NewGaugeVec(&metrics.GaugeOpts{
	Namespace: "openshift_network_operator",
	Name:      "live_migration_blocked",
	Help: "A metric which contains a constant '1' value labeled with the reason CNI live migration may not begin. " +
		"The metric is available when CNI live migration has started by annotating the Network CR.",
}, []string{metricLiveMigrationBlockedLabelKey})

var liveMigrationBlockedMetricOnce sync.Once

func ValidateLiveMigration(clusterConfig *configv1.Network, infraRes *bootstrap.InfraStatus, client cnoclient.Client) error {
	liveMigrationBlockedMetricOnce.Do(func() {
		legacyregistry.MustRegister(metricLiveMigrationBlocked)
	})
	metricLiveMigrationBlocked.Reset()
	// If the migration is completed or is already progressing do not run the validation
	if clusterConfig.Spec.NetworkType == clusterConfig.Status.NetworkType ||
		meta.IsStatusConditionPresentAndEqual(clusterConfig.Status.Conditions, names.NetworkTypeMigrationInProgress, metav1.ConditionTrue) {
		return nil
	}
	if clusterConfig.Spec.NetworkType != string(operv1.NetworkTypeOpenShiftSDN) &&
		clusterConfig.Spec.NetworkType != string(operv1.NetworkTypeOVNKubernetes) {
		metricLiveMigrationBlocked.With(prometheus.Labels{metricLiveMigrationBlockedLabelKey: unsupportedCNI}).Set(1)
		return fmt.Errorf("network type live migration is only supported for OVNKubernetes and OpenShiftSDN CNI")
	}

	if infraRes.HostedControlPlane != nil {
		metricLiveMigrationBlocked.With(prometheus.Labels{metricLiveMigrationBlockedLabelKey: unsupportedHCPCluster}).Set(1)
		return errors.Errorf("network type live migration is not supported on HyperShift clusters")
	}

	operConfig := &operv1.Network{}
	err := client.Default().CRClient().Get(context.TODO(), types.NamespacedName{Name: names.CLUSTER_CONFIG}, operConfig)
	if err != nil {
		return errors.Errorf("error getting network configuration: %v", err)
	}

	// Status contains the CNI we are migrating from
	if clusterConfig.Status.NetworkType == string(operv1.NetworkTypeOpenShiftSDN) {
		if operConfig.Spec.DefaultNetwork.OpenShiftSDNConfig != nil &&
			operConfig.Spec.DefaultNetwork.OpenShiftSDNConfig.Mode == operv1.SDNModeMultitenant {
			metricLiveMigrationBlocked.With(prometheus.Labels{metricLiveMigrationBlockedLabelKey: unsupportedSDNNetworkIsolationMode}).Set(1)
			return errors.Errorf("network type live migration is not supported on SDN Multitenant clusters")
		}

		if err := validateOVNKubernetesCIDROverlap(clusterConfig, operConfig); err != nil {
			return err
		}

		// Detect pods with pod.network.openshift.io/assign-macvlan (Used by egress router)
		macVlanPods, err := cnoclient.ListAllPodsWithAnnotationKey(context.TODO(), client, v1.AssignMacvlanAnnotation)
		if err != nil {
			return errors.Wrapf(err, "error listing macvlan pods")
		}
		if len(macVlanPods) != 0 {
			metricLiveMigrationBlocked.With(prometheus.Labels{metricLiveMigrationBlockedLabelKey: unsupportedMACVlanInterface}).Set(1)
			return fmt.Errorf("network type live migration is not supported for pods with %q annotation."+
				" Please remove all egress router pods", v1.AssignMacvlanAnnotation)
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
	}

	// Set migration in the config status
	if operConf.Migration != nil {
		if operConf.Migration.Mode == operv1.LiveNetworkMigrationMode {
			// in live migration mode, we want MCO to follow the DefaultNetwork in the operator config
			status.Migration = &configv1.NetworkMigration{
				NetworkType: string(operConf.DefaultNetwork.Type),
			}
		} else {
			status.Migration = &configv1.NetworkMigration{
				NetworkType: operConf.Migration.NetworkType,
			}
		}

		if operConf.Migration.MTU != nil {
			status.Migration.MTU = &configv1.MTUMigration{
				Network: (*configv1.MTUMigrationValues)(operConf.Migration.MTU.Network),
				Machine: (*configv1.MTUMigrationValues)(operConf.Migration.MTU.Machine),
			}
		}

		if operConf.Migration.Mode == operv1.LiveNetworkMigrationMode {
			status.Conditions = oldStatus.Conditions
		}
	} else {
		status.Migration = nil
	}
	return &status
}
