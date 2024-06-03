package network

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"

	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/hypershift"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/render"
	"github.com/openshift/cluster-network-operator/pkg/util/k8s"
	"github.com/openshift/cluster-network-operator/pkg/util/validation"
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	utilnet "k8s.io/utils/net"
)

const NetworkNodeIdentityWebhookPort = "9743"
const NetworkNodeIdentityNamespace = "openshift-network-node-identity"

// isBootstrapComplete checks whether the bootstrap phase of openshift installation completed
func isBootstrapComplete(cli cnoclient.Client) (bool, error) {
	clusterBootstrap := &corev1.ConfigMap{}
	clusterBootstrapLookup := types.NamespacedName{Name: "bootstrap", Namespace: CLUSTER_CONFIG_NAMESPACE}
	if err := cli.ClientFor("").CRClient().Get(context.TODO(), clusterBootstrapLookup, clusterBootstrap); err != nil {
		if !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("unable to bootstrap OVN, unable to retrieve cluster config: %s", err)
		}
	}
	status, ok := clusterBootstrap.Data["status"]
	if ok {
		return status == "complete", nil
	}
	klog.Warningf("no status found in bootstrap configmap")
	return false, nil
}

// renderNetworkNodeIdentity renders the network node identity component
func renderNetworkNodeIdentity(conf *operv1.NetworkSpec, bootstrapResult *bootstrap.BootstrapResult, manifestDir string, client cnoclient.Client) ([]*uns.Unstructured, error) {
	if !bootstrapResult.Infra.NetworkNodeIdentityEnabled {
		klog.Infof("Network node identity is disabled")
		return nil, nil
	}
	data := render.MakeRenderData()
	data.Data["ReleaseVersion"] = os.Getenv("RELEASE_VERSION")
	data.Data["OVNHybridOverlayEnable"] = false
	if conf.DefaultNetwork.OVNKubernetesConfig != nil {
		data.Data["OVNHybridOverlayEnable"] = conf.DefaultNetwork.OVNKubernetesConfig.HybridOverlayConfig != nil
	}
	data.Data["NetworkNodeIdentityPort"] = NetworkNodeIdentityWebhookPort

	manifestDirs := make([]string, 0, 2)
	manifestDirs = append(manifestDirs, filepath.Join(manifestDir, "network/node-identity/common"))

	clusterBootstrapFinished := true
	webhookCAConfigMap := &corev1.ConfigMap{}
	webhookCAClient := client.Default()
	webhookCALookup := types.NamespacedName{Name: "network-node-identity-ca", Namespace: NetworkNodeIdentityNamespace}
	caKey := "ca-bundle.crt"

	data.Data["ConfigureNodeAdmissionWebhook"] = false
	if conf.DefaultNetwork.Type == operv1.NetworkTypeOVNKubernetes {
		data.Data["ConfigureNodeAdmissionWebhook"] = true
	}

	webhookReady := false
	// HyperShift specific
	if hcpCfg := hypershift.NewHyperShiftConfig(); hcpCfg.Enabled {
		webhookCAClient = client.ClientFor(names.ManagementClusterName)

		data.Data["CAConfigMap"] = hcpCfg.CAConfigMap
		data.Data["CAConfigMapKey"] = hcpCfg.CAConfigMapKey

		webhookCALookup = types.NamespacedName{Name: hcpCfg.CAConfigMap, Namespace: hcpCfg.Namespace}
		caKey = hcpCfg.CAConfigMapKey

		data.Data["HostedClusterNamespace"] = hcpCfg.Namespace
		data.Data["ManagementClusterName"] = names.ManagementClusterName
		data.Data["NetworkNodeIdentityReplicas"] = 1
		if bootstrapResult.Infra.HostedControlPlane.ControllerAvailabilityPolicy == hypershift.HighlyAvailable {
			data.Data["NetworkNodeIdentityReplicas"] = 3
		}
		data.Data["ReleaseImage"] = hcpCfg.ReleaseImage
		data.Data["CLIImage"] = os.Getenv("CLI_IMAGE")
		data.Data["TokenMinterImage"] = os.Getenv("TOKEN_MINTER_IMAGE")
		data.Data["TokenAudience"] = os.Getenv("TOKEN_AUDIENCE")
		data.Data["HCPNodeSelector"] = bootstrapResult.Infra.HostedControlPlane.NodeSelector
		data.Data["NetworkNodeIdentityImage"] = hcpCfg.ControlPlaneImage // OVN_CONTROL_PLANE_IMAGE
		localAPIServer := bootstrapResult.Infra.APIServers[bootstrap.APIServerDefaultLocal]
		data.Data["K8S_LOCAL_APISERVER"] = "https://" + net.JoinHostPort(localAPIServer.Host, localAPIServer.Port)

		webhookDeployment := &appsv1.Deployment{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Deployment",
				APIVersion: appsv1.SchemeGroupVersion.String(),
			},
		}
		nsn := types.NamespacedName{Namespace: hcpCfg.Namespace, Name: "network-node-identity"}
		if err := client.ClientFor(names.ManagementClusterName).CRClient().Get(context.TODO(), nsn, webhookDeployment); err != nil {
			if !apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("failed to retrieve existing network-node-identity deployment: %w", err)
			} else {
				klog.Infof("network-node-identity deployment does not exist")
			}
		} else {
			webhookReady = !deploymentProgressing(webhookDeployment)
		}

		manifestDirs = append(manifestDirs, filepath.Join(manifestDir, "network/node-identity/managed"))
	} else {
		// self-hosted specific
		data.Data["NetworkNodeIdentityImage"] = os.Getenv("OVN_IMAGE")

		// NetworkNodeIdentityTerminationDurationSeconds holds the allowed termination duration
		// During node reboot, the webhook has to wait for the API server to terminate first to avoid disruptions
		data.Data["NetworkNodeIdentityTerminationDurationSeconds"] = 200

		apiServer := bootstrapResult.Infra.APIServers[bootstrap.APIServerDefault]
		data.Data["K8S_APISERVER"] = "https://" + net.JoinHostPort(apiServer.Host, apiServer.Port)

		// NetworkNodeIdentityIP/NetworkNodeIdentityAddress are only used in self-hosted deployments where the webhook listens on loopback
		// listening on localhost always picks the v4 address while dialing to localhost can choose either one
		// https://github.com/golang/go/issues/9334
		// For that reason set the webhook address use the loopback address of the primary IP family
		// Note: ServiceNetwork cannot be empty, so it is safe to use the first element
		networkNodeIdentityIP := "127.0.0.1"
		if utilnet.IsIPv6CIDRString(conf.ServiceNetwork[0]) {
			networkNodeIdentityIP = "::1"
		}
		data.Data["NetworkNodeIdentityIP"] = networkNodeIdentityIP
		data.Data["NetworkNodeIdentityAddress"] = net.JoinHostPort(networkNodeIdentityIP, NetworkNodeIdentityWebhookPort)

		var err error
		clusterBootstrapFinished, err = isBootstrapComplete(client)
		if err != nil {
			return nil, err
		}

		webhookDaemonSet := &appsv1.DaemonSet{
			TypeMeta: metav1.TypeMeta{
				Kind:       "DaemonSet",
				APIVersion: appsv1.SchemeGroupVersion.String(),
			},
		}
		nsn := types.NamespacedName{Namespace: NetworkNodeIdentityNamespace, Name: "network-node-identity"}
		if err := client.Default().CRClient().Get(context.TODO(), nsn, webhookDaemonSet); err != nil {
			if !apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("failed to retrieve existing network-node-identity daemonset: %w", err)
			} else {
				klog.Infof("network-node-identity daemonset does not exist")
			}
		} else {
			webhookReady = !daemonSetProgressing(webhookDaemonSet, false)
		}

		manifestDirs = append(manifestDirs, filepath.Join(manifestDir, "network/node-identity/self-hosted"))
	}

	var webhookCA []byte
	if err := webhookCAClient.CRClient().Get(context.TODO(), webhookCALookup, webhookCAConfigMap); err != nil {
		// If the CA doesn't exist, the ValidatingWebhookConfiguration will not be rendered
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("unable to retrieve ovnkube-identity webhook CA config: %s", err)
		}
	} else {
		_, webhookCA, err = validation.TrustBundleConfigMap(webhookCAConfigMap, caKey)
		if err != nil {
			return nil, err
		}
	}
	data.Data["NetworkNodeIdentityCABundle"] = base64.URLEncoding.EncodeToString(webhookCA)

	manifests, err := render.RenderDirs(manifestDirs, &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render network-node-identity manifests")
	}

	applyWebhook := true
	if !clusterBootstrapFinished {
		applyWebhook = false
		klog.Infof("network-node-identity webhook will not be applied, bootstrap is not complete")
	}
	if len(webhookCA) == 0 {
		applyWebhook = false
		klog.Infof("network-node-identity webhook will not be applied, CA bundle not found")
	}

	// This is useful only when upgrading from a version that didn't enable the webhook
	// because marking an existing webhook config with CreateWaitAnnotation won't remove it
	if !webhookReady {
		applyWebhook = false
		klog.Infof("network-node-identity webhook will not be applied, the deployment/daemonset is not ready")
	}

	if !applyWebhook {
		klog.Infof("network-node-identity webhook will not be applied, if it already exists it won't be removed")
		k8s.UpdateObjByGroupKindName(manifests, "admissionregistration.k8s.io", "ValidatingWebhookConfiguration", "", "network-node-identity.openshift.io", func(o *uns.Unstructured) {
			anno := o.GetAnnotations()
			if anno == nil {
				anno = map[string]string{}
			}
			anno[names.CreateWaitAnnotation] = "true"
			o.SetAnnotations(anno)
		})
	}
	return manifests, nil
}
