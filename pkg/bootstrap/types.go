package bootstrap

import (
	"github.com/gophercloud/utils/openstack/clientconfig"
	configv1 "github.com/openshift/api/config/v1"
	hyperv1 "github.com/openshift/hypershift/api/v1alpha1"
)

type KuryrBootstrapResult struct {
	ServiceSubnet      string
	PodSubnetpool      string
	WorkerNodesRouter  string
	WorkerNodesSubnets []string
	PodsNetworkMTU     uint32
	PodSecurityGroups  []string
	ExternalNetwork    string
	ClusterID          string
	OctaviaProvider    string
	OctaviaVersion     string
	OpenStackCloud     clientconfig.Cloud
	UserCACert         string
	HttpsProxy         string
	HttpProxy          string
	NoProxy            string
}

type OVNHyperShiftBootstrapResult struct {
	Enabled                   bool
	ClusterID                 string
	Namespace                 string
	ServicePublishingStrategy *hyperv1.ServicePublishingStrategy
	OVNSbDbRouteHost          string
	OVNSbDbRouteNodePort      int32
	ControlPlaneReplicas      int
}

type OVNConfigBoostrapResult struct {
	GatewayMode      string
	NodeMode         string
	HyperShiftConfig *OVNHyperShiftBootstrapResult
}

// OVNUpdateStatus contains the status of existing daemonset
// or statefulset that are maily used by upgrade process
type OVNUpdateStatus struct {
	Kind         string
	Namespace    string
	Name         string
	Version      string
	IPFamilyMode string
	Progressing  bool
}

type OVNBootstrapResult struct {
	MasterAddresses  []string
	ClusterInitiator string
	// MasterUpdateStatus is the status of ovnkube-master daemonset or statefulset (when hypershift is enabled)
	MasterUpdateStatus *OVNUpdateStatus
	// NodeUpdateStatus is the status of ovnkube-node daemonset
	NodeUpdateStatus *OVNUpdateStatus
	// IPsecUpdateStatus is the status of ovn-ipsec daemonset
	IPsecUpdateStatus *OVNUpdateStatus
	// PrePullerUpdateStatus is the status of ovnkube-upgrades-prepuller daemonset
	PrePullerUpdateStatus *OVNUpdateStatus
	OVNKubernetesConfig   *OVNConfigBoostrapResult
	FlowsConfig           *FlowsConfig
}

type BootstrapResult struct {
	Kuryr KuryrBootstrapResult
	OVN   OVNBootstrapResult
	Infra InfraStatus
}

type InfraStatus struct {
	PlatformType         configv1.PlatformType
	PlatformRegion       string
	PlatformStatus       *configv1.PlatformStatus
	ExternalControlPlane bool

	// KubeCloudConfig is the contents of the openshift-config-managed/kube-cloud-config ConfigMap
	KubeCloudConfig map[string]string

	// URLs to the apiservers. This is because we can't use the default in-cluster one (they assume a running service network)
	APIServers map[string]APIServer

	// Proxy settings to use for all communication to the KAS
	Proxy configv1.ProxyStatus
}

// APIServer is the hostname & port of a given APIServer. (This is the
// load-balancer or other "real" address, not the ServiceIP).
type APIServer struct {
	Host string
	Port string
}

// APIServerDefault is the key in to APIServers for the cluster's APIserver
// This **always** declares how manifests inside the cluster should reference it
// In other words, for Hypershift clusters, it is the url to the gateway / route / proxy
// for standard clusters, it is the internal ALB. It is never a service IP
const APIServerDefault = "default"

// APIServerDefaultLocal is the key in to APIServer that is local to the CNO.
// This describes how to talk to the apiserver in the same way the CNO does it. For hypershift,
// this might be a ServiceIP that is only valid inside the management cluster.
const APIServerDefaultLocal = "default-local"

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
