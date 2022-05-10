package network

import (
	"os"
	"path/filepath"

	"github.com/pkg/errors"

	v1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"github.com/openshift/cluster-network-operator/pkg/render"
	k8sutil "github.com/openshift/cluster-network-operator/pkg/util/k8s"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// renderCloudNetworkConfigController renders the cloud network config controller
func renderCloudNetworkConfigController(conf *operv1.NetworkSpec, cloudBootstrapResult bootstrap.InfraStatus, manifestDir string) ([]*uns.Unstructured, error) {
	pt := cloudBootstrapResult.PlatformType
	if !(pt == v1.AWSPlatformType || pt == v1.AzurePlatformType || pt == v1.GCPPlatformType) {
		return nil, nil
	}
	data := render.MakeRenderData()
	data.Data["ReleaseVersion"] = os.Getenv("RELEASE_VERSION")
	data.Data["PlatformType"] = cloudBootstrapResult.PlatformType
	data.Data["PlatformRegion"] = cloudBootstrapResult.PlatformRegion
	data.Data["PlatformTypeAWS"] = v1.AWSPlatformType
	data.Data["PlatformTypeAzure"] = v1.AzurePlatformType
	data.Data["PlatformTypeGCP"] = v1.GCPPlatformType
	data.Data["CloudNetworkConfigControllerImage"] = os.Getenv("CLOUD_NETWORK_CONFIG_CONTROLLER_IMAGE")
	data.Data["KubernetesServiceHost"] = cloudBootstrapResult.APIServers[bootstrap.APIServerDefault].Host
	data.Data["KubernetesServicePort"] = cloudBootstrapResult.APIServers[bootstrap.APIServerDefault].Port
	data.Data["ExternalControlPlane"] = cloudBootstrapResult.ExternalControlPlane
	data.Data["PlatformAzureEnvironment"] = ""
	data.Data["PlatformAWSCAPath"] = ""
	data.Data["HTTP_PROXY"] = cloudBootstrapResult.Proxy.HTTPProxy
	data.Data["HTTPS_PROXY"] = cloudBootstrapResult.Proxy.HTTPSProxy
	data.Data["NO_PROXY"] = cloudBootstrapResult.Proxy.NoProxy

	// AWS and azure allow for funky endpoint overriding.
	// in different ways, of course.
	apiurl := ""
	if cloudBootstrapResult.PlatformType == v1.AWSPlatformType {
		for _, ep := range cloudBootstrapResult.PlatformStatus.AWS.ServiceEndpoints {
			if ep.Name == "ec2" {
				apiurl = ep.URL
			}
		}
		if cloudBootstrapResult.KubeCloudConfig["ca-bundle.pem"] != "" {
			data.Data["PlatformAWSCAPath"] = "/kube-cloud-config/ca-bundle.pem" // installed by ConfigMap
		}
	}

	if cloudBootstrapResult.PlatformType == v1.AzurePlatformType {
		apiurl = cloudBootstrapResult.PlatformStatus.Azure.ARMEndpoint
		data.Data["PlatformAzureEnvironment"] = cloudBootstrapResult.PlatformStatus.Azure.CloudName
	}

	data.Data["PlatformAPIURL"] = apiurl

	manifests, err := render.RenderDir(filepath.Join(manifestDir, "cloud-network-config-controller"), &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render cloud-network-config-controller manifests")
	}

	// Generate the silly AWS CA override
	cm := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "openshift-cloud-network-config-controller",
			Name:      "kube-cloud-config",
		},
		Data: cloudBootstrapResult.KubeCloudConfig,
	}
	obj, err := k8sutil.ToUnstructured(cm)
	if err != nil {
		return nil, errors.Wrap(err, "failed to transmute")
	}
	manifests = k8sutil.ReplaceObj(manifests, obj)

	return manifests, nil
}
