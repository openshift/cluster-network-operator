package network

import (
	"log"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/pkg/errors"

	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"github.com/openshift/cluster-network-operator/pkg/render"
	iputil "github.com/openshift/cluster-network-operator/pkg/util/ip"

	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilnet "k8s.io/utils/net"
)

func Render(conf *operv1.NetworkSpec, bootstrapResult *bootstrap.BootstrapResult, manifestDir string) ([]*uns.Unstructured, error) {
	log.Printf("Starting render phase")
	objs := []*uns.Unstructured{}

	// render cloud network config controller **before** the network plugin.
	// the network plugin is dependent upon having the cloud network CRD
	// defined as to initialize its watcher, otherwise it will error and crash
	o, err := renderCloudNetworkConfigController(conf, bootstrapResult.Infra, manifestDir)
	if err != nil {
		return nil, err
	}
	objs = append(objs, o...)

	// render Multus
	o, err = renderMultus(conf, manifestDir)
	if err != nil {
		return nil, err
	}
	objs = append(objs, o...)

	// render MultusAdmissionController
	o, err = renderMultusAdmissionController(conf, manifestDir, bootstrapResult.Infra.ExternalControlPlane)
	if err != nil {
		return nil, err
	}
	objs = append(objs, o...)

	// render MultiNetworkPolicy
	o, err = renderMultiNetworkpolicy(conf, manifestDir)
	if err != nil {
		return nil, err
	}
	objs = append(objs, o...)

	// render default network
	o, err = renderDefaultNetwork(conf, bootstrapResult, manifestDir)
	if err != nil {
		return nil, err
	}
	objs = append(objs, o...)

	// render kube-proxy
	// DPU_DEV_PREVIEW
	// There is currently a restriction that renderStandaloneKubeProxy() is
	// called after renderDefaultNetwork(). The OVN-Kubernetes code is enabling
	// KubeProxy in Node Mode of "dpu". Once out of DevPreview, CNO API will be
	// expanded to include Node Mode and it will be stored in conf (operv1.NetworkSpec)
	// and KubeProxy can read Node Mode and be enabled in KubeProxy code, removing this
	// dependency.
	o, err = renderStandaloneKubeProxy(conf, bootstrapResult, manifestDir)
	if err != nil {
		return nil, err
	}
	objs = append(objs, o...)

	// render additional networks
	o, err = renderAdditionalNetworks(conf, manifestDir)
	if err != nil {
		return nil, err
	}
	objs = append(objs, o...)

	// render network diagnostics
	o, err = renderNetworkDiagnostics(conf, manifestDir)
	if err != nil {
		return nil, err
	}
	objs = append(objs, o...)

	o, err = renderNetworkPublic(manifestDir)
	if err != nil {
		return nil, err
	}
	objs = append(objs, o...)

	log.Printf("Render phase done, rendered %d objects", len(objs))
	return objs, nil
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
//      *** DO NOT ADD ANY NEW CANONICALIZATION TO THIS FUNCTION! ***
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
func FillDefaults(conf, previous *operv1.NetworkSpec) {
	hostMTU, err := getDefaultMTU()
	if hostMTU == 0 {
		hostMTU = 1500
	}
	if previous == nil { // host mtu isn't used in subsequent runs, elide these logs
		if err != nil {
			log.Printf("Failed MTU probe, falling back to 1500: %v", err)
		} else {
			log.Printf("Detected uplink MTU %d", hostMTU)
		}
	}
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
func IsChangeSafe(prev, next *operv1.NetworkSpec) error {
	if prev == nil {
		return nil
	}

	// Easy way out: nothing changed.
	if reflect.DeepEqual(prev, next) {
		return nil
	}

	errs := []error{}

	// Most ClusterNetworks/ServiceNetwork changes are not allowed
	if err := isNetworkChangeSafe(prev, next); err != nil {
		errs = append(errs, err)
	}

	// Check the network migration
	errs = append(errs, isMigrationChangeSafe(prev, next)...)

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

func isNetworkChangeSafe(prev, next *operv1.NetworkSpec) error {
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

	// Currently the only change we allow is switching to/from dual-stack.
	//
	// FIXME: the errors here currently do not actually mention dual-stack since it's
	// not supported yet.

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
	default:
		// They didn't change single-vs-dual
		if reflect.DeepEqual(prev.ServiceNetwork, next.ServiceNetwork) {
			return errors.Errorf("cannot change ClusterNetwork")
		} else {
			return errors.Errorf("cannot change ServiceNetwork")
		}
	}

	// Validate that the shared ServiceNetwork entry is unchanged. (validateIPPools
	// already checked that dualStack.ServiceNetwork[0] and [1] are of opposite IP
	// families so we don't need to check that here.)
	if singleStack.ServiceNetwork[0] != dualStack.ServiceNetwork[0] {
		// User changed the primary service network, or tried to swap the order of
		// the primary and secondary networks.
		return errors.Errorf("cannot change ServiceNetwork")
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
				return errors.Errorf("cannot change ClusterNetwork")
			}
		} else if utilnet.IsIPv6CIDRString(dualStack.ClusterNetwork[i].CIDR) == EntryZeroIsIPv6 {
			// Added a new element of the existing IP family
			return errors.Errorf("cannot change ClusterNetwork")
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
			errs = append(errs, err)
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
			errs = append(errs, err)
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
	case operv1.NetworkTypeKuryr:
		return validateKuryr(conf)
	default:
		return nil
	}
}

// renderDefaultNetwork generates the manifests corresponding to the requested
// default network
func renderDefaultNetwork(conf *operv1.NetworkSpec, bootstrapResult *bootstrap.BootstrapResult, manifestDir string) ([]*uns.Unstructured, error) {
	dn := conf.DefaultNetwork
	if errs := validateDefaultNetwork(conf); len(errs) > 0 {
		return nil, errors.Errorf("invalid Default Network configuration: %v", errs)
	}

	switch dn.Type {
	case operv1.NetworkTypeOpenShiftSDN:
		return renderOpenShiftSDN(conf, bootstrapResult, manifestDir)
	case operv1.NetworkTypeOVNKubernetes:
		return renderOVNKubernetes(conf, bootstrapResult, manifestDir)
	case operv1.NetworkTypeKuryr:
		return renderKuryr(conf, bootstrapResult, manifestDir)
	default:
		log.Printf("NOTICE: Unknown network type %s, ignoring", dn.Type)
		return nil, nil
	}
}

func fillDefaultNetworkDefaults(conf, previous *operv1.NetworkSpec, hostMTU int) {
	switch conf.DefaultNetwork.Type {
	case operv1.NetworkTypeOpenShiftSDN:
		fillOpenShiftSDNDefaults(conf, previous, hostMTU)
	case operv1.NetworkTypeOVNKubernetes:
		fillOVNKubernetesDefaults(conf, previous, hostMTU)
	case operv1.NetworkTypeKuryr:
		fillKuryrDefaults(conf, previous)
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
		case operv1.NetworkTypeKuryr:
			return isKuryrChangeSafe(prev, next)
		default:
			return nil
		}
	}
	return nil
}

func isMigrationChangeSafe(prev, next *operv1.NetworkSpec) []error {
	if prev.Migration != nil && next.Migration != nil && prev.Migration.NetworkType != next.Migration.NetworkType {
		return []error{errors.Errorf("cannot change migration network type after migration is start")}
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

// renderMultusAdmissionController generates the manifests of Multus Admission Controller
func renderMultusAdmissionController(conf *operv1.NetworkSpec, manifestDir string, externalControlPlane bool) ([]*uns.Unstructured, error) {
	if *conf.DisableMultiNetwork {
		return nil, nil
	}

	var err error
	out := []*uns.Unstructured{}

	objs, err := renderMultusAdmissonControllerConfig(manifestDir, externalControlPlane)
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
