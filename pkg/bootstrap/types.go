package bootstrap

import (
	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"

	"github.com/openshift/cluster-network-operator/pkg/hypershift"
)

type OVNHyperShiftBootstrapResult struct {
	Enabled              bool
	ClusterID            string
	Namespace            string
	RunAsUser            string
	HCPNodeSelector      map[string]string
	HCPLabels            map[string]string
	HCPTolerations       []string
	ControlPlaneReplicas int
	ReleaseImage         string
	ControlPlaneImage    string
	CAConfigMap          string
	CAConfigMapKey       string
}

type OVNConfigBoostrapResult struct {
	GatewayMode           string
	HyperShiftConfig      *OVNHyperShiftBootstrapResult
	DisableUDPAggregation bool
	DpuHostModeLabel      string
	DpuHostModeNodes      []string
	DpuModeLabel          string
	DpuModeNodes          []string
	SmartNicModeLabel     string
	SmartNicModeNodes     []string
	MgmtPortResourceName  string
}

// OVNUpdateStatus contains the status of existing daemonset
// or deployment that are maily used by the upgrade logic
type OVNUpdateStatus struct {
	Kind                string
	Namespace           string
	Name                string
	Version             string
	IPFamilyMode        string
	ClusterNetworkCIDRs string
	Progressing         bool
}

// OVNIPsecStatus contains status of current IPsec configuration
// in the cluster.
type OVNIPsecStatus struct {
	// IsOVNIPsecActiveOrRollingOut set to true unless we are sure it is not. Note that this is
	// set to true when ovnkube-node daemonset is in progressing state which is not reflecting
	// actual ovn ipsec state. so must be precautious in making decisions at the time of machine
	// configs rollout and node reboot scenarios.
	IsOVNIPsecActiveOrRollingOut bool
}

type OVNBootstrapResult struct {
	// ControlPlaneReplicaCount represents the number of control plane nodes in the cluster
	ControlPlaneReplicaCount int
	// ControlPlaneUpdateStatus is the status of ovnkube-control-plane deployment
	ControlPlaneUpdateStatus *OVNUpdateStatus
	// NodeUpdateStatus is the status of ovnkube-node daemonset
	NodeUpdateStatus *OVNUpdateStatus
	// IPsecUpdateStatus is the status of ovn-ipsec config
	IPsecUpdateStatus *OVNIPsecStatus
	// PrePullerUpdateStatus is the status of ovnkube-upgrades-prepuller daemonset
	PrePullerUpdateStatus     *OVNUpdateStatus
	OVNKubernetesConfig       *OVNConfigBoostrapResult
	FlowsConfig               *FlowsConfig
	DefaultV4MasqueradeSubnet string
	DefaultV6MasqueradeSubnet string
}

// IPTablesAlerterBootstrapResult contains configuration for the iptables-alerter
type IPTablesAlerterBootstrapResult struct {
	// Enabled is true if the iptables-alerter should be enabled
	Enabled bool
}

type BootstrapResult struct {
	Infra InfraStatus

	OVN             OVNBootstrapResult
	IPTablesAlerter IPTablesAlerterBootstrapResult
}

type InfraStatus struct {
	PlatformType           configv1.PlatformType
	PlatformRegion         string
	PlatformStatus         *configv1.PlatformStatus
	ControlPlaneTopology   configv1.TopologyMode
	InfrastructureTopology configv1.TopologyMode
	InfraName              string

	// KubeCloudConfig is the contents of the openshift-config-managed/kube-cloud-config ConfigMap
	KubeCloudConfig map[string]string

	// URLs to the apiservers. This is because we can't use the default in-cluster one (they assume a running service network)
	APIServers map[string]APIServer

	// Proxy settings to use for all communication to the KAS
	Proxy configv1.ProxyStatus

	// HostedControlPlane defines the hosted control plane, only used in HyperShift
	HostedControlPlane *hypershift.HostedControlPlane

	// NetworkNodeIdentityEnabled define if the network node identity feature should be enabled
	NetworkNodeIdentityEnabled bool

	// MasterIPsecMachineConfigs contains ipsec machine config objects of master nodes.
	MasterIPsecMachineConfigs []*mcfgv1.MachineConfig

	// WorkerIPsecMachineConfigs contains ipsec machine config objects of worker nodes.
	WorkerIPsecMachineConfigs []*mcfgv1.MachineConfig

	// MasterMCPStatus contains machine config pool statuses for pools having master role.
	MasterMCPStatuses []mcfgv1.MachineConfigPoolStatus

	// WorkerMCPStatus contains machine config pool statuses for pools having worker role.
	WorkerMCPStatuses []mcfgv1.MachineConfigPoolStatus

	// MachineConfigClusterOperatorReady set to true when Machine Config cluster operator is in ready state.
	MachineConfigClusterOperatorReady bool

	// ConsolePluginCRDExists set to true when the consoleplugins.console.openshift.io has been deployed.
	ConsolePluginCRDExists bool
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
