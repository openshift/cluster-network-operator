package platform

import (
	"context"
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	types "k8s.io/apimachinery/pkg/types"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

var cloudProviderConfig = types.NamespacedName{
	Namespace: "openshift-config-managed",
	Name:      "kube-cloud-config",
}

func BootstrapInfra(kubeClient crclient.Client) (*bootstrap.InfraBootstrapResult, error) {
	infraConfig := &configv1.Infrastructure{}
	if err := kubeClient.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, infraConfig); err != nil {
		return nil, fmt.Errorf("failed to get infrastructure 'cluster': %v", err)
	}

	res := &bootstrap.InfraBootstrapResult{
		PlatformType:         infraConfig.Status.PlatformStatus.Type,
		PlatformStatus:       infraConfig.Status.PlatformStatus,
		ExternalControlPlane: infraConfig.Status.ControlPlaneTopology == configv1.ExternalTopologyMode,
	}

	if res.PlatformType == configv1.AWSPlatformType {
		res.PlatformRegion = infraConfig.Status.PlatformStatus.AWS.Region
	} else if res.PlatformType == configv1.GCPPlatformType {
		res.PlatformRegion = infraConfig.Status.PlatformStatus.GCP.Region
	}

	// AWS specifies a CA bundle via a config map; retrieve it.
	if res.PlatformType == configv1.AWSPlatformType {
		cm := &corev1.ConfigMap{}
		if err := kubeClient.Get(context.TODO(), cloudProviderConfig, cm); err != nil {
			if !apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("failed to retrieve ConfigMap %s: %w", cloudProviderConfig, err)
			}
		} else {
			res.KubeCloudConfig = cm.Data
		}
	}

	return res, nil
}
