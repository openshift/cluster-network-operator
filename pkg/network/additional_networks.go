package network

import (
	"encoding/json"
	netv1 "github.com/openshift/cluster-network-operator/pkg/apis/networkoperator/v1"
	"github.com/openshift/cluster-network-operator/pkg/render"
	"github.com/pkg/errors"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"os"
	"path/filepath"
)

// renderMultusConfig returns the manifests of Multus DaemonSet and NetworkAttachmentDefinition.
func renderMultusConfig(conf *netv1.NetworkConfigSpec, manifestDir string) ([]*uns.Unstructured, error) {
	objs := []*uns.Unstructured{}
	// render the manifests on disk
	data := render.MakeRenderData()

	nodeCfg, err := multusNodeConfig(conf)
	if err != nil {
		return nil, errors.Wrap(err, "failed to build multus node config")
	}
	data.Data["NodeConfig"] = nodeCfg

	manifests, err := render.RenderDir(filepath.Join(manifestDir, "network/additional-networks/multus"), &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render manifests")
	}
	objs = append(objs, manifests...)
	return objs, nil
}

// renderRawCNIConfig returns the RawCNIConfig manifests
func renderRawCNIConfig(conf *netv1.AdditionalNetworkDefinition, manifestDir string) ([]*uns.Unstructured, error) {
	var err error
	objs := []*uns.Unstructured{}

	// render RawCNIConfig manifests
	data := render.MakeRenderData()
	data.Data["AdditionalNetworkName"] = conf.Name
	data.Data["AdditionalNetworkConfig"] = conf.RawCNIConfig
	data.Data["AdditionalNetworkNamespace"] = conf.Namespace
	if len(conf.Annotations) == 0 {
		data.Data["ConfigAnnotation"] = false
	} else {
		data.Data["ConfigAnnotation"] = true
		data.Data["AdditionalNetworkAnnotations"] = conf.Annotations
	}
	objs, err = render.RenderDir(filepath.Join(manifestDir, "network/additional-networks/cr"), &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render additional network")
	}
	return objs, nil
}

// validateRaw checks the AdditionalNetwork name and RawCNIConfig.
func validateRaw(conf *netv1.AdditionalNetworkDefinition) []error {
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

// multusNodeConfig builds the (json text of) the NodeConfig object
// consumed by the multus node process
func multusNodeConfig(conf *netv1.NetworkConfigSpec) (string, error) {
	dn := conf.DefaultNetwork
	var defaultCNIName string
	var defaultCNIType string

	switch dn.Type {
	case netv1.NetworkTypeOpenShiftSDN, netv1.NetworkTypeDeprecatedOpenshiftSDN:
		defaultCNIName = "openshift-sdn"
		defaultCNIType = "openshift-sdn"
	default:
		panic("invalid network")
	}

	cniConf := &netv1.MultusCNIConfig{}
	cniConf.Name = "multus-cni-network"
	cniConf.Type = "multus"
	cniConf.Kubeconfig = os.Getenv("HOST_KUBECONFIG")

	delegateConf := &netv1.MultusDelegate{}
	delegateConf.Name = defaultCNIName
	delegateConf.Type = defaultCNIType

	cniConf.Delegates = append(cniConf.Delegates, delegateConf)

	c, err := json.Marshal(cniConf)
	if err != nil {
		return "", err
	}

	return string(c), nil
}
