package platform

import (
	"context"
	"fmt"
	"os"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/names"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	types "k8s.io/apimachinery/pkg/types"
)

var cloudProviderConfig = types.NamespacedName{
	Namespace: "openshift-config-managed",
	Name:      "kube-cloud-config",
}

func InfraStatus(client cnoclient.Client) (*bootstrap.InfraStatus, error) {
	infraConfig := &configv1.Infrastructure{}
	if err := client.Default().CRClient().Get(context.TODO(), types.NamespacedName{Name: "cluster"}, infraConfig); err != nil {
		return nil, fmt.Errorf("failed to get infrastructure 'cluster': %v", err)
	}

	res := &bootstrap.InfraStatus{
		PlatformType:         infraConfig.Status.PlatformStatus.Type,
		PlatformStatus:       infraConfig.Status.PlatformStatus,
		ExternalControlPlane: infraConfig.Status.ControlPlaneTopology == configv1.ExternalTopologyMode,
		APIServers:           map[string]bootstrap.APIServer{},
	}

	// If we use hypershift, the components need proxy settings. In selfhosted, they
	// are always local to the API so there it is never needed.
	if res.ExternalControlPlane {
		proxy := &configv1.Proxy{}
		if err := client.Default().CRClient().Get(context.TODO(), types.NamespacedName{Name: "cluster"}, proxy); err != nil {
			return nil, fmt.Errorf("failed to get proxy 'cluster': %w", err)
		}
		res.Proxy = proxy.Status
	}

	// Extract apiserver URLs from the kubeconfig(s) passed to the CNO
	for name, c := range client.Clients() {
		h, p := c.HostPort()
		res.APIServers[name] = bootstrap.APIServer{
			Host: h,
			Port: p,
		}
	}

	// default-local defines how the CNO connects to the APIServer. So, just copy from Default
	res.APIServers[bootstrap.APIServerDefaultLocal] = res.APIServers[bootstrap.APIServerDefault]

	// Allow overriding the "default" apiserver via the environment var APISERVER_OVERRIDE_HOST / _PORT
	// This is used by Hypershift, since the cno connects to a "local" ServiceIP, but rendered manifests
	// that run on a hosted cluster need to talk to the external URL
	if h := os.Getenv(names.EnvApiOverrideHost); h != "" {
		p := os.Getenv(names.EnvApiOverridePort)
		if p == "" {
			p = "443"
		}

		res.APIServers[bootstrap.APIServerDefault] = bootstrap.APIServer{
			Host: h,
			Port: p,
		}
	}

	if res.PlatformType == configv1.AWSPlatformType {
		res.PlatformRegion = infraConfig.Status.PlatformStatus.AWS.Region
	} else if res.PlatformType == configv1.GCPPlatformType {
		res.PlatformRegion = infraConfig.Status.PlatformStatus.GCP.Region
	}

	// AWS specifies a CA bundle via a config map; retrieve it.
	if res.PlatformType == configv1.AWSPlatformType {
		cm := &corev1.ConfigMap{}
		if err := client.Default().CRClient().Get(context.TODO(), cloudProviderConfig, cm); err != nil {
			if !apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("failed to retrieve ConfigMap %s: %w", cloudProviderConfig, err)
			}
		} else {
			res.KubeCloudConfig = cm.Data
		}
	}

	return res, nil
}
