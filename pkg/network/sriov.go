package network

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"

	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/render"
	"github.com/pkg/errors"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type NetConfSRIOV struct {
	// ...
	Type string `json:"type"`
	// ...
}

// isOpenShiftSRIOV determines if an AdditionalNetworkDefinition is OpenShift SRIOV
func isOpenShiftSRIOV(conf *operv1.AdditionalNetworkDefinition) bool {
	cni := NetConfSRIOV{}
	err := json.Unmarshal([]byte(conf.RawCNIConfig), &cni)
	if err != nil {
		log.Printf("WARNING: Could not determine if network %q is SR-IOV: %v", conf.Name, err)
		return false
	}
	return cni.Type == "sriov"
}

// renderOpenShiftSRIOV returns the manifests of OpenShiftSRIOV NetworkAttachmentDefinition,
// OpenShiftSRIOV Device Plugin and OpenShiftSRIOV NetworkAttachmentDefinition CR.
func renderOpenShiftSRIOV(conf *operv1.AdditionalNetworkDefinition, manifestDir string) ([]*uns.Unstructured, error) {
	var err error
	objs := []*uns.Unstructured{}
	// render OpenShiftSRIOV manifests on disk
	data := render.MakeRenderData()
	data.Data["ReleaseVersion"] = os.Getenv("RELEASE_VERSION")
	data.Data["AdditionalNetworkName"] = conf.Name
	data.Data["AdditionalNetworkConfig"] = conf.RawCNIConfig
	data.Data["SRIOVCNIImage"] = os.Getenv("SRIOV_CNI_IMAGE")
	data.Data["SRIOVDevicePluginImage"] = os.Getenv("SRIOV_DEVICE_PLUGIN_IMAGE")
	objs, err = render.RenderDir(filepath.Join(manifestDir, "network/additional-networks/sriov"), &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render OpenShiftSRIOV Network manifests")
	}
	return objs, nil
}
