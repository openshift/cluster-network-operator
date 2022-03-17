package network

import (
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/platform"
	"github.com/openshift/cluster-network-operator/pkg/platform/openstack"

	operv1 "github.com/openshift/api/operator/v1"
)

// Bootstrap creates resources required by SDN on the cloud.
func Bootstrap(conf *operv1.Network, client cnoclient.Client) (*bootstrap.BootstrapResult, error) {
	out := &bootstrap.BootstrapResult{}

	i, err := platform.InfraStatus(client)
	if err != nil {
		return nil, err
	}
	out.Infra = *i

	switch conf.Spec.DefaultNetwork.Type {
	case operv1.NetworkTypeKuryr:
		k, err := openstack.BootstrapKuryr(&conf.Spec, client.Default().CRClient())
		if err != nil {
			return nil, err
		}
		out.Kuryr = *k
	case operv1.NetworkTypeOVNKubernetes:
		o, err := bootstrapOVN(conf, client)
		if err != nil {
			return nil, err
		}
		out.OVN = *o
	}

	return out, nil
}
