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

	// render default network
	o, err := RenderDefaultNetwork(conf, manifestDir)
	if err != nil {
		return nil, err
	}
	objs = append(objs, o...)

	// render kube-proxy
	// TODO: kube-proxy

	// render additional networks
	// TODO: extra networks

	log.Printf("Render phase done, rendered %d objects", len(objs))
	return objs, nil
}

// Validate checks that the supplied configuration is reasonable.
func Validate(conf *netv1.NetworkConfigSpec) error {
	errs := []error{}

	errs = append(errs, ValidateDefaultNetwork(conf)...)

	if len(errs) > 0 {
		return errors.Errorf("invalid configuration: %v", errs)
	}
	return nil
}

// FillDefaults computes any default values and applies them to the configuration
// This is a mutating operation. It should be called after Validate.
//
// This is done as an explicit step so we can check if a change is safe. We
// explicitly record all generated defaults at apply time, rather than leaving
// them implicit, so that a change in code later won't accidentally introduce
// an unsafe change.
func FillDefaults(conf *netv1.NetworkConfigSpec) {
	FillDefaultNetworkDefaults(conf)
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

// ValidateDefaultNetwork validates whichever network is specified
// as the default network.
func ValidateDefaultNetwork(conf *netv1.NetworkConfigSpec) []error {
	switch conf.DefaultNetwork.Type {
	case netv1.NetworkTypeOpenShiftSDN, netv1.NetworkTypeDeprecatedOpenshiftSDN:
		return validateOpenShiftSDN(conf)
	default:
		return []error{errors.Errorf("unknown or unsupported NetworkType: %s", conf.DefaultNetwork.Type)}
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
	}

	return nil, errors.Errorf("unknown or unsupported NetworkType: %s", dn.Type)
}

// FillDefaultNetworkDefaults
func FillDefaultNetworkDefaults(conf *netv1.NetworkConfigSpec) {
	switch conf.DefaultNetwork.Type {
	case netv1.NetworkTypeOpenShiftSDN, netv1.NetworkTypeDeprecatedOpenshiftSDN:
		fillOpenShiftSDNDefaults(conf)
	default:
		// This case has already been excluded by Validate
		panic("invalid network")
	}
}

func IsDefaultNetworkChangeSafe(prev, next *netv1.NetworkConfigSpec) []error {
	if prev.DefaultNetwork.Type != next.DefaultNetwork.Type {
		return []error{errors.Errorf("cannot change default network type")}
	}

	switch prev.DefaultNetwork.Type {
	case netv1.NetworkTypeOpenShiftSDN, netv1.NetworkTypeDeprecatedOpenshiftSDN:
		return isOpenShiftSDNChangeSafe(prev, next)
	default: // should be unreachable
		return []error{errors.Errorf("unknown network type %s", prev.DefaultNetwork.Type)}
	}
}
