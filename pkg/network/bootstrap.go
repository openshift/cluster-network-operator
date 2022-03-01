package network

import (
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"github.com/openshift/cluster-network-operator/pkg/platform/openstack"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	operv1 "github.com/openshift/api/operator/v1"
)

// Bootstrap creates resources required by SDN on the cloud.
func Bootstrap(conf *operv1.Network, client crclient.Client) (*bootstrap.BootstrapResult, error) {
	switch conf.Spec.DefaultNetwork.Type {
	case operv1.NetworkTypeKuryr:
		return openstack.BootstrapKuryr(&conf.Spec, client)
	case operv1.NetworkTypeOpenShiftSDN:
		return bootstrapSDN(conf, client)
	case operv1.NetworkTypeOVNKubernetes:
		return bootstrapOVN(conf, client)
	}

	return &bootstrap.BootstrapResult{}, nil
}
