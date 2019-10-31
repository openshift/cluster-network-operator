package network

import (
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"github.com/openshift/cluster-network-operator/pkg/platform/openstack"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operv1 "github.com/openshift/api/operator/v1"
)

// Bootstrap creates resources required by SDN on the cloud.
func Bootstrap(conf *operv1.NetworkSpec, client client.Client) (*bootstrap.BootstrapResult, error) {
	switch conf.DefaultNetwork.Type {
	case operv1.NetworkTypeKuryr:
		return openstack.BootstrapKuryr(conf, client)
	case operv1.NetworkTypeOpenShiftSDN:
		return nil, nil
	case operv1.NetworkTypeOVNKubernetes:
		return boostrapOVN(client)
	}

	return nil, nil
}
