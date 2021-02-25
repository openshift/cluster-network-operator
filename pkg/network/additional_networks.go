package network

import (
	"encoding/json"
	"net"
	"path/filepath"
	"strings"

	cnitypes "github.com/containernetworking/cni/pkg/types"

	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/render"
	"github.com/pkg/errors"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// renderAdditionalNetworksCRD returns the manifests of the NetworkAttachmentDefinition.
func renderAdditionalNetworksCRD(manifestDir string) ([]*uns.Unstructured, error) {
	objs := []*uns.Unstructured{}
	// render the manifests on disk
	data := render.MakeRenderData()
	manifests, err := render.RenderDir(filepath.Join(manifestDir, "network/additional-networks/crd"), &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render additional network manifests")
	}
	objs = append(objs, manifests...)
	return objs, nil
}

// renderRawCNIConfig returns the RawCNIConfig manifests
func renderRawCNIConfig(conf *operv1.AdditionalNetworkDefinition, manifestDir string) ([]*uns.Unstructured, error) {
	var err error

	// render RawCNIConfig manifests
	data := render.MakeRenderData()
	data.Data["AdditionalNetworkName"] = conf.Name
	data.Data["AdditionalNetworkNamespace"] = conf.Namespace
	data.Data["AdditionalNetworkConfig"] = conf.RawCNIConfig
	objs, err := render.RenderDir(filepath.Join(manifestDir, "network/additional-networks/raw"), &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render additional network")
	}
	return objs, nil
}

// validateRaw checks the AdditionalNetwork name and RawCNIConfig.
func validateRaw(conf *operv1.AdditionalNetworkDefinition) []error {
	out := []error{}
	var rawConfig map[string]interface{}
	var err error

	if conf.Name == "" {
		out = append(out, errors.Errorf("Additional Network Name cannot be nil"))
	}

	confBytes := []byte(conf.RawCNIConfig)
	err = json.Unmarshal(confBytes, &rawConfig)
	if err != nil {
		out = append(out, errors.Errorf("Failed to Unmarshal RawCNIConfig: %v", confBytes))
	}

	return out
}

// staticIPAMConfig for json generation for static IPAM
type staticIPAMConfig struct {
	Type      string              `json:"type"`
	Routes    []*cnitypes.Route   `json:"routes"`
	Addresses []staticIPAMAddress `json:"addresses,omitempty"`
	DNS       cnitypes.DNS        `json:"dns"`
}

// staticIPAMAddress for json generation for static IPAM
type staticIPAMAddress struct {
	AddressStr string `json:"address"`
	Gateway    net.IP `json:"gateway,omitempty"`
}

// getStaticIPAMConfigJSON generates static CNI json config
func getStaticIPAMConfigJSON(conf *operv1.StaticIPAMConfig) (string, error) {
	staticIPAMConfig := staticIPAMConfig{}
	staticIPAMConfig.Type = "static"
	for _, address := range conf.Addresses {
		staticIPAMConfig.Addresses = append(staticIPAMConfig.Addresses, staticIPAMAddress{AddressStr: address.Address, Gateway: net.ParseIP(address.Gateway)})
	}
	for _, route := range conf.Routes {
		_, dest, err := net.ParseCIDR(route.Destination)
		if err != nil {
			return "", errors.Wrap(err, "failed to parse macvlan route")
		}
		staticIPAMConfig.Routes = append(staticIPAMConfig.Routes, &cnitypes.Route{Dst: *dest, GW: net.ParseIP(route.Gateway)})
	}

	if conf.DNS != nil {
		staticIPAMConfig.DNS.Nameservers = append(staticIPAMConfig.DNS.Nameservers, conf.DNS.Nameservers...)
		staticIPAMConfig.DNS.Domain = conf.DNS.Domain
		staticIPAMConfig.DNS.Search = append(staticIPAMConfig.DNS.Search, conf.DNS.Search...)
	}

	jsonByte, err := json.Marshal(staticIPAMConfig)
	if err != nil {
		return "", errors.Wrap(err, "failed to create static ipam config")
	}

	return string(jsonByte), nil
}

// getIPAMConfigJSON generates IPAM CNI json config
func getIPAMConfigJSON(conf *operv1.IPAMConfig) (string, error) {

	if conf == nil || conf.Type == operv1.IPAMTypeDHCP {
		// DHCP does not have additional config
		return `{ "type": "dhcp" }`, nil
	} else if conf.Type == operv1.IPAMTypeStatic {
		staticIPAMConfig, err := getStaticIPAMConfigJSON(conf.StaticIPAMConfig)
		return staticIPAMConfig, err
	}

	return "", errors.Errorf("failed to render IPAM JSON")
}

// renderSimpleMacvlanConfig returns the SimpleMacvlanConfig manifests
func renderSimpleMacvlanConfig(conf *operv1.AdditionalNetworkDefinition, manifestDir string) ([]*uns.Unstructured, error) {
	var err error

	// render SimpleMacvlanConfig manifests
	data := render.MakeRenderData()
	data.Data["AdditionalNetworkName"] = conf.Name
	data.Data["AdditionalNetworkNamespace"] = conf.Namespace

	if conf.SimpleMacvlanConfig == nil {
		// no additional config, just fill default IPAM
		data.Data["IPAMConfig"], err = getIPAMConfigJSON(nil)
		if err != nil {
			return nil, errors.Wrap(err, "failed to render ipam config")
		}

	} else {
		macvlanConfig := conf.SimpleMacvlanConfig
		data.Data["Master"] = macvlanConfig.Master

		data.Data["IPAMConfig"], err = getIPAMConfigJSON(macvlanConfig.IPAMConfig)
		if err != nil {
			return nil, errors.Wrap(err, "failed to render ipam config")
		}

		if macvlanConfig.Mode != "" {
			// macvlan CNI only accepts mode in lowercase
			data.Data["Mode"] = strings.ToLower(string(macvlanConfig.Mode))
		}

		if macvlanConfig.MTU != 0 {
			data.Data["MTU"] = macvlanConfig.MTU
		}
	}

	objs, err := render.RenderDir(filepath.Join(manifestDir, "network/additional-networks/simplemacvlan"), &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render simplemacvlan additional network")
	}
	return objs, nil
}

// validateStaticIPAMConfig checks its IPAMConfig.
func validateStaticIPAMConfig(conf *operv1.StaticIPAMConfig) []error {
	out := []error{}
	for _, addr := range conf.Addresses {
		_, _, err := net.ParseCIDR(addr.Address)
		if err != nil {
			out = append(out, errors.Errorf("invalid static address: %v", err))
		}
		if addr.Gateway != "" && net.ParseIP(addr.Gateway) == nil {
			out = append(out, errors.Errorf("invalid gateway: %s", addr.Gateway))
		}
	}
	for _, route := range conf.Routes {
		_, _, err := net.ParseCIDR(route.Destination)
		if err != nil {
			out = append(out, errors.Errorf("invalid route destination: %v", err))
		}
		if route.Gateway != "" && net.ParseIP(route.Gateway) == nil {
			out = append(out, errors.Errorf("invalid gateway: %s", route.Gateway))
		}
	}
	return out
}

// validateIPAMConfig checks its IPAMConfig.
func validateIPAMConfig(conf *operv1.IPAMConfig) []error {
	out := []error{}

	switch conf.Type {
	case operv1.IPAMTypeStatic:
		outStatic := validateStaticIPAMConfig(conf.StaticIPAMConfig)
		out = append(out, outStatic...)
	case operv1.IPAMTypeDHCP:
	default:
		out = append(out, errors.Errorf("invalid IPAM type: %s", conf.Type))
	}

	return out
}

// validateSimpleMacvlanConfig checks its name and SimpleMacvlanConfig.
func validateSimpleMacvlanConfig(conf *operv1.AdditionalNetworkDefinition) []error {
	out := []error{}

	if conf.Name == "" {
		out = append(out, errors.Errorf("Additional Network Name cannot be nil"))
	}

	if conf.SimpleMacvlanConfig != nil {
		macvlanConfig := conf.SimpleMacvlanConfig
		if macvlanConfig.IPAMConfig != nil {
			outIPAM := validateIPAMConfig(macvlanConfig.IPAMConfig)
			out = append(out, outIPAM...)
		}

		if conf.SimpleMacvlanConfig.Mode != "" {
			switch conf.SimpleMacvlanConfig.Mode {
			case operv1.MacvlanModeBridge:
			case operv1.MacvlanModePrivate:
			case operv1.MacvlanModeVEPA:
			case operv1.MacvlanModePassthru:
			default:
				out = append(out, errors.Errorf("invalid Macvlan mode: %s", conf.SimpleMacvlanConfig.Mode))
			}
		}
	}

	return out
}
