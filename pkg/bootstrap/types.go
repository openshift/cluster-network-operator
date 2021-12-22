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
	GatewayMode string
	NodeMode    string
}

type OVNBootstrapResult struct {
	MasterIPs               []string
	ClusterInitiator        string
	ExistingMasterDaemonset *appsv1.DaemonSet
	ExistingNodeDaemonset   *appsv1.DaemonSet
	OVNKubernetesConfig     *OVNConfigBoostrapResult
	PrePullerDaemonset      *appsv1.DaemonSet
	FlowsConfig             *FlowsConfig
}

type BootstrapResult struct {
	Kuryr KuryrBootstrapResult
	OVN   OVNBootstrapResult
	Infra InfraBootstrapResult
}

type InfraBootstrapResult struct {
	PlatformType         configv1.PlatformType
	PlatformRegion       string
	ExternalControlPlane bool
}

type FlowsConfig struct {
	// Target IP:port of the flow collector
	Target string

	// CacheActiveTimeout is the max period, in seconds, during which the reporter will aggregate flows before sending
	CacheActiveTimeout *uint

	// CacheMaxFlows is the max number of flows in an aggregate; when reached, the reporter sends the flows
	CacheMaxFlows *uint

	// Sampling is the sampling rate on the reporter. 100 means one flow on 100 is sent. 0 means disabled.
	Sampling *uint
}
