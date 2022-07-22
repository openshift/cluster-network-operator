package network

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/render"
	"github.com/pkg/errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"
)

// ignoredNamespaces contains the comma separated namespace list that should be ignored
// to watch by multus admission controller. This only initialized first invocation.
var ignoredNamespaces string

// getOpenshiftNamespaces collect openshift related namespaces, as comma separate list
func getOpenshiftNamespaces(client cnoclient.Client) (string, error) {
	namespaces := []string{}

	// get openshift specific namespaces to add them into ignoreNamespace
	nsList, err := client.Default().Kubernetes().CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{
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
func renderMultusAdmissonControllerConfig(manifestDir string, externalControlPlane bool, bootstrapResult *bootstrap.BootstrapResult, client cnoclient.Client) ([]*uns.Unstructured, error) {
	objs := []*uns.Unstructured{}
	var err error

	replicas := getMultusAdmissionControllerReplicas(bootstrapResult)
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
	// Hypershift
	hsc := NewHyperShiftConfig()
	data.Data["HyperShiftEnabled"] = hsc.Enabled
	data.Data["ManagementClusterName"] = names.ManagementClusterName
	data.Data["AdmissionControllerNamespace"] = "openshift-multus"
	if hsc.Enabled {
		data.Data["AdmissionControllerNamespace"] = hsc.Namespace
		data.Data["KubernetesServiceHost"] = bootstrapResult.Infra.APIServers[bootstrap.APIServerDefaultLocal].Host
		data.Data["KubernetesServicePort"] = bootstrapResult.Infra.APIServers[bootstrap.APIServerDefaultLocal].Port
		data.Data["CLIImage"] = os.Getenv("CLI_IMAGE")
		data.Data["TokenMinterImage"] = os.Getenv("TOKEN_MINTER_IMAGE")
		data.Data["TokenAudience"] = os.Getenv("TOKEN_AUDIENCE")

		// Get serving CA from the management cluster since the service resides there
		serviceCA := &corev1.ConfigMap{}
		err := client.ClientFor(names.ManagementClusterName).CRClient().Get(
			context.TODO(), types.NamespacedName{Namespace: hsc.Namespace, Name: "openshift-service-ca.crt"}, serviceCA)
		if err != nil {
			return nil, fmt.Errorf("failed to get managments clusters service CA: %v", err)
		}
		ca, exists := serviceCA.Data["service-ca.crt"]
		if !exists {
			return nil, fmt.Errorf("(%s) %s/%s missing 'service-ca.crt' key", serviceCA.GroupVersionKind(), serviceCA.Namespace, serviceCA.Name)
		}

		data.Data["ManagementServiceCABundle"] = base64.URLEncoding.EncodeToString([]byte(ca))
	}

	manifests, err := render.RenderDir(filepath.Join(manifestDir, "network/multus-admission-controller"), &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render multus admission controller manifests")
	}
	objs = append(objs, manifests...)
	return objs, nil
}
