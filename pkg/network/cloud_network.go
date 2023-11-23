package network

import (
	"os"
	"path/filepath"

	"github.com/openshift/cluster-network-operator/pkg/hypershift"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/pkg/errors"

	configv1 "github.com/openshift/api/config/v1"
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
func renderCloudNetworkConfigController(conf *operv1.NetworkSpec, bootstrapResult *bootstrap.BootstrapResult, manifestDir string) ([]*uns.Unstructured, error) {
	cloudBootstrapResult := bootstrapResult.Infra
	pt := cloudBootstrapResult.PlatformType

	// Do not render the CNCC for platforms that the CNCC does not support.
	if !(pt == v1.AWSPlatformType || pt == v1.AzurePlatformType || pt == v1.GCPPlatformType || pt == v1.OpenStackPlatformType) {
		return nil, nil
	}
	// Do not render the CNCC for network plugins that do not support the CNCC.
	if conf.DefaultNetwork.Type != operv1.NetworkTypeOpenShiftSDN && conf.DefaultNetwork.Type != operv1.NetworkTypeOVNKubernetes {
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
	data.Data["KubernetesServiceHost"] = cloudBootstrapResult.APIServers[bootstrap.APIServerDefaultLocal].Host
	data.Data["KubernetesServicePort"] = cloudBootstrapResult.APIServers[bootstrap.APIServerDefaultLocal].Port
	data.Data["ExternalControlPlane"] = cloudBootstrapResult.ControlPlaneTopology == configv1.ExternalTopologyMode
	data.Data["PlatformAzureEnvironment"] = ""
	data.Data["PlatformAWSCAPath"] = ""

	// AWS and azure allow for funky endpoint overriding.
	// in different ways, of course.
	apiurl := ""
	// Needed for AWS and OpenStack CA override
	caOverride := &corev1.ConfigMap{
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

	manifestDirs := make([]string, 0, 2)
	manifestDirs = append(manifestDirs, filepath.Join(manifestDir, "cloud-network-config-controller/common"))
	if hcpCfg := hypershift.NewHyperShiftConfig(); hcpCfg.Enabled {
		data.Data["CLIImage"] = os.Getenv("CLI_IMAGE")
		data.Data["TokenMinterImage"] = os.Getenv("TOKEN_MINTER_IMAGE")
		data.Data["TokenAudience"] = os.Getenv("TOKEN_AUDIENCE")
		data.Data["ManagementClusterName"] = names.ManagementClusterName
		data.Data["HostedClusterNamespace"] = hcpCfg.Namespace
		data.Data["ReleaseImage"] = hcpCfg.ReleaseImage
		data.Data["HCPNodeSelector"] = cloudBootstrapResult.HostedControlPlane.Spec.NodeSelector
		// In HyperShift CloudNetworkConfigController is deployed as a part of the hosted cluster controlplane
		// which means that it is created in the management cluster.
		// CloudNetworkConfigController should use the proxy settings configured by hypershift controlplane operator
		// on CNO deployment.
		data.Data["HTTP_PROXY"] = os.Getenv("MGMT_HTTP_PROXY")
		data.Data["HTTPS_PROXY"] = os.Getenv("MGMT_HTTPS_PROXY")
		data.Data["NO_PROXY"] = os.Getenv("MGMT_NO_PROXY")
		caOverride.ObjectMeta = metav1.ObjectMeta{
			Namespace:   hcpCfg.Namespace,
			Name:        "cloud-network-config-controller-kube-cloud-config",
			Annotations: map[string]string{names.ClusterNameAnnotation: names.ManagementClusterName},
		}
		manifestDirs = append(manifestDirs, filepath.Join(manifestDir, "cloud-network-config-controller/managed"))
	} else {
		data.Data["HTTP_PROXY"] = cloudBootstrapResult.Proxy.HTTPProxy
		data.Data["HTTPS_PROXY"] = cloudBootstrapResult.Proxy.HTTPSProxy
		data.Data["NO_PROXY"] = cloudBootstrapResult.Proxy.NoProxy
		manifestDirs = append(manifestDirs, filepath.Join(manifestDir, "cloud-network-config-controller/self-hosted"))
	}

	manifests, err := render.RenderDirs(manifestDirs, &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render cloud-network-config-controller manifests")
	}

	obj, err := k8sutil.ToUnstructured(caOverride)
	if err != nil {
		return nil, errors.Wrap(err, "failed to transmute")
	}
	manifests = k8sutil.ReplaceObj(manifests, obj)

	return manifests, nil
}
