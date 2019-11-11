package network

import (
	"log"
	"net"
	"reflect"
	"strings"

	"github.com/pkg/errors"

	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"

	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func Render(conf *operv1.NetworkSpec, bootstrapResult *bootstrap.BootstrapResult, manifestDir string) ([]*uns.Unstructured, error) {
	log.Printf("Starting render phase")
	objs := []*uns.Unstructured{}

	// render Multus
	o, err := RenderMultus(conf, manifestDir)
	if err != nil {
		return nil, err
	}
	objs = append(objs, o...)

	// render MultusAdmissionController
	o, err = RenderMultusAdmissionController(conf, manifestDir)
	if err != nil {
		return nil, err
	}
	objs = append(objs, o...)

	// render default network
	o, err = RenderDefaultNetwork(conf, bootstrapResult, manifestDir)
	if err != nil {
		return nil, err
	}
	objs = append(objs, o...)

	// render kube-proxy
	o, err = RenderStandaloneKubeProxy(conf, manifestDir)
	if err != nil {
		return nil, err
	}
	objs = append(objs, o...)

	// render additional networks
	o, err = RenderAdditionalNetworks(conf, manifestDir)
	if err != nil {
		return nil, err
	}
	objs = append(objs, o...)

	log.Printf("Render phase done, rendered %d objects", len(objs))
	return objs, nil
}

// CanonicalizeIPAMConfig converts configuration to a canonical form.
// Currently we only care about case.
func CanonicalizeIPAMConfig(conf *operv1.IPAMConfig) {
	switch strings.ToLower(string(conf.Type)) {
	case strings.ToLower(string(operv1.IPAMTypeDHCP)):
		conf.Type = operv1.IPAMTypeDHCP
	case strings.ToLower(string(operv1.IPAMTypeStatic)):
		conf.Type = operv1.IPAMTypeStatic
	}
}

// CanonicalizeSimpleMacvlanConfig converts configuration to a canonical form.
// Currently we only care about case.
func CanonicalizeSimpleMacvlanConfig(conf *operv1.SimpleMacvlanConfig) {
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
		CanonicalizeIPAMConfig(conf.IPAMConfig)
	}
}

// Canonicalize converts configuration to a canonical form.
// Currently we only care about case.
func Canonicalize(conf *operv1.NetworkSpec) {
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
			CanonicalizeSimpleMacvlanConfig(conf.AdditionalNetworks[idx].SimpleMacvlanConfig)
		}
	}
}

// Validate checks that the supplied configuration is reasonable.
// This should be called after Canonicalize
func Validate(conf *operv1.NetworkSpec) error {
	errs := []error{}

	errs = append(errs, ValidateIPPools(conf)...)
	errs = append(errs, ValidateDefaultNetwork(conf)...)
	errs = append(errs, ValidateMultus(conf)...)
	errs = append(errs, ValidateStandaloneKubeProxy(conf)...)

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
	hostMTU, err := GetDefaultMTU()
	if hostMTU == 0 {
		hostMTU = 1500
	}
	if previous == nil { // host mtu isn't used in subsequent runs, elide these logs
		if err != nil {
			log.Printf("Failed MTU probe, failling back to 1500: %v", err)
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

	FillDefaultNetworkDefaults(conf, previous, hostMTU)
	FillKubeProxyDefaults(conf, previous)
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
	errs = append(errs, IsDefaultNetworkChangeSafe(prev, next)...)

	// Changing AdditionalNetworks is supported

	if !reflect.DeepEqual(prev.DisableMultiNetwork, next.DisableMultiNetwork) {
		errs = append(errs, errors.Errorf("cannot change DisableMultiNetwork"))
	}

	// Check kube-proxy
	errs = append(errs, IsKubeProxyChangeSafe(prev, next)...)

	if len(errs) > 0 {
		return errors.Errorf("invalid configuration: %v", errs)
	}
	return nil
}

// ValidateIPPools checks that all IP addresses are valid
// TODO: check for overlap
func ValidateIPPools(conf *operv1.NetworkSpec) []error {
	errs := []error{}
	for idx, pool := range conf.ClusterNetwork {
		_, _, err := net.ParseCIDR(pool.CIDR)
		if err != nil {
			errs = append(errs, errors.Wrapf(err, "could not parse ClusterNetwork %d CIDR %q", idx, pool.CIDR))
		}
	}

	for idx, pool := range conf.ServiceNetwork {
		_, _, err := net.ParseCIDR(pool)
		if err != nil {
			errs = append(errs, errors.Wrapf(err, "could not parse ServiceNetwork %d CIDR %q", idx, pool))
		}
	}
	return errs
}

// ValidateMultus validates the combination of DisableMultiNetwork and AddtionalNetworks
func ValidateMultus(conf *operv1.NetworkSpec) []error {
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

// ValidateDefaultNetwork validates whichever network is specified
// as the default network.
func ValidateDefaultNetwork(conf *operv1.NetworkSpec) []error {
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

// RenderDefaultNetwork generates the manifests corresponding to the requested
// default network
func RenderDefaultNetwork(conf *operv1.NetworkSpec, bootstrapResult *bootstrap.BootstrapResult, manifestDir string) ([]*uns.Unstructured, error) {
	dn := conf.DefaultNetwork
	if errs := ValidateDefaultNetwork(conf); len(errs) > 0 {
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

// FillDefaultNetworkDefaults
func FillDefaultNetworkDefaults(conf, previous *operv1.NetworkSpec, hostMTU int) {
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

func IsDefaultNetworkChangeSafe(prev, next *operv1.NetworkSpec) []error {
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
func ValidateAdditionalNetworks(conf *operv1.NetworkSpec) []error {
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

// RenderAdditionalNetworks generates the manifests of the requested additional networks
func RenderAdditionalNetworks(conf *operv1.NetworkSpec, manifestDir string) ([]*uns.Unstructured, error) {
	var err error
	ans := conf.AdditionalNetworks
	out := []*uns.Unstructured{}
	objs := []*uns.Unstructured{}

	// validate additional network configuration
	if errs := ValidateAdditionalNetworks(conf); len(errs) > 0 {
		return nil, errors.Errorf("invalid Additional Network Configuration: %v", errs)
	}

	if len(ans) == 0 {
		return nil, nil
	}

	// render additional network configuration
	for _, an := range ans {
		switch an.Type {
		case operv1.NetworkTypeRaw:
			objs, err = renderRawCNIConfig(&an, manifestDir)
			if err != nil {
				return nil, err
			}
			out = append(out, objs...)
		case operv1.NetworkTypeSimpleMacvlan:
			objs, err = renderSimpleMacvlanConfig(&an, manifestDir)
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

// RenderMultusAdmissionController generates the manifests of Multus Admission Controller
func RenderMultusAdmissionController(conf *operv1.NetworkSpec, manifestDir string) ([]*uns.Unstructured, error) {
	if *conf.DisableMultiNetwork {
		return nil, nil
	}

	var err error
	out := []*uns.Unstructured{}
	objs := []*uns.Unstructured{}

	objs, err = renderMultusAdmissonControllerConfig(manifestDir)
	if err != nil {
		return nil, err
	}
	out = append(out, objs...)
	return out, nil
}
