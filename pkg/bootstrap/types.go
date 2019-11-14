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
	ExternalNetwork   string
	ClusterID         string
	OpenStackCloud    clientconfig.Cloud
	WebhookCA         string
	WebhookCAKey      string
	WebhookCert       string
	WebhookKey        string
}

type OVNBootstrapResult struct {
	OVNMasterNodes   []string
	OVNMasterNodeIPs []string
}

type BootstrapResult struct {
	Kuryr KuryrBootstrapResult
	OVN   OVNBootstrapResult
}
