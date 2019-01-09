package bootstrap

import (
	"github.com/gophercloud/utils/openstack/clientconfig"
)

type KuryrBootstrapResult struct {
	ServiceSubnet     string
	PodSubnetpool     string
	WorkerNodesRouter string
	WorkerNodesSubnet string
	PodSecurityGroups []string
	ClusterID         string
	OpenStackCloud    clientconfig.Cloud
}

type BootstrapResult struct {
	Kuryr KuryrBootstrapResult
}
