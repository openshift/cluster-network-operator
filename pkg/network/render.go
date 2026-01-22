package network

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	configv1 "github.com/openshift/api/config/v1"
	apifeatures "github.com/openshift/api/features"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/hypershift"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/retry"
	utilnet "k8s.io/utils/net"

	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/render"
	iputil "github.com/openshift/cluster-network-operator/pkg/util/ip"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
)

const (
	pluginName = "networking-console-plugin"
)

func Render(operConf *operv1.NetworkSpec, clusterConf *configv1.NetworkSpec, manifestDir string, client cnoclient.Client, featureGates featuregates.FeatureGate, bootstrapResult *bootstrap.BootstrapResult) ([]*uns.Unstructured, bool, error) {
	log.Printf("Starting render phase")
	var progressing bool
	objs := []*uns.Unstructured{}

	// render cloud network config controller **before** the network plugin.
	// the network plugin is dependent upon having the cloud network CRD
	// defined as to initialize its watcher, otherwise it will error and crash
	o, err := renderCloudNetworkConfigController(operConf, bootstrapResult, manifestDir)
	if err != nil {
		return nil, progressing, err
	}
	objs = append(objs, o...)

	// render Multus
	o, err = renderMultus(operConf, bootstrapResult, manifestDir)
	if err != nil {
		return nil, progressing, err
	}
	objs = append(objs, o...)

	// render MultusAdmissionController
	o, err = renderMultusAdmissionController(operConf, manifestDir,
		bootstrapResult.Infra.ControlPlaneTopology == configv1.ExternalTopologyMode, bootstrapResult, client, featureGates)
	if err != nil {
		return nil, progressing, err
	}
	objs = append(objs, o...)

	// render MultiNetworkPolicy
	o, err = renderMultiNetworkpolicy(operConf, manifestDir)
	if err != nil {
		return nil, progressing, err
	}
	objs = append(objs, o...)

	// render default network
	o, progressing, err = renderDefaultNetwork(operConf, bootstrapResult, manifestDir, client, featureGates)
	if err != nil {
		return nil, progressing, err
	}
	objs = append(objs, o...)

	// render kube-proxy
	// DPU_DEV_PREVIEW
	// There is currently a restriction that renderStandaloneKubeProxy() is
	// called after renderDefaultNetwork(). The OVN-Kubernetes code is enabling
	// KubeProxy in Node Mode of "dpu".
	o, err = renderStandaloneKubeProxy(operConf, bootstrapResult, manifestDir)
	if err != nil {
		return nil, progressing, err
	}
	objs = append(objs, o...)

	// render additional networks
	o, err = renderAdditionalNetworks(operConf, manifestDir)
	if err != nil {
		return nil, progressing, err
	}
	objs = append(objs, o...)

	// render network diagnostics
	o, err = renderNetworkDiagnostics(operConf, clusterConf, manifestDir)
	if err != nil {
		return nil, progressing, err
	}
	objs = append(objs, o...)

	// render network public
	o, err = renderNetworkPublic(manifestDir)
	if err != nil {
		return nil, progressing, err
	}
	objs = append(objs, o...)

	// render network node identity
	o, err = renderNetworkNodeIdentity(operConf, bootstrapResult, manifestDir, client)
	if err != nil {
		return nil, progressing, err
	}
	objs = append(objs, o...)

	o, err = renderCNO(manifestDir)
	if err != nil {
		return nil, progressing, err
	}
	objs = append(objs, o...)

	o, err = renderIPTablesAlerter(operConf, bootstrapResult, manifestDir)
	if err != nil {
		return nil, progressing, err
	}
	objs = append(objs, o...)

	o, err = renderAdditionalRoutingCapabilities(operConf, manifestDir)
	if err != nil {
		return nil, progressing, err
	}
	objs = append(objs, o...)

	// render networking console plugin
	o, err = renderNetworkingConsolePlugin(manifestDir, bootstrapResult)
	if err != nil {
		return nil, progressing, err
	}
	if o != nil {
		objs = append(objs, o...)
	}

	err = registerNetworkingConsolePlugin(bootstrapResult, client)
	if err != nil {
		return nil, progressing, err
	}

	log.Printf("Render phase done, rendered %d objects", len(objs))
	return objs, progressing, nil
}

// deprecatedCanonicalizeIPAMConfig converts configuration to a canonical form
// for backward compatibility.
func deprecatedCanonicalizeIPAMConfig(conf *operv1.IPAMConfig) {
	switch strings.ToLower(string(conf.Type)) {
	case strings.ToLower(string(operv1.IPAMTypeDHCP)):
		conf.Type = operv1.IPAMTypeDHCP
	case strings.ToLower(string(operv1.IPAMTypeStatic)):
		conf.Type = operv1.IPAMTypeStatic
	}
}

