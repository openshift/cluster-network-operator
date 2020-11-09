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
	PodsNetworkMTU           uint32
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
	HttpsProxy               string
	HttpProxy                string
	NoProxy                  string
}

type OVNConfigBoostrapResult struct {
	GatewayMode            string
	NodeMode               string
	EnableEgressIP         bool
	DisableSNATMultipleGWs bool
}

type OVNBootstrapResult struct {
	MasterIPs               []string
	ClusterInitiator        string
	ExistingMasterDaemonset *appsv1.DaemonSet
	ExistingNodeDaemonset   *appsv1.DaemonSet
	Platform                configv1.PlatformType
	OVNKubernetesConfig     *OVNConfigBoostrapResult
	PrePullerDaemonset      *appsv1.DaemonSet
}

type BootstrapResult struct {
	Kuryr KuryrBootstrapResult
	OVN   OVNBootstrapResult
	SDN   SDNBootstrapResult

	ExternalControlPlane bool
}

type SDNBootstrapResult struct {
	Platform configv1.PlatformType
}
