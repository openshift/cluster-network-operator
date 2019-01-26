package network

import (
	"github.com/openshift/cluster-network-operator/pkg/render"
	"github.com/pkg/errors"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"os"
	"path/filepath"
)

// renderMultusConfig returns the manifests of Multus
func renderMultusConfig(manifestDir string) ([]*uns.Unstructured, error) {
	objs := []*uns.Unstructured{}

	// render the manifests on disk
	data := render.MakeRenderData()
	data.Data["MultusImage"] = os.Getenv("MULTUS_IMAGE")
	data.Data["CNIPluginsSupportedImage"] = os.Getenv("CNI_PLUGINS_SUPPORTED_IMAGE")
	data.Data["CNIPluginsUnsupportedImage"] = os.Getenv("CNI_PLUGINS_UNSUPPORTED_IMAGE")

	manifests, err := render.RenderDir(filepath.Join(manifestDir, "network/multus"), &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render multus manifests")
	}
	objs = append(objs, manifests...)
	return objs, nil
}
