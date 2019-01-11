package network

import (
	"github.com/openshift/cluster-network-operator/pkg/render"
	"github.com/pkg/errors"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"os"
	"path/filepath"
)

// renderSRIOVDevicePluginConfig returns the manifests of the SRIOV Device Plugin
func renderSRIOVDevicePluginConfig(manifestDir string) ([]*uns.Unstructured, error) {
	objs := []*uns.Unstructured{}

	// render the manifests on disk
	data := render.MakeRenderData()
	data.Data["SRIOVDevicePluginImage"] = os.Getenv("SRIOV_DEVICE_PLUGIN_IMAGE")
	data.Data["SRIOVCNIImage"] = os.Getenv("SRIOV_CNI_IMAGE")

	manifests, err := render.RenderDir(filepath.Join(manifestDir, "network/sriov"), &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render SRIOV Device Plugin manifests")
	}
	objs = append(objs, manifests...)
	return objs, nil
}
