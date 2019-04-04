package network

import (
	"os"
	"path/filepath"

	"github.com/openshift/cluster-network-operator/pkg/render"
	"github.com/pkg/errors"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// renderMultusConfig returns the manifests of Multus
func renderMultusConfig(manifestDir string, useDHCP bool) ([]*uns.Unstructured, error) {
	objs := []*uns.Unstructured{}

	// render the manifests on disk
	data := render.MakeRenderData()
	data.Data["ReleaseVersion"] = os.Getenv("RELEASE_VERSION")
	data.Data["MultusImage"] = os.Getenv("MULTUS_IMAGE")
	data.Data["CNIPluginsSupportedImage"] = os.Getenv("CNI_PLUGINS_SUPPORTED_IMAGE")
	data.Data["CNIPluginsUnsupportedImage"] = os.Getenv("CNI_PLUGINS_UNSUPPORTED_IMAGE")
	data.Data["KUBERNETES_SERVICE_HOST"] = os.Getenv("KUBERNETES_SERVICE_HOST")
	data.Data["KUBERNETES_SERVICE_PORT"] = os.Getenv("KUBERNETES_SERVICE_PORT")
	data.Data["RenderDHCP"] = useDHCP

	manifests, err := render.RenderDir(filepath.Join(manifestDir, "network/multus"), &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render multus manifests")
	}
	objs = append(objs, manifests...)
	return objs, nil
}