// deprecatedCanonicalizeSimpleMacvlanConfig converts configuration to a canonical form
// for backward compatibility.
func deprecatedCanonicalizeSimpleMacvlanConfig(conf *operv1.SimpleMacvlanConfig) {
	switch strings.ToLower(string(conf.Mode)) {
	case strings.ToLower(string(operv1.MacvlanModeBridge)):
		conf.Mode = operv1.MacvlanModeBridge
	case strings.ToLower(string(operv1.MacvlanModePrivate)):
		conf.Mode = operv1.MacvlanModePrivate
	case strings.ToLower(string(operv1.MacvlanModeVEPA)):
		conf.Mode = operv1.MacvlanModeVEPA
	case strings.ToLower(string(operv1.MacvlanModePassthru)):
		conf.Mode = operv1.MacvlanModePassthru
	}

	if conf.IPAMConfig != nil {
		deprecatedCanonicalizeIPAMConfig(conf.IPAMConfig)
	}
}

// DeprecatedCanonicalize converts configuration to a canonical form for backward
// compatibility.
//
//	*** DO NOT ADD ANY NEW CANONICALIZATION TO THIS FUNCTION! ***
//
// Altering the user-provided configuration from CNO causes problems when other components
// need to look at the configuration before CNO starts. Users should just write the
// configuration in the correct form to begin with.
//
// However, we cannot remove any of the existing canonicalizations because this might
// break existing clusters.
func DeprecatedCanonicalize(conf *operv1.NetworkSpec) {
	orig := conf.DeepCopy()

	if strings.EqualFold(string(conf.DefaultNetwork.Type), string(operv1.NetworkTypeOVNKubernetes)) {
		conf.DefaultNetwork.Type = operv1.NetworkTypeOVNKubernetes
	}

	for idx, an := range conf.AdditionalNetworks {
		switch strings.ToLower(string(an.Type)) {
		case strings.ToLower(string(operv1.NetworkTypeRaw)):
			conf.AdditionalNetworks[idx].Type = operv1.NetworkTypeRaw
		case strings.ToLower(string(operv1.NetworkTypeSimpleMacvlan)):
			conf.AdditionalNetworks[idx].Type = operv1.NetworkTypeSimpleMacvlan
		}

		if an.Type == operv1.NetworkTypeSimpleMacvlan && an.SimpleMacvlanConfig != nil {
			deprecatedCanonicalizeSimpleMacvlanConfig(conf.AdditionalNetworks[idx].SimpleMacvlanConfig)
		}
	}

	if !reflect.DeepEqual(orig, conf) {
		log.Printf("WARNING: One or more fields of Network.operator.openshift.io was incorrectly capitalized. Although this has been fixed now, it is possible that other components previously saw the incorrect value and interpreted it incorrectly.")
		log.Printf("Original spec: %#v\nModified spec: %#v\n", orig, conf)
	}
}

// Validate checks that the supplied configuration is reasonable.
// This should be called after Canonicalize
func Validate(conf *operv1.NetworkSpec) error {
	errs := []error{}

	errs = append(errs, validateIPPools(conf)...)
	errs = append(errs, validateDefaultNetwork(conf)...)
	errs = append(errs, validateMultus(conf)...)
	errs = append(errs, validateKubeProxy(conf)...)
	errs = append(errs, validateMigration(conf)...)

	if len(errs) > 0 {
		return errors.Errorf("invalid configuration: %v", errs)
	}
	return nil
}

// FillDefaults computes any default values and applies them to the configuration
// This is a mutating operation. It should be called after Validate.
//
// Defaults are carried forward from previous if it is provided. This is so we
// can change defaults as we move forward, but won't disrupt existing clusters.
//
// We may need to know the MTU of nodes in the cluster, so we can compute the correct
// underlay MTU for OVN-K.
func FillDefaults(conf, previous *operv1.NetworkSpec, hostMTU int) {
	// DisableMultiNetwork defaults to false
	if conf.DisableMultiNetwork == nil {
		disable := false
		conf.DisableMultiNetwork = &disable
	}

	// UseMultiNetworkPolicy defaults to false
	if conf.UseMultiNetworkPolicy == nil {
		disable := false
		conf.UseMultiNetworkPolicy = &disable
	}

	if len(conf.LogLevel) == 0 {
		conf.LogLevel = "Normal"
	}

	fillDefaultNetworkDefaults(conf, previous, hostMTU)
	fillKubeProxyDefaults(conf, previous)
}

