package platform

import (
	"context"
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	types "k8s.io/apimachinery/pkg/types"
)

var cloudProviderConfig = types.NamespacedName{
	Namespace: "openshift-config-managed",
	Name:      "kube-cloud-config",
}

func BootstrapInfra(client *cnoclient.Client) (*bootstrap.InfraBootstrapResult, error) {
	infraConfig := &configv1.Infrastructure{}
	if err := client.Default().CRClient().Get(context.TODO(), types.NamespacedName{Name: "cluster"}, infraConfig); err != nil {
		return nil, fmt.Errorf("failed to get infrastructure 'cluster': %v", err)
	}

	apis := map[string]bootstrap.APIServer{}
	for name, cclient := range client.Clusters() {
		h, p := cclient.HostPort()
		apis[name] = bootstrap.APIServer{
			Host: h,
			Port: p,
		}
	}

	res := &bootstrap.InfraBootstrapResult{
		PlatformType:         infraConfig.Status.PlatformStatus.Type,
		PlatformStatus:       infraConfig.Status.PlatformStatus,
		ExternalControlPlane: infraConfig.Status.ControlPlaneTopology == configv1.ExternalTopologyMode,
		APIServers:           apis,
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
