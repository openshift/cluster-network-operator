package network

import (
	"encoding/json"
	"path/filepath"

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
	objs := []*uns.Unstructured{}

	// render RawCNIConfig manifests
	data := render.MakeRenderData()
	data.Data["AdditionalNetworkName"] = conf.Name
	data.Data["AdditionalNetworkNamespace"] = conf.Namespace
	data.Data["AdditionalNetworkConfig"] = conf.RawCNIConfig
	objs, err = render.RenderDir(filepath.Join(manifestDir, "network/additional-networks/raw"), &data)
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

// renderMacvlanConfig returns the RawCNIConfig manifests
func renderMacvlanConfig(conf *operv1.AdditionalNetworkDefinition, manifestDir string) ([]*uns.Unstructured, error) {
	var err error
	objs := []*uns.Unstructured{}
	macvlanConfig := conf.MacvlanConfig

	// render RawCNIConfig manifests
	data := render.MakeRenderData()
	data.Data["AdditionalNetworkName"] = conf.Name
	data.Data["Master"] = macvlanConfig.Master
	data.Data["IPAM"] = "dhcp"

	if macvlanConfig.IPAM != "" {
		data.Data["IPAM"] = macvlanConfig.IPAM
	}

	if macvlanConfig.Mode != "" {
		data.Data["Mode"] = macvlanConfig.Mode
	}

	if macvlanConfig.MTU != nil {
		data.Data["MTU"] = macvlanConfig.MTU
	}

	objs, err = render.RenderDir(filepath.Join(manifestDir, "network/additional-networks/macvlan"), &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render macvlan additional network")
	}
	return objs, nil
}

// validateMacvlanConfig checks the M name and RawCNIConfig.
func validateMacvlanConfig(conf *operv1.AdditionalNetworkDefinition) []error {
	out := []error{}
	macvlanConfig := conf.MacvlanConfig

	if conf.Name == "" {
		out = append(out, errors.Errorf("Additional Network Name cannot be nil"))
	}

	if macvlanConfig.Master == "" {
		out = append(out, errors.Errorf("macvlan master cannot be nil"))
	}

	return out
}