// IsChangeSafe checks to see if the change between prev and next are allowed
// FillDefaults and Validate should have been called, but beware that prev may
// be from an older version.
func IsChangeSafe(prev, next *operv1.NetworkSpec, infraStatus *bootstrap.InfraStatus) error {
	if prev == nil {
		return nil
	}

	// Easy way out: nothing changed.
	if reflect.DeepEqual(prev, next) {
		return nil
	}

	errs := []error{}

	// Most ClusterNetworks/ServiceNetwork changes are not allowed
	if err := isNetworkChangeSafe(prev, next, infraStatus); err != nil {
		errs = append(errs, err)
	}

	// Check the default network
	errs = append(errs, isDefaultNetworkChangeSafe(prev, next)...)

	// Changing AdditionalNetworks is supported
	if !reflect.DeepEqual(prev.DisableMultiNetwork, next.DisableMultiNetwork) {
		errs = append(errs, errors.Errorf("cannot change DisableMultiNetwork"))
	}

	// Check MultiNetworkPolicy
	errs = append(errs, isMultiNetworkpolicyChangeSafe(prev, next)...)

	// Check kube-proxy
	errs = append(errs, isKubeProxyChangeSafe(prev, next)...)

	if len(errs) > 0 {
		return errors.Errorf("invalid configuration: %v", errs)
	}
	return nil
}

// NeedMTUProbe returns true if we need to probe the cluster's MTU.
// We need this if we don't have an MTU configured, either directly, or previously
// to "carry forward". If not, we'll have to probe it.
func NeedMTUProbe(prev, next *operv1.NetworkSpec) bool {
	needsMTU := func(c *operv1.NetworkSpec) bool {
		if c == nil {
			return true
		}
		d := c.DefaultNetwork
		if d.Type == operv1.NetworkTypeOVNKubernetes {
			return d.OVNKubernetesConfig == nil || d.OVNKubernetesConfig.MTU == nil || *d.OVNKubernetesConfig.MTU == 0
		}
		// other network types don't need MTU
		return false
	}
	return needsMTU(prev) && needsMTU(next)
}

func isNetworkChangeSafe(prev, next *operv1.NetworkSpec, infraRes *bootstrap.InfraStatus) error {
	// Forbid changing service network during a migration
	if prev.Migration != nil {
		if !reflect.DeepEqual(prev.ServiceNetwork, next.ServiceNetwork) {
			return errors.Errorf("cannot change ServiceNetwork during migration")
		}
		return nil
	}

	if reflect.DeepEqual(prev.ClusterNetwork, next.ClusterNetwork) && reflect.DeepEqual(prev.ServiceNetwork, next.ServiceNetwork) {
		return nil
	}

	// Currently the only changes we allow are:
	//   -  switching to/from dual-stack.
	//   -  ClusterNetwork modification (check isClusterNetworkChangeSafe()) for what is supported

	// validateIPPools() will have ensured that each config is independently either
	// a valid single-stack config or a valid dual-stack config. Make sure we have
	// one of each.
	var singleStack, dualStack *operv1.NetworkSpec
	switch {
	case len(prev.ServiceNetwork) < len(next.ServiceNetwork):
		// Going from single to dual
		singleStack, dualStack = prev, next
	case len(prev.ServiceNetwork) > len(next.ServiceNetwork):
		// Going from dual to single
		dualStack, singleStack = prev, next
	case !reflect.DeepEqual(prev.ServiceNetwork, next.ServiceNetwork):
		// If the ServiceNetwork has changed, but it's not part of a single<->dual stack migration
		// then we do not support
		return errors.Errorf("unsupported change to ServiceNetwork")
	default:
		// this is not a single/dual stack migration; check if the clusterNetwork change is ok
		return isClusterNetworkChangeSafe(prev, next)
	}

	// Check if migration to DualStack is prohibited
	if len(prev.ServiceNetwork) < len(next.ServiceNetwork) {
		if !isConversionToDualStackSupported(infraRes.PlatformType) {
			return errors.Errorf("%s does not allow conversion to dual-stack cluster", infraRes.PlatformType)
		}
	}

	// Validate that the shared ServiceNetwork entry is unchanged. (validateIPPools
	// already checked that dualStack.ServiceNetwork[0] and [1] are of opposite IP
	// families so we don't need to check that here.)
	if singleStack.ServiceNetwork[0] != dualStack.ServiceNetwork[0] {
		// User changed the primary service network, or tried to swap the order of
		// the primary and secondary networks.
		return errors.Errorf("cannot change primary ServiceNetwork when migrating to/from dual-stack")
	}

	// Validate that the shared ClusterNetwork entries are unchanged, and that ALL of
	// the new ones in dualStack are of the opposite IP family from the shared ones.
	// (ie, you cannot go from [ipv4] to [ipv4, ipv6, ipv4], even though the latter
	// would have been valid as a new install.)
	EntryZeroIsIPv6 := utilnet.IsIPv6CIDRString(singleStack.ClusterNetwork[0].CIDR)
	for i := range dualStack.ClusterNetwork {
		if i < len(singleStack.ClusterNetwork) {
			if !reflect.DeepEqual(singleStack.ClusterNetwork[i], dualStack.ClusterNetwork[i]) {
				// Changed or re-ordered an existing ClusterNetwork element
				return errors.Errorf("cannot change primary ClusterNetwork when migrating to/from dual-stack")
			}
		} else if utilnet.IsIPv6CIDRString(dualStack.ClusterNetwork[i].CIDR) == EntryZeroIsIPv6 {
			// Added a new element of the existing IP family
			return errors.Errorf("cannot add additional ClusterNetwork values of original IP family when migrating to dual stack")
		}
	}

	return nil
}

