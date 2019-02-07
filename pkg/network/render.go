package network

import (
	"log"
	"reflect"

	"github.com/pkg/errors"

	netv1 "github.com/openshift/cluster-network-operator/pkg/apis/networkoperator/v1"

	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func Render(conf *netv1.NetworkConfigSpec, manifestDir string) ([]*uns.Unstructured, error) {
	log.Printf("Starting render phase")
	objs := []*uns.Unstructured{}

	// render Multus
	o, err := RenderMultus(conf, manifestDir)
	if err != nil {
		return nil, err
	}
	objs = append(objs, o...)

	// render default network
	o, err = RenderDefaultNetwork(conf, manifestDir)
	if err != nil {
		return nil, err
	}
	objs = append(objs, o...)

	// render kube-proxy
	// TODO: kube-proxy

	// render additional networks
	o, err = RenderAdditionalNetworks(conf, manifestDir)
	if err != nil {
		return nil, err
	}
	objs = append(objs, o...)

	log.Printf("Render phase done, rendered %d objects", len(objs))
	return objs, nil
}

// Validate checks that the supplied configuration is reasonable.
func Validate(conf *netv1.NetworkConfigSpec) error {
	errs := []error{}

	errs = append(errs, ValidateDefaultNetwork(conf)...)
	errs = append(errs, ValidateMultus(conf)...)

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
func FillDefaults(conf, previous *netv1.NetworkConfigSpec) {
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
	FillDefaultNetworkDefaults(conf, previous, hostMTU)
}

// IsChangeSafe checks to see if the change between prev and next are allowed
// FillDefaults and Validate should have been called.
func IsChangeSafe(prev, next *netv1.NetworkConfigSpec) error {
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
	if !reflect.DeepEqual(prev.ClusterNetworks, next.ClusterNetworks) {
		errs = append(errs, errors.Errorf("cannot change ClusterNetworks"))
	}

	// Nor can you change service network
	if prev.ServiceNetwork != next.ServiceNetwork {
		errs = append(errs, errors.Errorf("cannot change ServiceNetwork"))
	}

	// Check the default network
	errs = append(errs, IsDefaultNetworkChangeSafe(prev, next)...)

	// Changing KubeProxyConfig and DeployKubeProxy is allowed, so we don't check that

	if len(errs) > 0 {
		return errors.Errorf("invalid configuration: %v", errs)
	}
	return nil
}

// ValidateMultus validates the combination of DisableMultiNetwork and AddtionalNetworks
func ValidateMultus(conf *netv1.NetworkConfigSpec) []error {
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
func ValidateDefaultNetwork(conf *netv1.NetworkConfigSpec) []error {
	switch conf.DefaultNetwork.Type {
	case netv1.NetworkTypeOpenShiftSDN, netv1.NetworkTypeDeprecatedOpenshiftSDN:
		return validateOpenShiftSDN(conf)
	default:
		return nil
	}
}

// RenderDefaultNetwork generates the manifests corresponding to the requested
// default network
func RenderDefaultNetwork(conf *netv1.NetworkConfigSpec, manifestDir string) ([]*uns.Unstructured, error) {
	dn := conf.DefaultNetwork
	if errs := ValidateDefaultNetwork(conf); len(errs) > 0 {
		return nil, errors.Errorf("invalid Default Network configuration: %v", errs)
	}

	switch dn.Type {
	case netv1.NetworkTypeOpenShiftSDN, netv1.NetworkTypeDeprecatedOpenshiftSDN:
		return renderOpenShiftSDN(conf, manifestDir)
	default:
		log.Printf("NOTICE: Unknown network type %s, ignoring", dn.Type)
		return nil, nil
	}
}

// FillDefaultNetworkDefaults
func FillDefaultNetworkDefaults(conf, previous *netv1.NetworkConfigSpec, hostMTU int) {
	switch conf.DefaultNetwork.Type {
	case netv1.NetworkTypeOpenShiftSDN, netv1.NetworkTypeDeprecatedOpenshiftSDN:
		fillOpenShiftSDNDefaults(conf, previous, hostMTU)
	default:
	}
}

func IsDefaultNetworkChangeSafe(prev, next *netv1.NetworkConfigSpec) []error {
	if prev.DefaultNetwork.Type != next.DefaultNetwork.Type {
		return []error{errors.Errorf("cannot change default network type")}
	}

	switch prev.DefaultNetwork.Type {
	case netv1.NetworkTypeOpenShiftSDN, netv1.NetworkTypeDeprecatedOpenshiftSDN:
		return isOpenShiftSDNChangeSafe(prev, next)
	default:
		return nil
	}
}

// ValidateAdditionalNetworks validates additional networks configs
func ValidateAdditionalNetworks(conf *netv1.NetworkConfigSpec) [][]error {
	out := [][]error{}
	ans := conf.AdditionalNetworks
	for _, an := range ans {
		switch an.Type {
		case netv1.NetworkTypeRaw:
			if errs := validateRaw(&an); len(errs) > 0 {
				out = append(out, errs)
			}
		default:
			out = append(out, []error{errors.Errorf("unknown or unsupported NetworkType: %s", an.Type)})
		}
	}
	return out
}

// RenderAdditionalNetworks generates the manifests of the requested additional networks
func RenderAdditionalNetworks(conf *netv1.NetworkConfigSpec, manifestDir string) ([]*uns.Unstructured, error) {
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
		case netv1.NetworkTypeRaw:
			objs, err = renderRawCNIConfig(&an, manifestDir)
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

// RenderMultus generates the manifests of Multus
func RenderMultus(conf *netv1.NetworkConfigSpec, manifestDir string) ([]*uns.Unstructured, error) {
	if *conf.DisableMultiNetwork {
		return nil, nil
	}

	var err error
	out := []*uns.Unstructured{}
	objs := []*uns.Unstructured{}

	// enabling Multus always renders the CRD since Multus uses it
	objs, err = renderAdditionalNetworksCRD(manifestDir)
	if err != nil {
		return nil, err
	}
	out = append(out, objs...)

	objs, err = renderMultusConfig(manifestDir)
	if err != nil {
		return nil, err
	}
	out = append(out, objs...)
	return out, nil
}
