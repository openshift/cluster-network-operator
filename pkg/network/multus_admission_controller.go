package network

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/hypershift"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/render"

	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"
)

const bytesInMiB = 1024 * 1024

// IsMultusAdmissionControllerIgnoredNamespace reports whether a namespace should
// be excluded from Multus admission validation.
func IsMultusAdmissionControllerIgnoredNamespace(ns *corev1.Namespace) bool {
	if ns == nil {
		return false
	}

	return metav1.HasAnnotation(ns.ObjectMeta, "workload.openshift.io/allowed") &&
		ns.Annotations["workload.openshift.io/allowed"] == "management" &&
		ns.Labels["openshift.io/cluster-monitoring"] == "true"
}

// getOpenshiftNamespaces returns OpenShift-related namespaces as a comma-separated list.
func getOpenshiftNamespaces(client cnoclient.Client) (string, error) {
	namespaces := []string{}

	// get openshift specific namespaces to add them into ignoreNamespace
	nsList, err := client.Default().Kubernetes().CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{
		LabelSelector: "openshift.io/cluster-monitoring==true",
	})
	if err != nil {
		return "", fmt.Errorf("failed to get namespaces to render multus admission controller manifests: %w", err)
	}

	for _, ns := range nsList.Items {
		if IsMultusAdmissionControllerIgnoredNamespace(&ns) {
			namespaces = append(namespaces, ns.Name)
		}
	}
	sort.Strings(namespaces)
	return strings.Join(namespaces, ","), nil
}

// renderMultusAdmissonControllerConfig returns the manifests for the Multus admission controller.
func renderMultusAdmissonControllerConfig(manifestDir string, externalControlPlane bool, bootstrapResult *bootstrap.BootstrapResult, client cnoclient.Client, hsc *hypershift.HyperShiftConfig, clientName string, featureGates featuregates.FeatureGate) ([]*uns.Unstructured, error) {
	objs := []*uns.Unstructured{}
	var err error

	replicas := getMultusAdmissionControllerReplicas(bootstrapResult, hsc.Enabled)
	ignoredNamespaces, err := getOpenshiftNamespaces(client)
	if err != nil {
		return nil, fmt.Errorf("failed to get openshift namespaces: %w", err)
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
	data.Data["HyperShiftEnabled"] = hsc.Enabled
	data.Data["ManagementClusterName"] = names.ManagementClusterName
	data.Data["AdmissionControllerNamespace"] = "openshift-multus"
	data.Data["RHOBSMonitoring"] = os.Getenv("RHOBS_MONITORING")
	data.Data["ResourceRequestCPU"] = nil
	data.Data["ResourceRequestMemory"] = nil
	data.Data["PriorityClass"] = nil

	if hsc.Enabled {
		data.Data["AdmissionControllerNamespace"] = hsc.Namespace
		localAPIServer := bootstrapResult.Infra.APIServers[bootstrap.APIServerDefaultLocal]
		data.Data["KubernetesServiceURL"] = "https://" + net.JoinHostPort(localAPIServer.Host, localAPIServer.Port)
		data.Data["CLIImage"] = os.Getenv("CLI_IMAGE")
		if os.Getenv("CLI_CONTROL_PLANE_IMAGE") != "" {
			data.Data["CLIImage"] = os.Getenv("CLI_CONTROL_PLANE_IMAGE")
		}
		data.Data["TokenMinterImage"] = os.Getenv("TOKEN_MINTER_IMAGE")
		data.Data["TokenAudience"] = os.Getenv("TOKEN_AUDIENCE")
		data.Data["RunAsUser"] = hsc.RunAsUser
		data.Data["CAConfigMap"] = hsc.CAConfigMap
		data.Data["CAConfigMapKey"] = hsc.CAConfigMapKey

		serviceCA := &corev1.ConfigMap{}
		err := client.ClientFor(clientName).CRClient().Get(
			context.TODO(), types.NamespacedName{Namespace: hsc.Namespace, Name: hsc.CAConfigMap}, serviceCA)
		if err != nil {
			return nil, fmt.Errorf("failed to get managments clusters service CA: %v", err)
		}
		ca, exists := serviceCA.Data[hsc.CAConfigMapKey]
		if !exists {
			return nil, fmt.Errorf("(%s) %s/%s missing CA ConfigMap key", serviceCA.GroupVersionKind(), serviceCA.Namespace, serviceCA.Name)
		}

		data.Data["ManagementServiceCABundle"] = base64.URLEncoding.EncodeToString([]byte(ca))

		data.Data["ClusterIDLabel"] = hypershift.ClusterIDLabel
		data.Data["ClusterID"] = bootstrapResult.Infra.HostedControlPlane.ClusterID
		data.Data["HCPNodeSelector"] = bootstrapResult.Infra.HostedControlPlane.NodeSelector
		data.Data["HCPLabels"] = bootstrapResult.Infra.HostedControlPlane.Labels
		data.Data["HCPTolerations"] = bootstrapResult.Infra.HostedControlPlane.Tolerations
		data.Data["PriorityClass"] = bootstrapResult.Infra.HostedControlPlane.PriorityClass

		// Preserve any existing multus container resource requests which may have been modified by an external source
		multusDeploy := &appsv1.Deployment{}
		err = client.ClientFor(clientName).CRClient().Get(
			context.TODO(), types.NamespacedName{Namespace: hsc.Namespace, Name: "multus-admission-controller"}, multusDeploy)
		if err == nil {
			multusContainer, ok := findContainer(multusDeploy.Spec.Template.Spec.Containers, "multus-admission-controller")
			if !ok {
				return nil, errors.New("error finding multus container")
			}
			if !multusContainer.Resources.Requests.Cpu().IsZero() {
				data.Data["ResourceRequestCPU"] = multusContainer.Resources.Requests.Cpu().MilliValue()
			}
			if !multusContainer.Resources.Requests.Memory().IsZero() {
				data.Data["ResourceRequestMemory"] = multusContainer.Resources.Requests.Memory().Value() / bytesInMiB
			}
		} else {
			if apierrors.IsNotFound(err) {
				klog.Warningf("failed to get multus deployment: %v", err)
			} else {
				return nil, fmt.Errorf("failed to get multus deployment: %v", err)
			}
		}

		data.Data["ReleaseImage"] = hsc.ReleaseImage
	}

	addTLSInfoToRenderData(data.Data, bootstrapResult, true)

	manifests, err := render.RenderDir(filepath.Join(manifestDir, "network/multus-admission-controller"), &data)
	if err != nil {
		return nil, fmt.Errorf("failed to render multus admission controller manifests: %w", err)
	}
	objs = append(objs, manifests...)
	return objs, nil
}

func findContainer(conts []corev1.Container, name string) (corev1.Container, bool) {
	for _, cont := range conts {
		if cont.Name == name {
			return cont, true
		}
	}
	return corev1.Container{}, false
}