func isClusterNetworkChangeSafe(prev, next *operv1.NetworkSpec) error {

	// quick check to make sure clusterNetwork slices are of same size as we do not
	// support adding/removing additional clusterNetwork entries unless it's for a
	// single/dual stack migration. in those cases validation is done in isNetworkChangeSafe()
	if len(prev.ClusterNetwork) != len(next.ClusterNetwork) {
		return errors.Errorf("adding/removing clusterNetwork entries of the same type is not supported")
	}

	// Only support changing ClusterNetwork CIDR if it's OVNK
	if next.DefaultNetwork.Type != operv1.NetworkTypeOVNKubernetes {
		return errors.Errorf("network type is %v. changing clusterNetwork entries is only supported for OVNKubernetes", next.DefaultNetwork.Type)
	}

	// sort prev and next just in case there was some re-ordering of the slice, since we
	// want to know for sure we are comparing the same elements in each below
	sort.SliceStable(prev.ClusterNetwork, func(i, j int) bool {
		return prev.ClusterNetwork[i].CIDR < prev.ClusterNetwork[j].CIDR
	})
	sort.SliceStable(next.ClusterNetwork, func(i, j int) bool {
		return next.ClusterNetwork[i].CIDR < next.ClusterNetwork[j].CIDR
	})

	// since we do not allow the clusterNetwork[] size to change, it should be safe to compare
	// prev[i] to next[i] in this validation
	for i, e := range prev.ClusterNetwork {
		prevIp, prevMask, err := net.ParseCIDR(e.CIDR)
		if err != nil {
			return errors.Errorf("error parsing CIDR from ClusterNetwork entry %s: %v", e.CIDR, err)
		}
		nextIp, nextMask, err := net.ParseCIDR(next.ClusterNetwork[i].CIDR)
		if err != nil {
			return errors.Errorf("error parsing CIDR from ClusterNetwork entry %s: %v", next.ClusterNetwork[i].CIDR, err)
		}
		prevHostPrefix := e.HostPrefix
		nextHostPrefix := next.ClusterNetwork[i].HostPrefix

		// changing hostPrefix is not allowed
		if prevHostPrefix != nextHostPrefix {
			return errors.Errorf("modifying a clusterNetwork's hostPrefix value is unsupported")
		}

		if !prevIp.Equal(nextIp) {
			return errors.Errorf("modifying IP network value for clusterNetwork CIDR is unsupported")
		}

		prevMaskSize, _ := prevMask.Mask.Size()
		nextMaskSize, _ := nextMask.Mask.Size()
		if prevMaskSize < nextMaskSize {
			return errors.Errorf("reducing IP range with a larger CIDR mask for clusterNetwork CIDR is unsupported")
		}
	}
	return nil
}

