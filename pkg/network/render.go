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

	// render Multus
	o, err := renderMultus(conf, manifestDir)
	if err != nil {
		return nil, err
	}
	objs = append(objs, o...)

	// render MultusAdmissionController
	o, err = renderMultusAdmissionController(conf, manifestDir)
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
	o, err = renderStandaloneKubeProxy(conf, manifestDir)
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

	// TODO: implement cluster network / service network expansion
	// We don't support cluster network changes
	if !reflect.DeepEqual(prev.ClusterNetwork, next.ClusterNetwork) {
		errs = append(errs, errors.Errorf("cannot change ClusterNetworks"))
	}

	// Nor can you change service network
	if !reflect.DeepEqual(prev.ServiceNetwork, next.ServiceNetwork) {
		errs = append(errs, errors.Errorf("cannot change ServiceNetwork"))
	}

	// Check the default network
	errs = append(errs, isDefaultNetworkChangeSafe(prev, next)...)

	// Changing AdditionalNetworks is supported

	if !reflect.DeepEqual(prev.DisableMultiNetwork, next.DisableMultiNetwork) {
		errs = append(errs, errors.Errorf("cannot change DisableMultiNetwork"))
	}

	// Check kube-proxy
	errs = append(errs, isKubeProxyChangeSafe(prev, next)...)

	if len(errs) > 0 {
		return errors.Errorf("invalid configuration: %v", errs)
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
		return renderOpenShiftSDN(conf, manifestDir)
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
		fillKuryrDefaults(conf)
	default:
	}
}

func isDefaultNetworkChangeSafe(prev, next *operv1.NetworkSpec) []error {
	if prev.DefaultNetwork.Type != next.DefaultNetwork.Type {
		return []error{errors.Errorf("cannot change default network type")}
	}

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
func renderMultusAdmissionController(conf *operv1.NetworkSpec, manifestDir string) ([]*uns.Unstructured, error) {
	if *conf.DisableMultiNetwork {
		return nil, nil
	}

	var err error
	out := []*uns.Unstructured{}

	objs, err := renderMultusAdmissonControllerConfig(manifestDir)
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
