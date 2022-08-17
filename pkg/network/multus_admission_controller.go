package network

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/render"
	"github.com/pkg/errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// ignoredNamespaces contains the comma separated namespace list that should be ignored
// to watch by multus admission controller. This only initialized first invocation.
var ignoredNamespaces string

// getOpenshiftNamespaces collect openshift related namespaces, as comma separate list
func getOpenshiftNamespaces(client kubernetes.Interface) (string, error) {
	namespaces := []string{}

	// get openshift specific namespaces to add them into ignoreNamespace
	nsList, err := client.CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{
		LabelSelector: "openshift.io/cluster-monitoring==true",
	})
	if err != nil {
		return "", errors.Wrap(err, "failed to get namespaces to render multus admission controller manifests")
	}

	for _, ns := range nsList.Items {
		namespaces = append(namespaces, ns.Name)
	}
	return strings.Join(namespaces, ","), nil
}

// renderMultusAdmissonControllerConfig returns the manifests of Multus Admisson Controller
func renderMultusAdmissonControllerConfig(manifestDir string, externalControlPlane bool, replicas int, client kubernetes.Interface) ([]*uns.Unstructured, error) {
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
	data.Data["Replicas"] = replicas

	manifests, err := render.RenderDir(filepath.Join(manifestDir, "network/multus-admission-controller"), &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render multus admission controller manifests")
	}
	objs = append(objs, manifests...)
	return objs, nil
}