// validateIPPools checks that all IP addresses are valid
// TODO: check for overlap
func validateIPPools(conf *operv1.NetworkSpec) []error {
	errs := []error{}

	// Check all networks for overlaps
	pool := iputil.IPPool{}

	var ipv4Service, ipv6Service, ipv4Cluster, ipv6Cluster bool

	// Validate ServiceNetwork values
	for _, snet := range conf.ServiceNetwork {
		_, cidr, err := net.ParseCIDR(snet)
		if err != nil {
			errs = append(errs, errors.Wrapf(err, "could not parse spec.serviceNetwork %s", snet))
			continue
		}
		if utilnet.IsIPv6CIDR(cidr) {
			ipv6Service = true
		} else {
			ipv4Service = true
		}
		if err := pool.Add(*cidr); err != nil {
			errs = append(errs, errors.Errorf("Whole or subset of ServiceNetwork CIDR %s is already in use: %s", snet, err))
		}
	}

	// Validate count / dual-stack-ness
	if len(conf.ServiceNetwork) == 0 {
		errs = append(errs, errors.Errorf("spec.serviceNetwork must have at least 1 entry"))
	} else if len(conf.ServiceNetwork) == 2 && !(ipv4Service && ipv6Service) {
		errs = append(errs, errors.Errorf("spec.serviceNetwork must contain at most one IPv4 and one IPv6 network"))
	} else if len(conf.ServiceNetwork) > 2 {
		errs = append(errs, errors.Errorf("spec.serviceNetwork must contain at most one IPv4 and one IPv6 network"))
	}

	// validate clusternetwork
	// - has an entry
	// - it is a valid ip
	// - has a reasonable cidr
	// - they do not overlap and do not overlap with the service cidr
	for _, cnet := range conf.ClusterNetwork {
		_, cidr, err := net.ParseCIDR(cnet.CIDR)
		if err != nil {
			errs = append(errs, errors.Errorf("could not parse spec.clusterNetwork %s", cnet.CIDR))
			continue
		}
		if utilnet.IsIPv6CIDR(cidr) {
			ipv6Cluster = true
		} else {
			ipv4Cluster = true
		}
		// ignore hostPrefix if the plugin does not use it and has it unset
		if pluginsUsingHostPrefix.Has(string(conf.DefaultNetwork.Type)) || (cnet.HostPrefix != 0) {
			ones, bits := cidr.Mask.Size()
			// The comparison is inverted; smaller number is larger block
			if cnet.HostPrefix < uint32(ones) {
				errs = append(errs, errors.Errorf("hostPrefix %d is larger than its cidr %s",
					cnet.HostPrefix, cnet.CIDR))
			}
			if int(cnet.HostPrefix) > bits-2 {
				errs = append(errs, errors.Errorf("hostPrefix %d is too small, must be a /%d or larger",
					cnet.HostPrefix, bits-2))
			}
		}
		if err := pool.Add(*cidr); err != nil {
			errs = append(errs, errors.Errorf("Whole or subset of ClusterNetwork CIDR %s is already in use: %s", cnet.CIDR, err))
		}
	}

	if len(conf.ClusterNetwork) < 1 {
		errs = append(errs, errors.Errorf("spec.clusterNetwork must have at least 1 entry"))
	}
	if len(errs) == 0 && (ipv4Cluster != ipv4Service || ipv6Cluster != ipv6Service) {
		errs = append(errs, errors.Errorf("spec.clusterNetwork and spec.serviceNetwork must either both be IPv4-only, both be IPv6-only, or both be dual-stack"))
	}

	return errs
}

// validateMultus validates the combination of DisableMultiNetwork and AddtionalNetworks
func validateMultus(conf *operv1.NetworkSpec) []error {
	// DisableMultiNetwork defaults to false
	deployMultus := true
	if conf.DisableMultiNetwork != nil && *conf.DisableMultiNetwork {
		deployMultus = false
	}

	// Additional Networks are useless without Multus, so don't let them
	// exist without Multus and confuse things (for now)
	if !deployMultus && len(conf.AdditionalNetworks) > 0 {
		return []error{errors.Errorf("additional networks cannot be specified without deploying Multus")}
	}
	return []error{}
}

// validateDefaultNetwork validates whichever network is specified
// as the default network.
func validateDefaultNetwork(conf *operv1.NetworkSpec) []error {
	if conf.DefaultNetwork.Type == operv1.NetworkTypeOVNKubernetes {
		return validateOVNKubernetes(conf)
	}
	return nil
}

// validateMigration validates if migration path is possible
func validateMigration(conf *operv1.NetworkSpec) []error {
	var errs []error

	if conf.Migration != nil {
		if conf.Migration.NetworkType != "" {
			errs = append(errs, errors.Errorf("network type migration is not supported"))
		}
		if conf.Migration.Features != nil {
			errs = append(errs, errors.Errorf("network feature migration is not supported"))
		}
	}
	return errs
}

// renderDefaultNetwork generates the manifests corresponding to the requested
// default network
func renderDefaultNetwork(conf *operv1.NetworkSpec, bootstrapResult *bootstrap.BootstrapResult, manifestDir string,
	client cnoclient.Client, featureGates featuregates.FeatureGate) ([]*uns.Unstructured, bool, error) {
	dn := conf.DefaultNetwork
	if errs := validateDefaultNetwork(conf); len(errs) > 0 {
		return nil, false, errors.Errorf("invalid Default Network configuration: %v", errs)
	}

	if dn.Type == operv1.NetworkTypeOVNKubernetes {
		return renderOVNKubernetes(conf, bootstrapResult, manifestDir, client, featureGates)
	}

	log.Printf("NOTICE: Unknown network type %s, ignoring", dn.Type)
	return nil, false, nil
}

