package network

import (
	"log"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/pkg/errors"

	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/hypershift"
	"github.com/openshift/cluster-network-operator/pkg/render"
	iputil "github.com/openshift/cluster-network-operator/pkg/util/ip"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"

	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/sets"
	utilnet "k8s.io/utils/net"
)

var dualStackPlatforms = sets.NewString(
	string(configv1.BareMetalPlatformType),
	string(configv1.NonePlatformType),
	string(configv1.VSpherePlatformType),
	string(configv1.OpenStackPlatformType),
)

func Render(conf *operv1.NetworkSpec, bootstrapResult *bootstrap.BootstrapResult, manifestDir string, client cnoclient.Client,
	featureGates featuregates.FeatureGate) ([]*uns.Unstructured, bool, error) {
	log.Printf("Starting render phase")
	var progressing bool
	objs := []*uns.Unstructured{}

	// render cloud network config controller **before** the network plugin.
	// the network plugin is dependent upon having the cloud network CRD
	// defined as to initialize its watcher, otherwise it will error and crash
	o, err := renderCloudNetworkConfigController(conf, bootstrapResult, manifestDir)
	if err != nil {
		return nil, progressing, err
	}
	objs = append(objs, o...)

	// render Multus
	o, err = renderMultus(conf, bootstrapResult, manifestDir)
	if err != nil {
		return nil, progressing, err
	}
	objs = append(objs, o...)

	// render MultusAdmissionController
	o, err = renderMultusAdmissionController(conf, manifestDir,
		bootstrapResult.Infra.ControlPlaneTopology == configv1.ExternalTopologyMode, bootstrapResult, client)
	if err != nil {
		return nil, progressing, err
	}
	objs = append(objs, o...)

	// render MultiNetworkPolicy
	o, err = renderMultiNetworkpolicy(conf, manifestDir)
	if err != nil {
		return nil, progressing, err
	}
	objs = append(objs, o...)

	// render default network
	o, progressing, err = renderDefaultNetwork(conf, bootstrapResult, manifestDir, client, featureGates)
	if err != nil {
		return nil, progressing, err
	}
	objs = append(objs, o...)

	if conf.Migration != nil && conf.Migration.NetworkType != "" {
		// During SDN Migration, CNO needs to convert the custom resources of
		// egressIP, egressFirewall, etc. Therefore we need to render the CRDs for
		// both OpenShiftSDN and OVNKubernetes.
		o, err = renderCRDForMigration(conf, manifestDir, featureGates)
		if err != nil {
			return nil, progressing, err
		}
		objs = append(objs, o...)
	}
	// render kube-proxy
	// DPU_DEV_PREVIEW
	// There is currently a restriction that renderStandaloneKubeProxy() is
	// called after renderDefaultNetwork(). The OVN-Kubernetes code is enabling
	// KubeProxy in Node Mode of "dpu".
	o, err = renderStandaloneKubeProxy(conf, bootstrapResult, manifestDir)
	if err != nil {
		return nil, progressing, err
	}
	objs = append(objs, o...)

	// render additional networks
	o, err = renderAdditionalNetworks(conf, manifestDir)
	if err != nil {
		return nil, progressing, err
	}
	objs = append(objs, o...)

	// render network diagnostics
	o, err = renderNetworkDiagnostics(conf, manifestDir)
	if err != nil {
		return nil, progressing, err
	}
	objs = append(objs, o...)

	o, err = renderNetworkPublic(manifestDir)
	if err != nil {
		return nil, progressing, err
	}
	objs = append(objs, o...)

	// render
	o, err = renderNetworkNodeIdentity(conf, bootstrapResult, manifestDir, client)
	if err != nil {
		return nil, progressing, err
	}
	objs = append(objs, o...)

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

	switch strings.ToLower(string(conf.DefaultNetwork.Type)) {
	case strings.ToLower(string(operv1.NetworkTypeOpenShiftSDN)):
		conf.DefaultNetwork.Type = operv1.NetworkTypeOpenShiftSDN
	case strings.ToLower(string(operv1.NetworkTypeOVNKubernetes)):
		conf.DefaultNetwork.Type = operv1.NetworkTypeOVNKubernetes
	}

	if conf.DefaultNetwork.Type == operv1.NetworkTypeOpenShiftSDN &&
		conf.DefaultNetwork.OpenShiftSDNConfig != nil {
		sdnc := conf.DefaultNetwork.OpenShiftSDNConfig
		switch strings.ToLower(string(sdnc.Mode)) {
		case strings.ToLower(string(operv1.SDNModeMultitenant)):
			sdnc.Mode = operv1.SDNModeMultitenant
		case strings.ToLower(string(operv1.SDNModeNetworkPolicy)):
			sdnc.Mode = operv1.SDNModeNetworkPolicy
		case strings.ToLower(string(operv1.SDNModeSubnet)):
			sdnc.Mode = operv1.SDNModeSubnet
		}
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
// underlay MTU (for OVN-K and OSDN).
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

	// Check the network migration
	errs = append(errs, isMigrationChangeSafe(prev, next, infraStatus)...)

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
		switch d.Type {
		case operv1.NetworkTypeOVNKubernetes:
			return d.OVNKubernetesConfig == nil || d.OVNKubernetesConfig.MTU == nil || *d.OVNKubernetesConfig.MTU == 0
		case operv1.NetworkTypeOpenShiftSDN:
			return d.OpenShiftSDNConfig == nil || d.OpenShiftSDNConfig.MTU == nil || *d.OpenShiftSDNConfig.MTU == 0
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

	// Validate that this is either a BareMetal or None PlatformType. For all other
	// PlatformTypes, migration to DualStack is prohibited
	if len(prev.ServiceNetwork) < len(next.ServiceNetwork) {
		if !isSupportedDualStackPlatform(infraRes.PlatformType) {
			return errors.Errorf("%s is not one of the supported platforms for dual stack (%s)", infraRes.PlatformType,
				strings.Join(dualStackPlatforms.List(), ", "))
		} else if string(configv1.OpenStackPlatformType) == string(infraRes.PlatformType) {
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
	if !reflect.DeepEqual(next.DefaultNetwork.Type, operv1.NetworkTypeOVNKubernetes) {
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
	switch conf.DefaultNetwork.Type {
	case operv1.NetworkTypeOpenShiftSDN:
		return validateOpenShiftSDN(conf)
	case operv1.NetworkTypeOVNKubernetes:
		return validateOVNKubernetes(conf)
	default:
		return nil
	}
}

// validateMigration validates if migration path is possible
func validateMigration(conf *operv1.NetworkSpec) []error {
	return []error{}
}

// renderDefaultNetwork generates the manifests corresponding to the requested
// default network
func renderDefaultNetwork(conf *operv1.NetworkSpec, bootstrapResult *bootstrap.BootstrapResult, manifestDir string,
	client cnoclient.Client, featureGates featuregates.FeatureGate) ([]*uns.Unstructured, bool, error) {
	dn := conf.DefaultNetwork
	if errs := validateDefaultNetwork(conf); len(errs) > 0 {
		return nil, false, errors.Errorf("invalid Default Network configuration: %v", errs)
	}

	if conf.Migration != nil && conf.Migration.Mode == operv1.LiveNetworkMigrationMode {
		log.Printf("Render both CNIs for live migration")
		ovnObjs, ovnProgressing, err := renderOVNKubernetes(conf, bootstrapResult, manifestDir, client, featureGates)
		if err != nil {
			return nil, false, err
		}
		objs, sdnProgressing, err := renderOpenShiftSDN(conf, bootstrapResult, manifestDir)
		if err != nil {
			return nil, false, err
		}
		return append(objs, ovnObjs...), sdnProgressing || ovnProgressing, nil
	}

	switch dn.Type {
	case operv1.NetworkTypeOpenShiftSDN:
		return renderOpenShiftSDN(conf, bootstrapResult, manifestDir)
	case operv1.NetworkTypeOVNKubernetes:
		return renderOVNKubernetes(conf, bootstrapResult, manifestDir, client, featureGates)
	default:
		log.Printf("NOTICE: Unknown network type %s, ignoring", dn.Type)
		return nil, false, nil
	}
}

// renderCRDForMigration generates OpenShiftSDN CRDs when default network is OVNKubernetes,
// and generates OVNKubernetes CRDs when default network is OpenShiftSDN.
func renderCRDForMigration(conf *operv1.NetworkSpec, manifestDir string, featureGates featuregates.FeatureGate) ([]*uns.Unstructured, error) {
	switch conf.DefaultNetwork.Type {
	case operv1.NetworkTypeOpenShiftSDN:
		// When we migrate from SDN to OVNK, we must set the feature gate values so that
		// the CRD installation can happen according to whether the feature gate is enabled or not
		// in the cluster
		data := render.MakeRenderData()
		data.Data["OVN_ADMIN_NETWORK_POLICY_ENABLE"] = featureGates.Enabled(configv1.FeatureGateAdminNetworkPolicy)
		manifests, err := render.RenderTemplate(filepath.Join(manifestDir, "network/ovn-kubernetes/common/001-crd.yaml"), &data)
		if err != nil {
			return nil, errors.Wrap(err, "failed to render OVNKubernetes CRDs")
		}
		return manifests, err
	case operv1.NetworkTypeOVNKubernetes:
		manifests, err := render.RenderTemplate(filepath.Join(manifestDir, "network/openshift-sdn/001-crd.yaml"), &render.RenderData{})
		if err != nil {
			return nil, errors.Wrap(err, "failed to render OpenShiftSDN CRDs")
		}
		return manifests, err
	default:
		log.Printf("NOTICE: Unsupported network type %s, ignoring", conf.DefaultNetwork.Type)
		return nil, nil
	}
}

func fillDefaultNetworkDefaults(conf, previous *operv1.NetworkSpec, hostMTU int) {
	if conf.Migration != nil && conf.Migration.Mode == operv1.LiveNetworkMigrationMode {
		log.Printf("fill default for both sdn and ovnkube during live migration")
		fillOpenShiftSDNDefaults(conf, previous, hostMTU)
		fillOVNKubernetesDefaults(conf, previous, hostMTU)
		return
	}
	switch conf.DefaultNetwork.Type {
	case operv1.NetworkTypeOpenShiftSDN:
		fillOpenShiftSDNDefaults(conf, previous, hostMTU)
		conf.DefaultNetwork.OVNKubernetesConfig = nil
	case operv1.NetworkTypeOVNKubernetes:
		fillOVNKubernetesDefaults(conf, previous, hostMTU)
		conf.DefaultNetwork.OpenShiftSDNConfig = nil
	default:
	}
}

func isDefaultNetworkChangeSafe(prev, next *operv1.NetworkSpec) []error {

	if prev.DefaultNetwork.Type != next.DefaultNetwork.Type {
		if prev.Migration == nil {
			return []error{errors.Errorf("cannot change default network type when not doing migration")}
		} else {
			if operv1.NetworkType(prev.Migration.NetworkType) != next.DefaultNetwork.Type {
				return []error{errors.Errorf("can only change default network type to the target migration network type")}
			}
		}
	}

	if prev.Migration == nil || prev.Migration.NetworkType == "" {
		switch prev.DefaultNetwork.Type {
		case operv1.NetworkTypeOpenShiftSDN:
			return isOpenShiftSDNChangeSafe(prev, next)
		case operv1.NetworkTypeOVNKubernetes:
			return isOVNKubernetesChangeSafe(prev, next)
		default:
			return nil
		}
	}
	return nil
}

func isMigrationChangeSafe(prev, next *operv1.NetworkSpec, infraStatus *bootstrap.InfraStatus) []error {
	// infra.HostedControlPlane is not nil only when HyperShift is enabled
	if next.Migration != nil && next.Migration.Mode == operv1.LiveNetworkMigrationMode && infraStatus.HostedControlPlane != nil {
		return []error{errors.Errorf("live migration is unsupported in a HyperShift environment")}
	}
	if next.Migration != nil && next.Migration.Mode == operv1.LiveNetworkMigrationMode &&
		!infraStatus.StandaloneManagedCluster {
		return []error{errors.Errorf("live migration is unsupported on a self managed cluster")}
	}
	if prev.Migration != nil && next.Migration != nil && prev.Migration.NetworkType != next.Migration.NetworkType && next.Migration.Mode != operv1.LiveNetworkMigrationMode {
		return []error{errors.Errorf("cannot change migration network type after migration has started")}
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
func renderMultusAdmissionController(conf *operv1.NetworkSpec, manifestDir string, externalControlPlane bool, bootstrapResult *bootstrap.BootstrapResult, client cnoclient.Client) ([]*uns.Unstructured, error) {
	if *conf.DisableMultiNetwork {
		return nil, nil
	}

	var err error
	out := []*uns.Unstructured{}

	objs, err := renderMultusAdmissonControllerConfig(manifestDir, externalControlPlane,
		bootstrapResult, client)
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
func renderNetworkDiagnostics(conf *operv1.NetworkSpec, manifestDir string) ([]*uns.Unstructured, error) {
	if conf.DisableNetworkDiagnostics {
		return nil, nil
	}

	data := render.MakeRenderData()
	data.Data["ReleaseVersion"] = os.Getenv("RELEASE_VERSION")
	data.Data["NetworkCheckSourceImage"] = os.Getenv("NETWORK_CHECK_SOURCE_IMAGE")
	data.Data["NetworkCheckTargetImage"] = os.Getenv("NETWORK_CHECK_TARGET_IMAGE")

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

func isSupportedDualStackPlatform(platformType configv1.PlatformType) bool {
	return dualStackPlatforms.Has(string(platformType))
}
