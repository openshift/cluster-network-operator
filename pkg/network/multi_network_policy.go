package network

import (
	"os"
	"path/filepath"

	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/render"
	"github.com/pkg/errors"

	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// renderMultiNetworkpolicyConfig returns the manifests of MultiNetworkPolicy
func renderMultiNetworkpolicyConfig(manifestDir string) ([]*uns.Unstructured, error) {
	objs := []*uns.Unstructured{}

	// render the manifests on disk
	data := render.MakeRenderData()
	data.Data["ReleaseVersion"] = os.Getenv("RELEASE_VERSION")
	data.Data["MultiNetworkPolicyImage"] = os.Getenv("MULTUS_NETWORKPOLICY_IMAGE")

	manifests, err := render.RenderDir(filepath.Join(manifestDir, "network/multus-networkpolicy"), &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render multus networkpolicy manifests")
	}
	objs = append(objs, manifests...)
	return objs, nil
}

// isMultiNetworkpolicyChangeSafe is noop, but it would check if the proposed kube-proxy
// change is safe.
func isMultiNetworkpolicyChangeSafe(prev, next *operv1.NetworkSpec) []error {
	// At present, all multiNetworkPolicy changes are safe to deploy
	return nil
}