func fillDefaultNetworkDefaults(conf, previous *operv1.NetworkSpec, hostMTU int) {
	if conf.DefaultNetwork.Type == operv1.NetworkTypeOVNKubernetes {
		fillOVNKubernetesDefaults(conf, previous, hostMTU)
	}
}

func isDefaultNetworkChangeSafe(prev, next *operv1.NetworkSpec) []error {
	if prev.DefaultNetwork.Type != next.DefaultNetwork.Type {
		return []error{errors.Errorf("cannot change default network type")}
	}

	if prev.DefaultNetwork.Type == operv1.NetworkTypeOVNKubernetes {
		return isOVNKubernetesChangeSafe(prev, next)
	}
	return nil
}

// ValidateAdditionalNetworks validates additional networks configs
func validateAdditionalNetworks(conf *operv1.NetworkSpec) []error {
	out := []error{}
	ans := conf.AdditionalNetworks
	for _, an := range ans {
		switch an.Type {
		case operv1.NetworkTypeRaw:
			if errs := validateRaw(&an); len(errs) > 0 {
				out = append(out, errs...)
			}
		case operv1.NetworkTypeSimpleMacvlan:
			if errs := validateSimpleMacvlanConfig(&an); len(errs) > 0 {
				out = append(out, errs...)
			}
		default:
			out = append(out, errors.Errorf("unknown or unsupported NetworkType: %s", an.Type))
		}
	}
	return out
}

// renderAdditionalNetworks generates the manifests of the requested additional networks
func renderAdditionalNetworks(conf *operv1.NetworkSpec, manifestDir string) ([]*uns.Unstructured, error) {
	ans := conf.AdditionalNetworks
	out := []*uns.Unstructured{}

	// validate additional network configuration
	if errs := validateAdditionalNetworks(conf); len(errs) > 0 {
		return nil, errors.Errorf("invalid Additional Network Configuration: %v", errs)
	}

	if len(ans) == 0 {
		return nil, nil
	}

	// render additional network configuration
	for _, an := range ans {
		switch an.Type {
		case operv1.NetworkTypeRaw:
			objs, err := renderRawCNIConfig(&an, manifestDir)
			if err != nil {
				return nil, err
			}
			out = append(out, objs...)
		case operv1.NetworkTypeSimpleMacvlan:
			objs, err := renderSimpleMacvlanConfig(&an, manifestDir)
			if err != nil {
				return nil, err
			}
			out = append(out, objs...)
		default:
			return nil, errors.Errorf("unknown or unsupported NetworkType: %s", an.Type)
		}
	}

	return out, nil
}

func getMultusAdmissionControllerReplicas(bootstrapResult *bootstrap.BootstrapResult, hyperShiftEnabled bool) int {
	replicas := 2
	if bootstrapResult.Infra.ControlPlaneTopology == configv1.ExternalTopologyMode {
		// In HyperShift check HostedControlPlane.ControllerAvailabilityPolicy, otherwise rely on Infra.InfrastructureTopology
		if hyperShiftEnabled {
			if bootstrapResult.Infra.HostedControlPlane.ControllerAvailabilityPolicy == hypershift.SingleReplica {
				replicas = 1
			}
		} else if bootstrapResult.Infra.InfrastructureTopology == configv1.SingleReplicaTopologyMode {
			replicas = 1
		}
	} else if bootstrapResult.Infra.ControlPlaneTopology == configv1.SingleReplicaTopologyMode {
		replicas = 1
	}

	return replicas
}

// renderMultusAdmissionController generates the manifests of Multus Admission Controller
func renderMultusAdmissionController(conf *operv1.NetworkSpec, manifestDir string, externalControlPlane bool, bootstrapResult *bootstrap.BootstrapResult, client cnoclient.Client, featureGates featuregates.FeatureGate) ([]*uns.Unstructured, error) {
	if *conf.DisableMultiNetwork {
		return nil, nil
	}

	var err error
	out := []*uns.Unstructured{}

	hsc := hypershift.NewHyperShiftConfig()
	objs, err := renderMultusAdmissonControllerConfig(manifestDir, externalControlPlane,
		bootstrapResult, client, hsc, names.ManagementClusterName, featureGates)
	if err != nil {
		return nil, err
	}
	out = append(out, objs...)
	return out, nil
}

