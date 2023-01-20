package network

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/render"
	"github.com/pkg/errors"

	corev1 "k8s.io/api/core/v1"
	//metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	//"k8s.io/apimachinery/pkg/labels"
	//"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ignoredNamespaces contains the comma separated namespace list that should be ignored
// to watch by multus admission controller. This only initialized first invocation.
var ignoredNamespaces string

// getOpenshiftNamespaces collect openshift related namespaces, as comma separate list
func getOpenshiftNamespaces(kubeClient client.Client) (string, error) {
	namespaces := []string{}

	// get openshift specific namespaces to add them into ignoreNamespace
	nsList := &corev1.NamespaceList{}
	matchingLabels := &client.MatchingLabels{"openshift.io/cluster-monitoring": "true"}
	err := kubeClient.List(context.TODO(), nsList, matchingLabels)

	if err != nil {
		return "", errors.Wrap(err, "failed to get namespaces to render multus admission controller manifests")
	}

	for _, ns := range nsList.Items {
		namespaces = append(namespaces, ns.Name)
	}
	return strings.Join(namespaces, ","), nil
}

// renderMultusAdmissonControllerConfig returns the manifests of Multus Admisson Controller
func renderMultusAdmissonControllerConfig(manifestDir string, externalControlPlane bool, client client.Client) ([]*uns.Unstructured, error) {
	objs := []*uns.Unstructured{}
	var err error

	if ignoredNamespaces == "" {
		ignoredNamespaces, err = getOpenshiftNamespaces(client)
		if err != nil {
			klog.Warningf("failed to get openshift namespaces: %+v", err)
		}
	}

	// render the manifests on disk
	data := render.MakeRenderData()
	data.Data["ReleaseVersion"] = os.Getenv("RELEASE_VERSION")
	data.Data["MultusAdmissionControllerImage"] = os.Getenv("MULTUS_ADMISSION_CONTROLLER_IMAGE")
	data.Data["IgnoredNamespace"] = ignoredNamespaces
	data.Data["MultusValidatingWebhookName"] = names.MULTUS_VALIDATING_WEBHOOK
	data.Data["KubeRBACProxyImage"] = os.Getenv("KUBE_RBAC_PROXY_IMAGE")
	data.Data["ExternalControlPlane"] = externalControlPlane

	manifests, err := render.RenderDir(filepath.Join(manifestDir, "network/multus-admission-controller"), &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render multus admission controller manifests")
	}
	objs = append(objs, manifests...)
	return objs, nil
}
