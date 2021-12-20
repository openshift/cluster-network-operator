package platform

import (
	"context"
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	types "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func BootstrapInfra(kubeClient client.Client) (*bootstrap.InfraBootstrapResult, error) {
	var platformType configv1.PlatformType
	var platformRegion string

	infraConfig := &configv1.Infrastructure{}
	if err := kubeClient.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, infraConfig); err != nil {
		return nil, fmt.Errorf("failed to get infrastructure 'cluster': %v", err)
	}

	if infraConfig.Status.PlatformStatus.Type != "" {
		platformType = infraConfig.Status.PlatformStatus.Type
	}
	if platformType == configv1.AWSPlatformType {
		platformRegion = infraConfig.Status.PlatformStatus.AWS.Region
	} else if platformType == configv1.GCPPlatformType {
		platformRegion = infraConfig.Status.PlatformStatus.GCP.Region
	}

	res := &bootstrap.InfraBootstrapResult{
		PlatformType:         platformType,
		PlatformRegion:       platformRegion,
		ExternalControlPlane: infraConfig.Status.ControlPlaneTopology == configv1.ExternalTopologyMode,
	}
	return res, nil
}
