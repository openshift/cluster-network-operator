package bootstrap

import (
	"github.com/gophercloud/utils/openstack/clientconfig"

	configv1 "github.com/openshift/api/config/v1"
	appsv1 "k8s.io/api/apps/v1"
)

type KuryrBootstrapResult struct {
	ServiceSubnet            string
	PodSubnetpool            string
	WorkerNodesRouter        string
	WorkerNodesSubnets       []string
	NodesNetworkMTU          int
	PodSecurityGroups        []string
	ExternalNetwork          string
	ClusterID                string
	OctaviaProvider          string
	OctaviaMultipleListeners bool
	OctaviaVersion           string
	OpenStackCloud           clientconfig.Cloud
	WebhookCA                string
	WebhookCAKey             string
	WebhookCert              string
	WebhookKey               string
	UserCACert               string
}

type OVNBootstrapResult struct {
	MasterIPs               []string
	ClusterInitiator        string
	ExistingMasterDaemonset *appsv1.DaemonSet
	ExistingNodeDaemonset   *appsv1.DaemonSet
	ExistingIPsecDaemonset  *appsv1.DaemonSet
	GatewayMode             string
	Platform                configv1.PlatformType
}

type BootstrapResult struct {
	Kuryr KuryrBootstrapResult
	OVN   OVNBootstrapResult
	SDN   SDNBootstrapResult
}

type SDNBootstrapResult struct {
	Platform configv1.PlatformType
}