// renderMultiNetworkpolicy generates the manifests of MultiNetworkPolicy
func renderMultiNetworkpolicy(conf *operv1.NetworkSpec, manifestDir string) ([]*uns.Unstructured, error) {
	// disable it if DisableMultiNetwork = true
	if *conf.DisableMultiNetwork {
		return nil, nil
	}

	if conf.UseMultiNetworkPolicy == nil || !*conf.UseMultiNetworkPolicy {
		return nil, nil
	}

	var err error
	out := []*uns.Unstructured{}

	objs, err := renderMultiNetworkpolicyConfig(manifestDir)
	if err != nil {
		return nil, err
	}
	out = append(out, objs...)
	return out, nil
}

// renderNetworkDiagnostics renders the connectivity checks
func renderNetworkDiagnostics(operConf *operv1.NetworkSpec, clusterConf *configv1.NetworkSpec, manifestDir string) ([]*uns.Unstructured, error) {
	// network diagnostics feature is disabled when clusterConf.NetworkDiagnostics.Mode is set to "Disabled"
	// or when clusterConf.NetworkDiagnostics is empty and the legacy operConf.DisableNetworkDiagnostics is true
	if clusterConf.NetworkDiagnostics.Mode == configv1.NetworkDiagnosticsDisabled ||
		reflect.DeepEqual(clusterConf.NetworkDiagnostics, configv1.NetworkDiagnostics{}) && operConf.DisableNetworkDiagnostics {
		return nil, nil
	}

	data := render.MakeRenderData()
	data.Data["ReleaseVersion"] = os.Getenv("RELEASE_VERSION")
	data.Data["NetworkCheckSourceImage"] = os.Getenv("NETWORK_CHECK_SOURCE_IMAGE")
	data.Data["NetworkCheckTargetImage"] = os.Getenv("NETWORK_CHECK_TARGET_IMAGE")
	defaultNodeSelector := map[string]string{"kubernetes.io/os": "linux"}
	data.Data["NetworkCheckSourceNodeSelector"] = defaultNodeSelector
	if clusterConf.NetworkDiagnostics.SourcePlacement.NodeSelector != nil {
		data.Data["NetworkCheckSourceNodeSelector"] = clusterConf.NetworkDiagnostics.SourcePlacement.NodeSelector
	}
	data.Data["NetworkCheckTargetNodeSelector"] = defaultNodeSelector
	if clusterConf.NetworkDiagnostics.TargetPlacement.NodeSelector != nil {
		data.Data["NetworkCheckTargetNodeSelector"] = clusterConf.NetworkDiagnostics.TargetPlacement.NodeSelector
	}
	data.Data["NetworkCheckSourceTolerations"] = clusterConf.NetworkDiagnostics.SourcePlacement.Tolerations
	data.Data["NetworkCheckTargetTolerations"] = []corev1.Toleration{{Operator: corev1.TolerationOpExists}}
	if clusterConf.NetworkDiagnostics.TargetPlacement.Tolerations != nil {
		data.Data["NetworkCheckTargetTolerations"] = clusterConf.NetworkDiagnostics.TargetPlacement.Tolerations
	}
	manifests, err := render.RenderDir(filepath.Join(manifestDir, "network-diagnostics"), &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render network-diagnostics manifests")
	}

	return manifests, nil
}

// renderNetworkPublic renders the common objects related to the openshift-network-features configmap
func renderNetworkPublic(manifestDir string) ([]*uns.Unstructured, error) {
	data := render.MakeRenderData()

	manifests, err := render.RenderDir(filepath.Join(manifestDir, "network", "public"), &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render network/public manifests")
	}
	return manifests, nil
}

// renderCNO renders the common objects in the cluster-network-operator directory
func renderCNO(manifestDir string) ([]*uns.Unstructured, error) {
	data := render.MakeRenderData()

	manifests, err := render.RenderDir(filepath.Join(manifestDir, "cluster-network-operator"), &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render cluster-network-operator manifests")
	}
	return manifests, nil
}

// renderIPTablesAlerter generates the manifests for the pod iptables usage alerter
func renderIPTablesAlerter(conf *operv1.NetworkSpec, bootstrapResult *bootstrap.BootstrapResult, manifestDir string) ([]*uns.Unstructured, error) {
	if !bootstrapResult.IPTablesAlerter.Enabled {
		return nil, nil
	}

	data := render.MakeRenderData()
	data.Data["ReleaseVersion"] = os.Getenv("RELEASE_VERSION")
	data.Data["CLIImage"] = os.Getenv("CLI_IMAGE")

	manifests, err := render.RenderDir(filepath.Join(manifestDir, "network", "iptables-alerter"), &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render network/iptables-alerter manifests")
	}
	return manifests, nil
}

