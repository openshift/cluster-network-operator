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
	OctaviaProvider   string
	OpenStackCloud    clientconfig.Cloud
	WebhookCA         string
	WebhookCAKey      string
	WebhookCert       string
	WebhookKey        string
}

type BootstrapResult struct {
	Kuryr KuryrBootstrapResult
}