// renderNetworkingConsolePlugin renders the common objects related to the networking console plugin resources
func renderNetworkingConsolePlugin(manifestDir string, bootstrapResult *bootstrap.BootstrapResult) ([]*uns.Unstructured, error) {
	if !bootstrapResult.Infra.ConsolePluginCRDExists {
		log.Printf("consoleplugins.console.openshift.io CRD does not exist yet")
		return nil, nil
	}

	data := render.MakeRenderData()
	data.Data["ReleaseVersion"] = os.Getenv("RELEASE_VERSION")

	replicas := int32(2)
	if bootstrapResult.Infra.InfrastructureTopology == configv1.SingleReplicaTopologyMode {
		// the plugin pod(s) are running on worker nodes. in case of SingleReplica of
		// Infrastructure Topology, we use one replica for the networking console plugin.
		replicas = int32(1)
	}
	data.Data["Replicas"] = replicas

	consolePluginImage, ok := os.LookupEnv("NETWORKING_CONSOLE_PLUGIN_IMAGE")
	if !ok {
		return nil, errors.Errorf("Could not get NETWORKING_CONSOLE_PLUGIN_IMAGE env var")
	}
	data.Data["NetworkingConsolePluginImage"] = consolePluginImage

	manifests, err := render.RenderDir(filepath.Join(manifestDir, "networking-console-plugin"), &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render networking-console-plugin manifests")
	}

	return manifests, nil
}

// registerNetworkingConsolePlugin enables console plugin for networking-console if not already enabled
func registerNetworkingConsolePlugin(bootstrapResult *bootstrap.BootstrapResult, cl cnoclient.Client) error {
	if !bootstrapResult.Infra.ConsolePluginCRDExists {
		return nil
	}

	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		console, err := cl.ClientFor("").OpenshiftOperatorClient().OperatorV1().Consoles().Get(context.TODO(), "cluster", metav1.GetOptions{})
		if err != nil {
			return errors.Wrap(err, "Failed to get Console Operator resource")
		}

		if sets.NewString(console.Spec.Plugins...).Has(pluginName) {
			return nil
		}
		console.Spec.Plugins = append(console.Spec.Plugins, pluginName)

		_, err = cl.Default().OpenshiftOperatorClient().OperatorV1().Consoles().Update(context.TODO(), console, metav1.UpdateOptions{})
		return err
	})
}

func renderAdditionalRoutingCapabilities(conf *operv1.NetworkSpec, manifestDir string) ([]*uns.Unstructured, error) {
	if conf == nil || conf.AdditionalRoutingCapabilities == nil {
		return nil, nil
	}
	var out []*uns.Unstructured
	for _, provider := range conf.AdditionalRoutingCapabilities.Providers {
		switch provider {
		case operv1.RoutingCapabilitiesProviderFRR:
			data := render.MakeRenderData()
			data.Data["FRRK8sImage"] = os.Getenv("FRR_K8S_IMAGE")
			data.Data["KubeRBACProxyImage"] = os.Getenv("KUBE_RBAC_PROXY_IMAGE")
			data.Data["ReleaseVersion"] = os.Getenv("RELEASE_VERSION")
			objs, err := render.RenderDir(filepath.Join(manifestDir, "network/frr-k8s"), &data)
			if err != nil {
				return nil, fmt.Errorf("failed to render frr-k8s manifests: %w", err)
			}
			out = append(out, objs...)
		}
	}

	return out, nil
}

// isSupportedDualStackPlatform returns true if the platform supports dual-stack networking
// on Day-0 (initial cluster installation). Some platforms (AWS, Azure) require feature gates
// to be enabled to support dual-stack.
func isSupportedDualStackPlatform(platformType configv1.PlatformType, featureGates featuregates.FeatureGate) bool {
	switch platformType {
	case configv1.BareMetalPlatformType, configv1.NonePlatformType, configv1.VSpherePlatformType, configv1.OpenStackPlatformType, configv1.KubevirtPlatformType:
		return true
	case configv1.AWSPlatformType:
		return featureGates.Enabled(apifeatures.FeatureGateAWSDualStackInstall)
	case configv1.AzurePlatformType:
		return featureGates.Enabled(apifeatures.FeatureGateAzureDualStackInstall)
	default:
		return false
	}
}

// isConversionToDualStackSupported returns true if the platform supports converting
// from single-stack to dual-stack on Day-2 (after initial installation). This is a
// subset of platforms that support dual-stack on Day-0.
func isConversionToDualStackSupported(platformType configv1.PlatformType) bool {
	switch platformType {
	case configv1.BareMetalPlatformType, configv1.NonePlatformType, configv1.VSpherePlatformType:
		return true
	default:
		return false
	}
}
