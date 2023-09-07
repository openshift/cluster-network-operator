package network

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	yaml "github.com/ghodss/yaml"
	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/platform"
	"github.com/openshift/cluster-network-operator/pkg/render"
	"github.com/openshift/cluster-network-operator/pkg/util"
	iputil "github.com/openshift/cluster-network-operator/pkg/util/ip"
	"github.com/openshift/cluster-network-operator/pkg/util/k8s"
	hyperv1 "github.com/openshift/hypershift/api/v1beta1"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	utilnet "k8s.io/utils/net"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const OVN_NB_PORT = "9641"
const OVN_SB_PORT = "9642"
const OVN_NB_RAFT_PORT = "9643"
const OVN_SB_RAFT_PORT = "9644"
const CLUSTER_CONFIG_NAME = "cluster-config-v1"
const CLUSTER_CONFIG_NAMESPACE = "kube-system"
const OVN_CERT_CN = "ovn"
const OVN_MASTER_DISCOVERY_POLL = 5
const OVN_MASTER_DISCOVERY_BACKOFF = 120
const OVN_LOCAL_GW_MODE = "local"
const OVN_SHARED_GW_MODE = "shared"
const OVN_LOG_PATTERN_CONSOLE = "%D{%Y-%m-%dT%H:%M:%S.###Z}|%05N|%c%T|%p|%m"
const OVN_NODE_MODE_FULL = "full"
const OVN_NODE_MODE_DPU_HOST = "dpu-host"
const OVN_NODE_MODE_DPU = "dpu"
const OVN_NODE_MODE_SMART_NIC = "smart-nic"
const OVN_NODE_SELECTOR_DEFAULT_DPU_HOST = "network.operator.openshift.io/dpu-host"
const OVN_NODE_SELECTOR_DEFAULT_DPU = "network.operator.openshift.io/dpu"
const OVN_NODE_SELECTOR_DEFAULT_SMART_NIC = "network.operator.openshift.io/smart-nic"

// gRPC healthcheck port. See: https://github.com/openshift/enhancements/pull/1209
const OVN_EGRESSIP_HEALTHCHECK_PORT = "9107"

var OVN_MASTER_DISCOVERY_TIMEOUT = 250

const (
	// TODO: get this from the route Status
	OVN_SB_DB_ROUTE_PORT       = "443"
	OVN_SB_DB_ROUTE_LOCAL_PORT = "9645"
	OVSFlowsConfigMapName      = "ovs-flows-config"
	OVSFlowsConfigNamespace    = names.APPLIED_NAMESPACE
)

// renderOVNKubernetes returns the manifests for the ovn-kubernetes.
// This creates
// - the openshift-ovn-kubernetes namespace
// - the ovn-config ConfigMap
// - the ovnkube-node daemonset
// - the ovnkube-master deployment
// and some other small things.
func renderOVNKubernetes(conf *operv1.NetworkSpec, bootstrapResult *bootstrap.BootstrapResult, manifestDir string,
	client cnoclient.Client, featureGates featuregates.FeatureGate) ([]*uns.Unstructured, bool, error) {
	var progressing bool

	// TODO: Fix operator behavior when running in a cluster with an externalized control plane.
	// For now, return an error since we don't have any master nodes to run the ovn-master daemonset.
	externalControlPlane := bootstrapResult.Infra.ControlPlaneTopology == configv1.ExternalTopologyMode
	if externalControlPlane && !bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.Enabled {
		return nil, progressing, fmt.Errorf("Unable to render OVN in a cluster with an external control plane")
	}

	c := conf.DefaultNetwork.OVNKubernetesConfig

	objs := []*uns.Unstructured{}
	apiServer := bootstrapResult.Infra.APIServers[bootstrap.APIServerDefault]
	localAPIServer := bootstrapResult.Infra.APIServers[bootstrap.APIServerDefaultLocal]

	targetZoneMode, err := getTargetInterConnectZoneMode(client)
	if err != nil {
		return nil, progressing, fmt.Errorf("failed to render manifests, could not determine interconnect zone: %w", err)
	}

	err = prepareUpgradeToInterConnect(bootstrapResult.OVN, client, &targetZoneMode)
	if err != nil {
		return nil, progressing, fmt.Errorf("failed to render manifests: %w", err)
	}

	// render the manifests on disk
	data := render.MakeRenderData()
	data.Data["ReleaseVersion"] = os.Getenv("RELEASE_VERSION")
	data.Data["OvnImage"] = os.Getenv("OVN_IMAGE")
	data.Data["OvnControlPlaneImage"] = os.Getenv("OVN_IMAGE")
	if bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.Enabled {
		data.Data["OvnControlPlaneImage"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.ControlPlaneImage
	}
	data.Data["OvnkubeMasterReplicas"] = len(bootstrapResult.OVN.MasterAddresses)
	data.Data["KubeRBACProxyImage"] = os.Getenv("KUBE_RBAC_PROXY_IMAGE")
	data.Data["Socks5ProxyImage"] = os.Getenv("SOCKS5_PROXY_IMAGE")
	data.Data["KUBERNETES_SERVICE_HOST"] = apiServer.Host
	data.Data["KUBERNETES_SERVICE_PORT"] = apiServer.Port
	data.Data["K8S_APISERVER"] = "https://" + net.JoinHostPort(apiServer.Host, apiServer.Port)
	data.Data["K8S_LOCAL_APISERVER"] = "https://" + net.JoinHostPort(localAPIServer.Host, localAPIServer.Port)
	data.Data["HTTP_PROXY"] = ""
	data.Data["HTTPS_PROXY"] = ""
	data.Data["NO_PROXY"] = ""
	if bootstrapResult.Infra.ControlPlaneTopology == configv1.ExternalTopologyMode {
		data.Data["HTTP_PROXY"] = bootstrapResult.Infra.Proxy.HTTPProxy
		data.Data["HTTPS_PROXY"] = bootstrapResult.Infra.Proxy.HTTPSProxy
		data.Data["NO_PROXY"] = bootstrapResult.Infra.Proxy.NoProxy
	}

	data.Data["TokenMinterImage"] = os.Getenv("TOKEN_MINTER_IMAGE")
	// TOKEN_AUDIENCE is used by token-minter to identify the audience for the service account token which is verified by the apiserver
	data.Data["TokenAudience"] = os.Getenv("TOKEN_AUDIENCE")
	data.Data["MTU"] = c.MTU
	data.Data["RoutableMTU"] = nil
	data.Data["V4JoinSubnet"] = c.V4InternalSubnet
	data.Data["V6JoinSubnet"] = c.V6InternalSubnet
	data.Data["EnableUDPAggregation"] = !bootstrapResult.OVN.OVNKubernetesConfig.DisableUDPAggregation

	if conf.Migration != nil && conf.Migration.MTU != nil {
		if *conf.Migration.MTU.Network.From > *conf.Migration.MTU.Network.To {
			data.Data["MTU"] = conf.Migration.MTU.Network.From
			data.Data["RoutableMTU"] = conf.Migration.MTU.Network.To
		} else {
			data.Data["MTU"] = conf.Migration.MTU.Network.To
			data.Data["RoutableMTU"] = conf.Migration.MTU.Network.From
		}

		// c.MTU is used to set the applied network configuration MTU
		// MTU migration procedure:
		//  1. User sets the MTU they want to migrate to
		//  2. CNO sets the MTU as applied
		//  3. User can then set the MTU as configured
		c.MTU = conf.Migration.MTU.Network.To
	}
	data.Data["GenevePort"] = c.GenevePort
	data.Data["CNIConfDir"] = pluginCNIConfDir(conf)
	data.Data["CNIBinDir"] = CNIBinDir
	data.Data["OVN_NODE_MODE"] = OVN_NODE_MODE_FULL
	data.Data["DpuHostModeLabel"] = bootstrapResult.OVN.OVNKubernetesConfig.DpuHostModeLabel
	data.Data["DpuModeLabel"] = bootstrapResult.OVN.OVNKubernetesConfig.DpuModeLabel
	data.Data["SmartNicModeLabel"] = bootstrapResult.OVN.OVNKubernetesConfig.SmartNicModeLabel
	data.Data["MgmtPortResourceName"] = bootstrapResult.OVN.OVNKubernetesConfig.MgmtPortResourceName
	data.Data["OVN_NB_PORT"] = OVN_NB_PORT
	data.Data["OVN_SB_PORT"] = OVN_SB_PORT
	data.Data["OVN_NB_RAFT_PORT"] = OVN_NB_RAFT_PORT
	data.Data["OVN_SB_RAFT_PORT"] = OVN_SB_RAFT_PORT
	data.Data["OVN_NB_RAFT_ELECTION_TIMER"] = os.Getenv("OVN_NB_RAFT_ELECTION_TIMER")
	data.Data["OVN_SB_RAFT_ELECTION_TIMER"] = os.Getenv("OVN_SB_RAFT_ELECTION_TIMER")
	data.Data["OVN_CONTROLLER_INACTIVITY_PROBE"] = os.Getenv("OVN_CONTROLLER_INACTIVITY_PROBE")
	controller_inactivity_probe := os.Getenv("OVN_CONTROLLER_INACTIVITY_PROBE")
	if len(controller_inactivity_probe) == 0 {
		controller_inactivity_probe = "180000"
		klog.Infof("OVN_CONTROLLER_INACTIVITY_PROBE env var is not defined. Using: %s", controller_inactivity_probe)
	}
	data.Data["OVN_CONTROLLER_INACTIVITY_PROBE"] = controller_inactivity_probe
	nb_inactivity_probe := os.Getenv("OVN_NB_INACTIVITY_PROBE")
	if len(nb_inactivity_probe) == 0 {
		nb_inactivity_probe = "60000"
		klog.Infof("OVN_NB_INACTIVITY_PROBE env var is not defined. Using: %s", nb_inactivity_probe)
	}

	// Hypershift
	data.Data["ManagementClusterName"] = names.ManagementClusterName
	data.Data["HostedClusterNamespace"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.Namespace
	data.Data["ReleaseImage"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.ReleaseImage
	data.Data["ClusterID"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.ClusterID
	data.Data["ClusterIDLabel"] = platform.ClusterIDLabel
	data.Data["OVNDbServiceType"] = corev1.ServiceTypeClusterIP
	data.Data["OVNSbDbRouteHost"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.OVNSbDbRouteHost
	data.Data["OVNSbDbRouteLabels"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.OVNSbDbRouteLabels
	data.Data["HCPNodeSelector"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.HCPNodeSelector
	data.Data["OVN_SB_NODE_PORT"] = nil
	data.Data["OVN_NB_DB_ENDPOINT"] = fmt.Sprintf("ssl:%s:%s", bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.OVNSbDbRouteHost, OVN_SB_DB_ROUTE_PORT)
	data.Data["OVN_SB_DB_ENDPOINT"] = fmt.Sprintf("ssl:%s:%s", bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.OVNSbDbRouteHost, OVN_SB_DB_ROUTE_PORT)
	pubStrategy := bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.ServicePublishingStrategy
	if bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.OVNSbDbRouteHost == "" && pubStrategy != nil && pubStrategy.Type == hyperv1.Route && pubStrategy.Route != nil && pubStrategy.Route.Hostname != "" {
		data.Data["OVNSbDbRouteHost"] = pubStrategy.Route.Hostname
	} else if pubStrategy != nil && pubStrategy.Type == hyperv1.NodePort {
		data.Data["OVNDbServiceType"] = corev1.ServiceTypeNodePort
		data.Data["OVN_SB_NODE_PORT"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.OVNSbDbRouteNodePort
		data.Data["OVN_NB_DB_ENDPOINT"] = fmt.Sprintf("ssl:%s:%d", bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.OVNSbDbRouteHost, bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.OVNSbDbRouteNodePort)
		data.Data["OVN_SB_DB_ENDPOINT"] = fmt.Sprintf("ssl:%s:%d", bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.OVNSbDbRouteHost, bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.OVNSbDbRouteNodePort)
	}

	// Hypershift proxy
	// proxy should not be used for internal routes
	if bootstrapResult.Infra.Proxy.HTTPProxy == "" ||
		bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.OVNSbDbRouteLabels[platform.HyperShiftInternalRouteLabel] == "true" {
		data.Data["ENABLE_OVN_NODE_PROXY"] = false
	} else {
		data.Data["ENABLE_OVN_NODE_PROXY"] = true
		u, err := url.Parse(bootstrapResult.Infra.Proxy.HTTPProxy)
		if err != nil {
			return nil, progressing, errors.Wrap(err, "failed to parse http proxy")
		}
		host, port, err := net.SplitHostPort(u.Host)
		if err != nil {
			return nil, progressing, errors.Wrap(err, "failed to split http proxy host")
		}
		data.Data["HTTP_PROXY_IP"] = host
		data.Data["HTTP_PROXY_PORT"] = port
		data.Data["OVN_SB_DB_ROUTE_LOCAL_PORT"] = OVN_SB_DB_ROUTE_LOCAL_PORT
		data.Data["OVN_NB_DB_ENDPOINT"] = fmt.Sprintf("ssl:%s:%s",
			bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.OVNSbDbRouteHost, OVN_SB_DB_ROUTE_LOCAL_PORT)
		data.Data["OVN_SB_DB_ENDPOINT"] = fmt.Sprintf("ssl:%s:%s",
			bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.OVNSbDbRouteHost, OVN_SB_DB_ROUTE_LOCAL_PORT)
		data.Data["OVN_SB_DB_ROUTE_HOST"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.OVNSbDbRouteHost

		var routePort string
		if pubStrategy != nil && pubStrategy.Type == hyperv1.NodePort {
			routePort = strconv.Itoa(int(bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.OVNSbDbRouteNodePort))
		} else {
			routePort = OVN_SB_DB_ROUTE_PORT
		}
		data.Data["OVN_SB_DB_ROUTE_PORT"] = routePort
	}

	data.Data["OVN_NB_INACTIVITY_PROBE"] = nb_inactivity_probe
	data.Data["OVN_NB_DB_LIST"] = dbList(bootstrapResult.OVN.MasterAddresses, OVN_NB_PORT)
	data.Data["OVN_SB_DB_LIST"] = dbList(bootstrapResult.OVN.MasterAddresses, OVN_SB_PORT)
	data.Data["OVN_DB_CLUSTER_INITIATOR"] = bootstrapResult.OVN.ClusterInitiator
	data.Data["OVN_MIN_AVAILABLE"] = len(bootstrapResult.OVN.MasterAddresses)/2 + 1
	data.Data["LISTEN_DUAL_STACK"] = listenDualStack(bootstrapResult.OVN.MasterAddresses[0])
	data.Data["OVN_CERT_CN"] = OVN_CERT_CN
	data.Data["OVN_NORTHD_PROBE_INTERVAL"] = os.Getenv("OVN_NORTHD_PROBE_INTERVAL")
	data.Data["NetFlowCollectors"] = ""
	data.Data["SFlowCollectors"] = ""
	data.Data["IPFIXCollectors"] = ""
	data.Data["IPFIXCacheMaxFlows"] = ""
	data.Data["IPFIXCacheActiveTimeout"] = ""
	data.Data["IPFIXSampling"] = ""
	data.Data["OVNPolicyAuditRateLimit"] = c.PolicyAuditConfig.RateLimit
	data.Data["OVNPolicyAuditMaxFileSize"] = c.PolicyAuditConfig.MaxFileSize
	data.Data["OVNPolicyAuditMaxLogFiles"] = c.PolicyAuditConfig.MaxLogFiles
	data.Data["OVNPolicyAuditDestination"] = c.PolicyAuditConfig.Destination
	data.Data["OVNPolicyAuditSyslogFacility"] = c.PolicyAuditConfig.SyslogFacility
	data.Data["OVN_LOG_PATTERN_CONSOLE"] = OVN_LOG_PATTERN_CONSOLE
	data.Data["PlatformType"] = bootstrapResult.Infra.PlatformType
	if bootstrapResult.Infra.PlatformType == configv1.AzurePlatformType {
		data.Data["OVNPlatformAzure"] = true
	} else {
		data.Data["OVNPlatformAzure"] = false
	}

	var ippools string
	for _, net := range conf.ClusterNetwork {
		if len(ippools) != 0 {
			ippools += ","
		}
		ippools += fmt.Sprintf("%s/%d", net.CIDR, net.HostPrefix)
	}
	data.Data["OVN_cidr"] = ippools

	data.Data["OVN_service_cidr"] = strings.Join(conf.ServiceNetwork, ",")

	hybridOverlayStatus := "disabled"
	if c.HybridOverlayConfig != nil {
		if len(c.HybridOverlayConfig.HybridClusterNetwork) > 0 {
			data.Data["OVNHybridOverlayNetCIDR"] = c.HybridOverlayConfig.HybridClusterNetwork[0].CIDR
		} else {
			data.Data["OVNHybridOverlayNetCIDR"] = ""
		}
		if c.HybridOverlayConfig.HybridOverlayVXLANPort != nil {
			data.Data["OVNHybridOverlayVXLANPort"] = c.HybridOverlayConfig.HybridOverlayVXLANPort
		} else {
			data.Data["OVNHybridOverlayVXLANPort"] = ""
		}
		data.Data["OVNHybridOverlayEnable"] = true
		hybridOverlayStatus = "enabled"
	} else {
		data.Data["OVNHybridOverlayNetCIDR"] = ""
		data.Data["OVNHybridOverlayEnable"] = false
		data.Data["OVNHybridOverlayVXLANPort"] = ""
	}

	// If IPsec is enabled for the first time, we start the daemonset. If it is
	// disabled after that, we do not stop the daemonset but only stop IPsec.
	//
	// TODO: We need to do this as, by default, we maintain IPsec state on the
	// node in order to maintain encrypted connectivity in the case of upgrades.
	// If we only unrender the IPsec daemonset, we will be unable to cleanup
	// the IPsec state on the node and the traffic will continue to be
	// encrypted.
	if c.IPsecConfig != nil {
		// IPsec is enabled
		data.Data["OVNIPsecDaemonsetEnable"] = true
		data.Data["OVNIPsecEnable"] = true
	} else {
		if bootstrapResult.OVN.IPsecUpdateStatus != nil {
			// IPsec has previously started and
			// now it has been requested to be disabled
			data.Data["OVNIPsecDaemonsetEnable"] = true
			data.Data["OVNIPsecEnable"] = false
		} else {
			// IPsec has never started
			data.Data["OVNIPsecDaemonsetEnable"] = false
			data.Data["OVNIPsecEnable"] = false
		}
	}

	if c.GatewayConfig != nil && c.GatewayConfig.RoutingViaHost {
		data.Data["OVN_GATEWAY_MODE"] = OVN_LOCAL_GW_MODE
	} else {
		data.Data["OVN_GATEWAY_MODE"] = OVN_SHARED_GW_MODE
	}

	data.Data["IP_FORWARDING_MODE"] = operv1.IPForwardingRestricted
	if c.GatewayConfig != nil {
		data.Data["IP_FORWARDING_MODE"] = c.GatewayConfig.IPForwarding
	}

	// leverage feature gates
	data.Data["OVN_ADMIN_NETWORK_POLICY_ENABLE"] = featureGates.Enabled(configv1.FeatureGateAdminNetworkPolicy)

	data.Data["ReachabilityTotalTimeoutSeconds"] = c.EgressIPConfig.ReachabilityTotalTimeoutSeconds

	reachability_node_port := os.Getenv("OVN_EGRESSIP_HEALTHCHECK_PORT")
	if len(reachability_node_port) == 0 {
		reachability_node_port = OVN_EGRESSIP_HEALTHCHECK_PORT
		klog.Infof("OVN_EGRESSIP_HEALTHCHECK_PORT env var is not defined. Using: %s", reachability_node_port)
	}
	data.Data["ReachabilityNodePort"] = reachability_node_port
	data.Data["RHOBSMonitoring"] = os.Getenv("RHOBS_MONITORING")

	exportNetworkFlows := conf.ExportNetworkFlows
	if exportNetworkFlows != nil {
		if exportNetworkFlows.NetFlow != nil {
			var collectors strings.Builder
			for _, v := range exportNetworkFlows.NetFlow.Collectors {
				collectors.WriteString(string(v) + ",")
			}
			data.Data["NetFlowCollectors"] = strings.TrimSuffix(collectors.String(), ",")
		}
		if exportNetworkFlows.SFlow != nil {
			var collectors strings.Builder
			for _, v := range exportNetworkFlows.SFlow.Collectors {
				collectors.WriteString(string(v) + ",")
			}
			data.Data["SFlowCollectors"] = strings.TrimSuffix(collectors.String(), ",")
		}
		if exportNetworkFlows.IPFIX != nil {
			var collectors strings.Builder
			for _, v := range exportNetworkFlows.IPFIX.Collectors {
				collectors.WriteString(string(v) + ",")
			}
			data.Data["IPFIXCollectors"] = strings.TrimSuffix(collectors.String(), ",")
		}
	}
	renderOVNFlowsConfig(bootstrapResult, &data)
	if len(bootstrapResult.OVN.MasterAddresses) == 1 {
		data.Data["IsSNO"] = true
		data.Data["NorthdThreads"] = 1
	} else {
		data.Data["IsSNO"] = false
		// OVN 22.06 and later support multiple northd threads.
		// Less resource constrained clusters can use multiple threads
		// in northd to improve network operation latency at the cost
		// of a bit of CPU.
		data.Data["NorthdThreads"] = 1
	}

	data.Data["OVN_MULTI_NETWORK_ENABLE"] = true
	data.Data["OVN_MULTI_NETWORK_POLICY_ENABLE"] = false
	if conf.DisableMultiNetwork != nil && *conf.DisableMultiNetwork {
		data.Data["OVN_MULTI_NETWORK_ENABLE"] = false
	} else if conf.UseMultiNetworkPolicy != nil && *conf.UseMultiNetworkPolicy {
		// Multi-network policy support requires multi-network support to be
		// enabled
		data.Data["OVN_MULTI_NETWORK_POLICY_ENABLE"] = true
	}

	var manifestSubDir string
	manifestDirs := make([]string, 0, 2)
	manifestDirs = append(manifestDirs, filepath.Join(manifestDir, "network/ovn-kubernetes/common"))

	productFlavor := "self-hosted"
	if bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.Enabled {
		productFlavor = "managed"
	}
	manifestSubDirBasePath := filepath.Join("network/ovn-kubernetes", productFlavor)

	manifestDirs = append(manifestDirs, filepath.Join(manifestDir, manifestSubDirBasePath, "common"))

	// choose the YAMLs based on the target zone mode (4.14 only) TODO: starting from 4.15, support multizone only
	manifestSubDir = filepath.Join(manifestSubDirBasePath, "multi-zone-interconnect") // default is multizone
	if targetZoneMode.zoneMode == zoneModeSingleZone {
		// non-default, internal use only; this is selected in the first phase of an upgrade from a
		// non-interconnect version (< 4.14) to an interconnect version (>= 4.14)
		manifestSubDir = filepath.Join(manifestSubDirBasePath, "single-zone-interconnect")
	} else if isMigrationToMultiZoneAboutToStartOrOngoing(bootstrapResult.OVN, &targetZoneMode) {
		// intermediate step when converting from single zone to multizone; this is selected
		// in the second phase of an upgrade from a non-interconnect version (< 4.14)
		// to an interconnect version (>= 4.14)
		manifestSubDir = filepath.Join(manifestSubDirBasePath, "multi-zone-interconnect-tmp")
	}
	klog.Infof("render YAMLs from %s folder", manifestSubDir)
	manifestDirs = append(manifestDirs, filepath.Join(manifestDir, manifestSubDir))

	manifests, err := render.RenderDirs(manifestDirs, &data)
	if err != nil {
		return nil, progressing, errors.Wrap(err, "failed to render manifests")
	}
	objs = append(objs, manifests...)

	err = setOVNObjectAnnotation(objs, names.NetworkHybridOverlayAnnotation, hybridOverlayStatus)
	if err != nil {
		return nil, progressing, errors.Wrapf(err, "failed to set the status of hybrid overlay %s annotation on daemonsets or statefulsets", hybridOverlayStatus)
	}

	if len(bootstrapResult.OVN.OVNKubernetesConfig.SmartNicModeNodes) > 0 {
		data.Data["OVN_NODE_MODE"] = OVN_NODE_MODE_SMART_NIC
		manifests, err = render.RenderTemplate(filepath.Join(manifestDir, manifestSubDir+"/ovnkube-node.yaml"), &data)
		if err != nil {
			return nil, progressing, errors.Wrap(err, "failed to render manifests for smart-nic")
		}
		objs = append(objs, manifests...)
	}

	if len(bootstrapResult.OVN.OVNKubernetesConfig.DpuHostModeNodes) > 0 {
		data.Data["OVN_NODE_MODE"] = OVN_NODE_MODE_DPU_HOST
		manifests, err = render.RenderTemplate(filepath.Join(manifestDir, manifestSubDir+"/ovnkube-node.yaml"), &data)
		if err != nil {
			return nil, progressing, errors.Wrap(err, "failed to render manifests for dpu-host")
		}
		objs = append(objs, manifests...)
	}

	if len(bootstrapResult.OVN.OVNKubernetesConfig.DpuModeNodes) > 0 {
		// "OVN_NODE_MODE" not set when render.RenderDir() called above,
		// so render just the error-cni.yaml with "OVN_NODE_MODE" set.
		data.Data["OVN_NODE_MODE"] = OVN_NODE_MODE_DPU
		manifests, err = render.RenderTemplate(filepath.Join(manifestDir, "network/ovn-kubernetes/common/error-cni.yaml"), &data)
		if err != nil {
			return nil, progressing, errors.Wrap(err, "failed to render manifests for dpu")
		}
		objs = append(objs, manifests...)

		// Run KubeProxy on DPU
		// DPU_DEV_PREVIEW
		if conf.DeployKubeProxy == nil {
			v := true
			conf.DeployKubeProxy = &v
		} else {
			*conf.DeployKubeProxy = true
		}
		fillKubeProxyDefaults(conf, nil)
	}

	zoneModeMigrationIsOngoing := isZoneModeMigrationAboutToStartOrOngoing(bootstrapResult.OVN, &targetZoneMode)
	if zoneModeMigrationIsOngoing {
		klog.Infof("Migration to %s is ongoing or about to start", string(targetZoneMode.zoneMode))
	}

	// process zone mode change (single zone -> multizone or multizone -> single zone)
	updateNode, updateMaster, updateControlPlane := shouldUpdateOVNKonInterConnectZoneModeChange(bootstrapResult.OVN, targetZoneMode.zoneMode)

	updateNode, updateMaster, updateControlPlane, err = handleIPFamilyAnnotationAndIPFamilyChange(
		conf, bootstrapResult.OVN, &objs, zoneModeMigrationIsOngoing, updateNode, updateMaster, updateControlPlane)
	if err != nil {
		return nil, progressing, fmt.Errorf("unable to render OVN: failed to handle IP family annotation or change: %w", err)
	}

	// process upgrades only if we aren't handling a zone mode migration or an IP family migration
	if !zoneModeMigrationIsOngoing && updateNode && updateMaster && updateControlPlane {
		updateNode, updateMaster, updateControlPlane = handleOVNKUpdateUponOpenshiftUpgrade(conf, bootstrapResult.OVN)
	}

	renderPrePull := false
	if updateNode {
		updateNode, renderPrePull = shouldUpdateOVNKonPrepull(bootstrapResult.OVN, os.Getenv("RELEASE_VERSION"))
	}

	klog.Infof("ovnk components: ovnkube-node: isRunning=%t, update=%t; ovnkube-master: isRunning=%t, update=%t; ovnkube-control-plane: isRunning=%t, update=%t",
		bootstrapResult.OVN.NodeUpdateStatus != nil, updateNode,
		bootstrapResult.OVN.MasterUpdateStatus != nil, updateMaster,
		bootstrapResult.OVN.ControlPlaneUpdateStatus != nil, updateControlPlane)

	// If we need to delay the rollout of control plane, we'll tag its deployment with:
	// - "create-wait" when migrating zone mode (when going to multizone in phase2 of the upgrade: we delay control
	//   plane until all ovnkube node pods are up; since there's no control plane in single zone, "create-wait"
	// prevents the control plane from getting pushed to the API server right away; the annotation is removed as soon
	// as ovnkube-node has rolled out.
	// - "create-only" if we're only going through the upgrade path (with no zone mode migration),
	//   in the same fashion used until 4.13 for master and node.
	if !updateControlPlane { // no-op if object is not found
		annotationKey := names.CreateOnlyAnnotation // skip only if object doesn't exist already
		if zoneModeMigrationIsOngoing {
			annotationKey = names.CreateWaitAnnotation // skip altogether
		}
		klog.Infof("annotate local copy of ovnkube-control-plane deployment with %s", annotationKey)
		namespace := util.OVN_NAMESPACE
		if bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.Enabled {
			namespace = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.Namespace
		}
		k8s.UpdateObjByGroupKindName(objs, "apps", "Deployment", namespace, util.OVN_CONTROL_PLANE, func(o *uns.Unstructured) {
			anno := o.GetAnnotations()
			if anno == nil {
				anno = map[string]string{}
			}
			anno[annotationKey] = "true"
			o.SetAnnotations(anno)
		})
	}
	// If we need to delay the rollout of master or node, we'll tag that daemonset/statefulset with "create-only"
	// (will keep the existing running daemonset/statefulset)
	if !updateMaster && bootstrapResult.OVN.MasterUpdateStatus != nil {
		klog.Infof("annotate local copy of ovnkube-master with create-only")
		kind := bootstrapResult.OVN.MasterUpdateStatus.Kind
		namespace := bootstrapResult.OVN.MasterUpdateStatus.Namespace
		name := bootstrapResult.OVN.MasterUpdateStatus.Name
		k8s.UpdateObjByGroupKindName(objs, "apps", kind, namespace, name, func(o *uns.Unstructured) {
			anno := o.GetAnnotations()
			if anno == nil {
				anno = map[string]string{}
			}
			anno[names.CreateOnlyAnnotation] = "true" // skip if annotated and object exists already
			o.SetAnnotations(anno)
		})
	}
	if !updateNode && bootstrapResult.OVN.NodeUpdateStatus != nil {
		klog.Infof("annotate local copy of ovnkube-node DaemonSet with create-only")
		kind := bootstrapResult.OVN.NodeUpdateStatus.Kind
		namespace := bootstrapResult.OVN.NodeUpdateStatus.Namespace
		name := bootstrapResult.OVN.NodeUpdateStatus.Name
		k8s.UpdateObjByGroupKindName(objs, "apps", kind, namespace, name, func(o *uns.Unstructured) {
			anno := o.GetAnnotations()
			if anno == nil {
				anno = map[string]string{}
			}
			anno[names.CreateOnlyAnnotation] = "true" // skip if annotated and object exists already
			o.SetAnnotations(anno)
		})
	}

	if !renderPrePull {
		// remove prepull from the list of objects to render.
		objs = k8s.RemoveObjByGroupKindName(objs, "apps", "DaemonSet", util.OVN_NAMESPACE, "ovnkube-upgrades-prepuller")
	}

	// In hypershift, if we're pushing the route (single-zone only) don't create ovnkube node if the route isn't up yet
	if bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.Enabled &&
		k8s.CheckObjByGroupKindName(
			objs, "apps", "Route",
			bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.Namespace,
			"ovnkube-sbdb") &&
		bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.OVNSbDbRouteHost == "" {

		k8s.UpdateObjByGroupKindName(objs, "apps", "DaemonSet", util.OVN_NAMESPACE, util.OVN_NODE, func(o *uns.Unstructured) {
			anno := o.GetAnnotations()
			if anno == nil {
				anno = map[string]string{}
			}
			anno[names.CreateWaitAnnotation] = "true"
			o.SetAnnotations(anno)
		})
		progressing = true
	}

	return objs, progressing, nil
}

// renderOVNFlowsConfig renders the bootstrapped information from the ovs-flows-config ConfigMap
func renderOVNFlowsConfig(bootstrapResult *bootstrap.BootstrapResult, data *render.RenderData) {
	flows := bootstrapResult.OVN.FlowsConfig
	if flows == nil {
		return
	}
	if flows.Target == "" {
		klog.Warningf("ovs-flows-config configmap 'target' field can't be empty. Ignoring configuration: %+v", flows)
		return
	}
	// if IPFIX collectors are provided by means of both the operator configuration and the
	// ovs-flows-config ConfigMap, we will merge both targets
	if colls, ok := data.Data["IPFIXCollectors"].(string); !ok || colls == "" {
		data.Data["IPFIXCollectors"] = flows.Target
	} else {
		data.Data["IPFIXCollectors"] = colls + "," + flows.Target
	}
	if flows.CacheMaxFlows != nil {
		data.Data["IPFIXCacheMaxFlows"] = *flows.CacheMaxFlows
	}
	if flows.Sampling != nil {
		data.Data["IPFIXSampling"] = *flows.Sampling
	}
	if flows.CacheActiveTimeout != nil {
		data.Data["IPFIXCacheActiveTimeout"] = *flows.CacheActiveTimeout
	}
}

func bootstrapOVNHyperShiftConfig(hc *platform.HyperShiftConfig, kubeClient cnoclient.Client, infraStatus *bootstrap.InfraStatus) (*bootstrap.OVNHyperShiftBootstrapResult, error) {
	ovnHypershiftResult := &bootstrap.OVNHyperShiftBootstrapResult{
		Enabled:            hc.Enabled,
		Namespace:          hc.Namespace,
		OVNSbDbRouteHost:   hc.OVNSbDbRouteHost,
		OVNSbDbRouteLabels: hc.OVNSbDbRouteLabels,
		ReleaseImage:       hc.ReleaseImage,
		ControlPlaneImage:  hc.ControlPlaneImage,
	}

	if !hc.Enabled {
		return ovnHypershiftResult, nil
	}

	hcp := infraStatus.HostedControlPlane

	ovnHypershiftResult.ClusterID = hcp.Spec.ClusterID
	ovnHypershiftResult.HCPNodeSelector = hcp.Spec.NodeSelector
	switch hcp.Spec.ControllerAvailabilityPolicy {
	case hyperv1.HighlyAvailable:
		ovnHypershiftResult.ControlPlaneReplicas = 3
	default:
		ovnHypershiftResult.ControlPlaneReplicas = 1
	}
	for _, svc := range hcp.Spec.Services {
		// TODO: instead of the hardcoded string use ServiceType hyperv1.OVNSbDb once the API is updated
		if svc.Service == "OVNSbDb" {
			s := svc.ServicePublishingStrategy
			ovnHypershiftResult.ServicePublishingStrategy = &s
		}
	}
	if ovnHypershiftResult.ServicePublishingStrategy == nil {
		klog.Warningf("service publishing strategy for OVN southbound database does not exist in hyperv1.HostedControlPlane %s/%s. Defaulting to route", hc.Name, hc.Namespace)
		ovnHypershiftResult.ServicePublishingStrategy = &hyperv1.ServicePublishingStrategy{
			Type: hyperv1.Route,
		}
	}

	if ovnHypershiftResult.OVNSbDbRouteHost != "" {
		return ovnHypershiftResult, nil
	}

	switch ovnHypershiftResult.ServicePublishingStrategy.Type {
	case hyperv1.Route:
		{
			route := &routev1.Route{}
			gvr := schema.GroupVersionResource{
				Group:    "route.openshift.io",
				Version:  "v1",
				Resource: "routes",
			}
			clusterClient := kubeClient.ClientFor(names.ManagementClusterName)
			routeObj, err := clusterClient.Dynamic().Resource(gvr).Namespace(hc.Namespace).Get(context.TODO(), "ovnkube-sbdb", metav1.GetOptions{})
			if err != nil {
				if apierrors.IsNotFound(err) {
					klog.Infof("Did not find ovnkube-sbdb route")
				} else {
					return nil, fmt.Errorf("could not get ovnkube-sbdb route: %v", err)
				}
			} else {
				err := runtime.DefaultUnstructuredConverter.FromUnstructured(routeObj.UnstructuredContent(), route)
				if err != nil {
					return nil, err
				}
				if (len(route.Status.Ingress) < 1 || route.Status.Ingress[0].Host == "") && route.Spec.Host == "" {
					return ovnHypershiftResult, nil
				}
				if len(route.Status.Ingress) >= 1 && route.Status.Ingress[0].Host != "" {
					ovnHypershiftResult.OVNSbDbRouteHost = route.Status.Ingress[0].Host
				} else if route.Spec.Host != "" {
					ovnHypershiftResult.OVNSbDbRouteHost = route.Spec.Host
				}
				klog.Infof("Overriding OVN configuration route to %s", ovnHypershiftResult.OVNSbDbRouteHost)
			}
		}
	case hyperv1.NodePort:
		{
			svc := &corev1.Service{}
			clusterClient := kubeClient.ClientFor(names.ManagementClusterName)
			err := clusterClient.CRClient().Get(context.TODO(), types.NamespacedName{Namespace: hc.Namespace, Name: "ovnkube-master-external"}, svc)
			if err != nil {
				if apierrors.IsNotFound(err) {
					klog.Infof("Did not find ovnkube-master service")
					return ovnHypershiftResult, nil
				} else {
					return nil, fmt.Errorf("could not get ovnkube-master service: %v", err)
				}
			}
			var sbDbPort int32
			for _, p := range svc.Spec.Ports {
				if p.Name == "south" {
					sbDbPort = p.NodePort
				}
			}
			if sbDbPort > 0 {
				ovnHypershiftResult.OVNSbDbRouteHost = ovnHypershiftResult.ServicePublishingStrategy.NodePort.Address
				ovnHypershiftResult.OVNSbDbRouteNodePort = sbDbPort
			} else {
				klog.Infof("Node port not defined for ovnkube-master service")
			}
		}
	default:
		return nil, fmt.Errorf("unsupported service publishing strategy type: %s", ovnHypershiftResult.ServicePublishingStrategy.Type)
	}
	return ovnHypershiftResult, nil
}

func getDisableUDPAggregation(cl crclient.Reader) bool {
	disable := false

	// Disable by default on s390x because it sometimes doesn't work there; see OCPBUGS-2532
	if goruntime.GOARCH == "s390x" {
		disable = true
	}

	cm := &corev1.ConfigMap{}
	if err := cl.Get(context.TODO(), types.NamespacedName{
		Namespace: "openshift-network-operator",
		Name:      "udp-aggregation-config",
	}, cm); err != nil {
		if !apierrors.IsNotFound(err) {
			klog.Warningf("Error fetching udp-aggregation-config configmap: %v", err)
		}
		return disable
	}

	disableUDPAggregation := cm.Data["disable-udp-aggregation"]
	if disableUDPAggregation == "true" {
		disable = true
	} else if disableUDPAggregation == "false" {
		disable = false
	} else {
		klog.Warningf("Ignoring unexpected udp-aggregation-config override value disable-udp-aggregation=%q", disableUDPAggregation)
	}

	return disable
}

// getNodeListByLabel returns a list of node names that matches the provided label.
func getNodeListByLabel(kubeClient cnoclient.Client, label string) ([]string, error) {
	var nodeNames []string
	nodeList, err := kubeClient.Default().Kubernetes().CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{LabelSelector: label})
	if err != nil {
		return nil, err
	}
	for _, node := range nodeList.Items {
		nodeNames = append(nodeNames, node.Name)
	}
	klog.Infof("For Label %s, the list of nodes are %+q", label, nodeNames)
	return nodeNames, nil
}

// findCommonNode returns true if there is a common node in the node list.
func findCommonNode(nodeLists ...[]string) (bool, string) {
	exists := make(map[string]bool)
	for _, list := range nodeLists {
		for _, value := range list {
			if exists[value] {
				return true, value
			}
			exists[value] = true
		}
	}
	return false, ""
}

// bootstrapOVNConfig returns the values in the openshift-ovn-kubernetes/hardware-offload-config configMap
// if it exists, otherwise returns default configuration for OCP clusters using OVN-Kubernetes
func bootstrapOVNConfig(conf *operv1.Network, kubeClient cnoclient.Client, hc *platform.HyperShiftConfig, infraStatus *bootstrap.InfraStatus) (*bootstrap.OVNConfigBoostrapResult, error) {
	ovnConfigResult := &bootstrap.OVNConfigBoostrapResult{
		DpuHostModeLabel:     OVN_NODE_SELECTOR_DEFAULT_DPU_HOST,
		DpuModeLabel:         OVN_NODE_SELECTOR_DEFAULT_DPU,
		SmartNicModeLabel:    OVN_NODE_SELECTOR_DEFAULT_SMART_NIC,
		MgmtPortResourceName: "",
	}
	if conf.Spec.DefaultNetwork.OVNKubernetesConfig.GatewayConfig == nil {
		bootstrapOVNGatewayConfig(conf, kubeClient.ClientFor("").CRClient())
	}

	var err error
	ovnConfigResult.HyperShiftConfig, err = bootstrapOVNHyperShiftConfig(hc, kubeClient, infraStatus)
	if err != nil {
		return nil, err
	}

	cm := &corev1.ConfigMap{}
	dmc := types.NamespacedName{Namespace: "openshift-network-operator", Name: "hardware-offload-config"}
	err = kubeClient.ClientFor("").CRClient().Get(context.TODO(), dmc, cm)

	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("Could not determine Node Mode: %w", err)
		}
	} else {
		dpuHostModeLabel, exists := cm.Data["dpu-host-mode-label"]
		if exists {
			ovnConfigResult.DpuHostModeLabel = dpuHostModeLabel
		}

		dpuModeLabel, exists := cm.Data["dpu-mode-label"]
		if exists {
			ovnConfigResult.DpuModeLabel = dpuModeLabel
		}

		smartNicModeLabel, exists := cm.Data["smart-nic-mode-label"]
		if exists {
			ovnConfigResult.SmartNicModeLabel = smartNicModeLabel
		}

		mgmtPortresourceName, exists := cm.Data["mgmt-port-resource-name"]
		if exists {
			ovnConfigResult.MgmtPortResourceName = mgmtPortresourceName
		}
	}

	// We want to see if there are any nodes that are labeled for specific modes.
	ovnConfigResult.DpuHostModeNodes, err = getNodeListByLabel(kubeClient, ovnConfigResult.DpuHostModeLabel+"=")
	if err != nil {
		return nil, fmt.Errorf("Could not get node list with label %s : %w", ovnConfigResult.DpuHostModeLabel, err)
	}

	ovnConfigResult.DpuModeNodes, err = getNodeListByLabel(kubeClient, ovnConfigResult.DpuModeLabel+"=")
	if err != nil {
		return nil, fmt.Errorf("Could not get node list with label %s : %w", ovnConfigResult.DpuModeLabel, err)
	}

	ovnConfigResult.SmartNicModeNodes, err = getNodeListByLabel(kubeClient, ovnConfigResult.SmartNicModeLabel+"=")
	if err != nil {
		return nil, fmt.Errorf("Could not get node list with label %s : %w", ovnConfigResult.SmartNicModeLabel, err)
	}

	// No node shall have any other label set. Each node should be ONLY be DPU, DPU Host, or Smart NIC.
	found, nodeName := findCommonNode(ovnConfigResult.DpuHostModeNodes, ovnConfigResult.DpuModeNodes, ovnConfigResult.SmartNicModeNodes)
	if found {
		return nil, fmt.Errorf("Node %s has multiple hardware offload labels.", nodeName)
	}

	klog.Infof("OVN configuration is now %+v", ovnConfigResult)

	ovnConfigResult.DisableUDPAggregation = getDisableUDPAggregation(kubeClient.ClientFor("").CRClient())

	return ovnConfigResult, nil
}

// validateOVNKubernetes checks that the ovn-kubernetes specific configuration
// is basically sane.
func validateOVNKubernetes(conf *operv1.NetworkSpec) []error {
	out := []error{}

	var cnHasIPv4, cnHasIPv6 bool
	for _, cn := range conf.ClusterNetwork {
		if utilnet.IsIPv6CIDRString(cn.CIDR) {
			cnHasIPv6 = true
		} else {
			cnHasIPv4 = true
		}
	}
	if !cnHasIPv6 && !cnHasIPv4 {
		out = append(out, errors.Errorf("ClusterNetwork cannot be empty"))
	}

	var snHasIPv4, snHasIPv6 bool
	for _, sn := range conf.ServiceNetwork {
		if utilnet.IsIPv6CIDRString(sn) {
			snHasIPv6 = true
		} else {
			snHasIPv4 = true
		}
	}
	if !snHasIPv6 && !snHasIPv4 {
		out = append(out, errors.Errorf("ServiceNetwork cannot be empty"))
	}

	if cnHasIPv4 != snHasIPv4 || cnHasIPv6 != snHasIPv6 {
		out = append(out, errors.Errorf("ClusterNetwork and ServiceNetwork must have matching IP families"))
	}
	if len(conf.ServiceNetwork) > 2 || (len(conf.ServiceNetwork) == 2 && (!snHasIPv4 || !snHasIPv6)) {
		out = append(out, errors.Errorf("ServiceNetwork must have either a single CIDR or a dual-stack pair of CIDRs"))
	}

	oc := conf.DefaultNetwork.OVNKubernetesConfig
	if oc != nil {
		minMTU := MinMTUIPv4
		if cnHasIPv6 {
			minMTU = MinMTUIPv6
		}
		if oc.MTU != nil && (*oc.MTU < minMTU || *oc.MTU > MaxMTU) {
			out = append(out, errors.Errorf("invalid MTU %d", *oc.MTU))
		}
		if oc.GenevePort != nil && (*oc.GenevePort < 1 || *oc.GenevePort > 65535) {
			out = append(out, errors.Errorf("invalid GenevePort %d", *oc.GenevePort))
		}
		var v4Net, v6Net *net.IPNet
		var err error
		if oc.V4InternalSubnet != "" {
			if !cnHasIPv4 {
				out = append(out, errors.Errorf("v4InternalSubnet and ClusterNetwork must have matching IP families"))
			}
			_, v4Net, err = net.ParseCIDR(oc.V4InternalSubnet)
			if err != nil {
				out = append(out, errors.Errorf("v4InternalSubnet is invalid: %s", err))
			}
			if !isV4InternalSubnetLargeEnough(conf) {
				out = append(out, errors.Errorf("v4InternalSubnet is no large enough for the maximum number of nodes which can be supported by ClusterNetwork"))
			}
		}
		if oc.V6InternalSubnet != "" {
			if !cnHasIPv6 {
				out = append(out, errors.Errorf("v6InternalSubnet and ClusterNetwork must have matching IP families"))
			}
			_, v6Net, err = net.ParseCIDR(oc.V6InternalSubnet)
			if err != nil {
				out = append(out, errors.Errorf("v6InternalSubnet is invalid: %s", err))
			}
			if !isV6InternalSubnetLargeEnough(conf) {
				out = append(out, errors.Errorf("v6InternalSubnet is no large enough for the maximum number of nodes which can be supported by ClusterNetwork"))
			}
		}
		for _, cn := range conf.ClusterNetwork {
			if utilnet.IsIPv6CIDRString(cn.CIDR) {
				if oc.V6InternalSubnet != "" {
					_, v6ClusterNet, _ := net.ParseCIDR(cn.CIDR)
					if iputil.NetsOverlap(*v6Net, *v6ClusterNet) {
						out = append(out, errors.Errorf("v6InternalSubnet overlaps with ClusterNetwork %s", cn.CIDR))
					}
				}
			} else {
				if oc.V4InternalSubnet != "" {
					_, v4ClusterNet, _ := net.ParseCIDR(cn.CIDR)
					if iputil.NetsOverlap(*v4Net, *v4ClusterNet) {
						out = append(out, errors.Errorf("v4InternalSubnet overlaps with ClusterNetwork %s", cn.CIDR))
					}
				}
			}
		}
		for _, sn := range conf.ServiceNetwork {
			if utilnet.IsIPv6CIDRString(sn) {
				if oc.V6InternalSubnet != "" {
					_, v6ServiceNet, _ := net.ParseCIDR(sn)
					if iputil.NetsOverlap(*v6Net, *v6ServiceNet) {
						out = append(out, errors.Errorf("v6InternalSubnet overlaps with ServiceNetwork %s", sn))
					}
				}
			} else {
				if oc.V4InternalSubnet != "" {
					_, v4ServiceNet, _ := net.ParseCIDR(sn)
					if iputil.NetsOverlap(*v4Net, *v4ServiceNet) {
						out = append(out, errors.Errorf("v4InternalSubnet overlaps with ServiceNetwork %s", sn))
					}
				}
			}
		}
	}

	return out
}

func getOVNEncapOverhead(conf *operv1.NetworkSpec) uint32 {
	const geneveOverhead = 100
	const ipsecOverhead = 46 // Transport mode, AES-GCM
	var encapOverhead uint32 = geneveOverhead
	if conf.DefaultNetwork.OVNKubernetesConfig.IPsecConfig != nil {
		encapOverhead += ipsecOverhead
	}
	return encapOverhead
}

// isOVNKubernetesChangeSafe currently returns an error if any changes to immutable
// fields are made.
// In the future, we may support rolling out MTU or other alterations.
func isOVNKubernetesChangeSafe(prev, next *operv1.NetworkSpec) []error {
	pn := prev.DefaultNetwork.OVNKubernetesConfig
	nn := next.DefaultNetwork.OVNKubernetesConfig
	errs := []error{}

	if next.Migration != nil && next.Migration.MTU != nil {
		mtuNet := next.Migration.MTU.Network
		mtuMach := next.Migration.MTU.Machine

		// For MTU values provided for migration, verify that:
		//  - The current and target MTUs for the CNI are provided
		//  - The machine target MTU is provided
		//  - The current MTU actually matches the MTU known as current
		//  - The machine target MTU has a valid overhead with the CNI target MTU
		if mtuNet == nil || mtuMach == nil || mtuNet.From == nil || mtuNet.To == nil || mtuMach.To == nil {
			errs = append(errs, errors.Errorf("invalid Migration.MTU, at least one of the required fields is missing"))
		} else {
			// Only check next.Migration.MTU.Network.From when it changes
			checkPrevMTU := prev.Migration == nil || prev.Migration.MTU == nil || prev.Migration.MTU.Network == nil || !reflect.DeepEqual(prev.Migration.MTU.Network.From, next.Migration.MTU.Network.From)
			if checkPrevMTU && !reflect.DeepEqual(next.Migration.MTU.Network.From, pn.MTU) {
				errs = append(errs, errors.Errorf("invalid Migration.MTU.Network.From(%d) not equal to the currently applied MTU(%d)", *next.Migration.MTU.Network.From, *pn.MTU))
			}

			minMTU := MinMTUIPv4
			for _, cn := range next.ClusterNetwork {
				if utilnet.IsIPv6CIDRString(cn.CIDR) {
					minMTU = MinMTUIPv6
					break
				}
			}
			if *next.Migration.MTU.Network.To < minMTU || *next.Migration.MTU.Network.To > MaxMTU {
				errs = append(errs, errors.Errorf("invalid Migration.MTU.Network.To(%d), has to be in range: %d-%d", *next.Migration.MTU.Network.To, minMTU, MaxMTU))
			}
			if *next.Migration.MTU.Machine.To < minMTU || *next.Migration.MTU.Machine.To > MaxMTU {
				errs = append(errs, errors.Errorf("invalid Migration.MTU.Machine.To(%d), has to be in range: %d-%d", *next.Migration.MTU.Machine.To, minMTU, MaxMTU))
			}
			if (*next.Migration.MTU.Network.To + getOVNEncapOverhead(next)) > *next.Migration.MTU.Machine.To {
				errs = append(errs, errors.Errorf("invalid Migration.MTU.Machine.To(%d), has to be at least %d", *next.Migration.MTU.Machine.To, *next.Migration.MTU.Network.To+getOVNEncapOverhead(next)))
			}
		}
	} else if !reflect.DeepEqual(pn.MTU, nn.MTU) {
		errs = append(errs, errors.Errorf("cannot change ovn-kubernetes MTU without migration"))
	}

	if !reflect.DeepEqual(pn.GenevePort, nn.GenevePort) {
		errs = append(errs, errors.Errorf("cannot change ovn-kubernetes genevePort"))
	}
	if pn.HybridOverlayConfig != nil && nn.HybridOverlayConfig != nil {
		if !reflect.DeepEqual(pn.HybridOverlayConfig, nn.HybridOverlayConfig) {
			errs = append(errs, errors.Errorf("cannot edit a running hybrid overlay network"))
		}
	}
	if pn.IPsecConfig != nil && nn.IPsecConfig != nil {
		if !reflect.DeepEqual(pn.IPsecConfig, nn.IPsecConfig) {
			errs = append(errs, errors.Errorf("cannot edit IPsec configuration at runtime"))
		}
	}

	return errs
}

func fillOVNKubernetesDefaults(conf, previous *operv1.NetworkSpec, hostMTU int) {

	if conf.DefaultNetwork.OVNKubernetesConfig == nil {
		conf.DefaultNetwork.OVNKubernetesConfig = &operv1.OVNKubernetesConfig{}
	}

	sc := conf.DefaultNetwork.OVNKubernetesConfig
	// MTU is currently the only field we pull from previous.
	// If it's not supplied, we infer it by probing a node's interface via the mtu-prober job.
	// However, this can never change, so we always prefer previous.
	if sc.MTU == nil {
		var mtu uint32
		if previous != nil && previous.DefaultNetwork.OVNKubernetesConfig != nil &&
			previous.DefaultNetwork.OVNKubernetesConfig.MTU != nil {
			mtu = *previous.DefaultNetwork.OVNKubernetesConfig.MTU
		} else {
			// utter paranoia
			// somehow we didn't probe the MTU in the controller, but we need it.
			// This might be wrong in cases where the CNO is not local (e.g. Hypershift).
			if hostMTU == 0 {
				log.Printf("BUG: Probed MTU wasn't supplied, but was needed. Falling back to host MTU")
				hostMTU, _ = GetDefaultMTU()
				if hostMTU == 0 { // this is beyond unlikely.
					panic("BUG: Probed MTU wasn't supplied, host MTU invalid")
				}
			}
			mtu = uint32(hostMTU) - getOVNEncapOverhead(conf)
		}
		sc.MTU = &mtu
	}
	if sc.GenevePort == nil {
		var geneve uint32 = uint32(6081)
		sc.GenevePort = &geneve
	}

	if sc.PolicyAuditConfig == nil {
		sc.PolicyAuditConfig = &operv1.PolicyAuditConfig{}
	}

	if sc.PolicyAuditConfig.RateLimit == nil {
		var ratelimit uint32 = uint32(20)
		sc.PolicyAuditConfig.RateLimit = &ratelimit
	}
	if sc.PolicyAuditConfig.MaxFileSize == nil {
		var maxfilesize uint32 = uint32(50)
		sc.PolicyAuditConfig.MaxFileSize = &maxfilesize
	}
	if sc.PolicyAuditConfig.Destination == "" {
		var destination string = "null"
		sc.PolicyAuditConfig.Destination = destination
	}
	if sc.PolicyAuditConfig.SyslogFacility == "" {
		var syslogfacility string = "local0"
		sc.PolicyAuditConfig.SyslogFacility = syslogfacility
	}

}

type replicaCountDecoder struct {
	ControlPlane struct {
		Replicas string `json:"replicas"`
	} `json:"controlPlane"`
}

// bootstrapOVNGatewayConfig sets the Network.operator.openshift.io.Spec.DefaultNetwork.OVNKubernetesConfig.GatewayConfig value
// based on the values from the "gateway-mode-config" map if any
func bootstrapOVNGatewayConfig(conf *operv1.Network, kubeClient crclient.Client) {
	// handle upgrade logic for gateway mode in OVN-K plugin (migration from hidden config map to using proper API)
	// TODO: Remove this logic in future releases when we are sure everyone has migrated away from the config-map
	cm := &corev1.ConfigMap{}
	nsn := types.NamespacedName{Namespace: "openshift-network-operator", Name: "gateway-mode-config"}
	err := kubeClient.Get(context.TODO(), nsn, cm)
	modeOverride := OVN_SHARED_GW_MODE
	routeViaHost := false

	if err != nil {
		klog.Infof("Did not find gateway-mode-config. Using default gateway mode: %s", OVN_SHARED_GW_MODE)
	} else {
		modeOverride = cm.Data["mode"]
		if modeOverride != OVN_SHARED_GW_MODE && modeOverride != OVN_LOCAL_GW_MODE {
			klog.Warningf("gateway-mode-config does not match %q or %q, is: %q. Using default gateway mode: %s",
				OVN_LOCAL_GW_MODE, OVN_SHARED_GW_MODE, modeOverride, OVN_SHARED_GW_MODE)
			modeOverride = OVN_SHARED_GW_MODE
		}
	}
	if modeOverride == OVN_LOCAL_GW_MODE {
		routeViaHost = true
	}
	conf.Spec.DefaultNetwork.OVNKubernetesConfig.GatewayConfig = &operv1.GatewayConfig{
		RoutingViaHost: routeViaHost,
	}
	klog.Infof("Gateway mode is %s", modeOverride)
}

type nodeInfo struct {
	address string
	created time.Time
}

type nodeInfoList []nodeInfo

func (l nodeInfoList) Len() int {
	return len(l)
}

func (l nodeInfoList) Swap(i, j int) {
	l[i], l[j] = l[j], l[i]
}

func (l nodeInfoList) Less(i, j int) bool {
	return l[i].created.Before(l[j].created)
}

// getMasterAddresses determines the addresses (IP or DNS names) of the ovn-kubernetes
// control plane nodes. It returns the list of addresses and an updated timeout,
// or an error.
func getMasterAddresses(kubeClient crclient.Client, controlPlaneReplicaCount int, hypershift bool, timeout int) ([]string, int, error) {
	var heartBeat int
	masterNodeList := &corev1.NodeList{}
	ovnMasterAddresses := make([]string, 0, controlPlaneReplicaCount)

	if hypershift {
		for i := 0; i < controlPlaneReplicaCount; i++ {
			ovnMasterAddresses = append(ovnMasterAddresses, fmt.Sprintf("ovnkube-master-%d.ovnkube-master-internal.%s.svc.cluster.local", i, os.Getenv("HOSTED_CLUSTER_NAMESPACE")))
		}
		sort.Strings(ovnMasterAddresses)
		return ovnMasterAddresses, timeout, nil
	}

	// Not Hypershift... find all master nodes by label
	err := wait.PollUntilContextTimeout(context.TODO(), OVN_MASTER_DISCOVERY_POLL*time.Second, time.Duration(timeout)*time.Second, true, func(ctx context.Context) (bool, error) {
		matchingLabels := &crclient.MatchingLabels{"node-role.kubernetes.io/master": ""}
		if err := kubeClient.List(ctx, masterNodeList, matchingLabels); err != nil {
			return false, err
		}
		if len(masterNodeList.Items) != 0 && controlPlaneReplicaCount == len(masterNodeList.Items) {
			return true, nil
		}

		heartBeat++
		if heartBeat%3 == 0 {
			klog.V(2).Infof("Waiting to complete OVN bootstrap: found (%d) master nodes out of (%d) expected: timing out in %d seconds",
				len(masterNodeList.Items), controlPlaneReplicaCount, timeout-OVN_MASTER_DISCOVERY_POLL*heartBeat)
		}
		return false, nil
	})
	if wait.Interrupted(err) {
		klog.Warningf("Timeout exceeded while bootstraping OVN, expected amount of control plane nodes (%v) do not match found (%v): continuing deployment with found replicas",
			controlPlaneReplicaCount, len(masterNodeList.Items))
		// On certain types of cluster this condition will never be met (assisted installer, for example)
		// As to not hold the reconciliation loop for too long on such clusters: dynamically modify the timeout
		// to a shorter and shorter value. Never reach 0 however as that will result in a `PollInfinity`.
		// Right now we'll do:
		// - First reconciliation 250 second timeout
		// - Second reconciliation 130 second timeout
		// - >= Third reconciliation 10 second timeout
		if timeout-OVN_MASTER_DISCOVERY_BACKOFF > 0 {
			timeout = timeout - OVN_MASTER_DISCOVERY_BACKOFF
		}
	} else if err != nil {
		return nil, timeout, fmt.Errorf("unable to bootstrap OVN, err: %v", err)
	}

	nodeList := make(nodeInfoList, 0, len(masterNodeList.Items))
	for _, node := range masterNodeList.Items {
		ni := nodeInfo{created: node.CreationTimestamp.Time}
		for _, address := range node.Status.Addresses {
			if address.Type == corev1.NodeInternalIP {
				ni.address = address.Address
				break
			}
		}
		if ni.address == "" {
			return nil, timeout, fmt.Errorf("no InternalIP found on master node '%s'", node.Name)
		}

		nodeList = append(nodeList, ni)
	}

	// Take the oldest masters up to the expected number of replicas
	sort.Stable(nodeList)
	for i, ni := range nodeList {
		if i >= controlPlaneReplicaCount {
			break
		}
		ovnMasterAddresses = append(ovnMasterAddresses, ni.address)
	}
	klog.V(2).Infof("Preferring %s for database clusters", ovnMasterAddresses)

	return ovnMasterAddresses, timeout, nil
}

type InterConnectZoneMode string

const (
	zoneModeMultiZone  InterConnectZoneMode = "multizone"  // every node is assigned to a different zone
	zoneModeSingleZone InterConnectZoneMode = "singlezone" // all nodes are assigned to a single global zone
)

type targetZoneModeType struct {
	// zoneMode indicates the target zone mode that CNO is supposed to converge to.
	zoneMode InterConnectZoneMode
	// "temporary", if true, indicates that the target zone mode is only temporary;
	// it is used along with zoneMode=singlezone in order to temporarily switch to single-zone mode when upgrading
	// from versions with no interconnect support (<=4.13). It has no effect if zoneMode=multizone.
	temporary bool
	// "configMapFound" indicates whether the interconnect configmap was found; when not found,
	// the zone mode defaults to multizone.
	configMapFound bool
	// ongoingUpgrade is true when the configmap was pushed by CNO itself; it is used by status manager
	// to know it has to keep reporting the old version when this parameter is set.
	ongoingUpgrade bool
}

// getTargetInterConnectZoneMode determines the desired interconnect zone mode for the cluster.
// Available modes are two: multizone (default, one node per zone) and single zone (all nodes in the same zone).
// A configmap is looked up in order to switch to non-default single zone. In absence of this configmap, multizone is applied.
func getTargetInterConnectZoneMode(kubeClient cnoclient.Client) (targetZoneModeType, error) {
	targetZoneMode := targetZoneModeType{}

	interConnectConfigMap, err := util.GetInterConnectConfigMap(kubeClient.ClientFor("").Kubernetes())
	if err != nil {
		if apierrors.IsNotFound(err) {
			klog.Infof("No OVN InterConnect configMap found, applying default: multizone")
			targetZoneMode.zoneMode = zoneModeMultiZone
			return targetZoneMode, nil
		}
		return targetZoneMode, fmt.Errorf("unable to retrieve interconnect configMap: %w", err)
	}
	targetZoneMode.configMapFound = true
	if zoneModeFromConfigMap, ok := interConnectConfigMap.Data["zone-mode"]; ok {
		switch strings.ToLower(zoneModeFromConfigMap) {
		case string(zoneModeSingleZone):
			targetZoneMode.zoneMode = zoneModeSingleZone
		case string(zoneModeMultiZone):
			targetZoneMode.zoneMode = zoneModeMultiZone
		default:
			klog.Errorf("unrecognized value from interconnect configmap: %s, defaulting to multizone",
				zoneModeFromConfigMap)
			targetZoneMode.zoneMode = zoneModeMultiZone
		}
	} else {
		klog.Infof("no target zone mode in interconnect configmap, defaulting to multizone")
		targetZoneMode.zoneMode = zoneModeMultiZone
	}

	if temporaryFromConfigMap, ok := interConnectConfigMap.Data["temporary"]; ok {
		targetZoneMode.temporary = strings.ToLower(temporaryFromConfigMap) == "true"
	}

	if _, ok := interConnectConfigMap.Data["ongoing-upgrade"]; ok {
		targetZoneMode.ongoingUpgrade = true
	}

	klog.Infof("target zone mode: %+v", targetZoneMode)

	return targetZoneMode, nil
}

func bootstrapOVN(conf *operv1.Network, kubeClient cnoclient.Client, infraStatus *bootstrap.InfraStatus) (*bootstrap.OVNBootstrapResult, error) {
	clusterConfig := &corev1.ConfigMap{}
	clusterConfigLookup := types.NamespacedName{Name: CLUSTER_CONFIG_NAME, Namespace: CLUSTER_CONFIG_NAMESPACE}

	if err := kubeClient.ClientFor("").CRClient().Get(context.TODO(), clusterConfigLookup, clusterConfig); err != nil {
		return nil, fmt.Errorf("Unable to bootstrap OVN, unable to retrieve cluster config: %s", err)
	}

	rcD := replicaCountDecoder{}
	if err := yaml.Unmarshal([]byte(clusterConfig.Data["install-config"]), &rcD); err != nil {
		return nil, fmt.Errorf("Unable to bootstrap OVN, unable to unmarshal install-config: %s", err)
	}

	hc := platform.NewHyperShiftConfig()
	ovnConfigResult, err := bootstrapOVNConfig(conf, kubeClient, hc, infraStatus)
	if err != nil {
		return nil, fmt.Errorf("Unable to bootstrap OVN config, err: %v", err)
	}

	var controlPlaneReplicaCount int
	if hc.Enabled {
		controlPlaneReplicaCount = ovnConfigResult.HyperShiftConfig.ControlPlaneReplicas
	} else {
		controlPlaneReplicaCount, _ = strconv.Atoi(rcD.ControlPlane.Replicas)
	}

	ovnMasterAddresses, newTimeout, err := getMasterAddresses(kubeClient.ClientFor("").CRClient(), controlPlaneReplicaCount, hc.Enabled, OVN_MASTER_DISCOVERY_TIMEOUT)
	if err != nil {
		return nil, err
	}
	OVN_MASTER_DISCOVERY_TIMEOUT = newTimeout

	// clusterInitiator is used to avoid a split-brain scenario for the OVN NB/SB DBs. We want to consistently initialize
	// any OVN cluster which is bootstrapped here, to the same initiator (should it still exist), hence we annotate the
	// network.operator.openshift.io CRD with this information and always try to re-use the same member for the OVN RAFT
	// cluster initialization
	// This part is only needed in single-zone mode, will be removed in 4.15.
	var clusterInitiator string
	currentAnnotation := conf.GetAnnotations()
	if cInitiator, ok := currentAnnotation[names.OVNRaftClusterInitiator]; ok && currentInitiatorExists(ovnMasterAddresses, cInitiator) {
		clusterInitiator = cInitiator
	} else {
		clusterInitiator = ovnMasterAddresses[0]
		if currentAnnotation == nil {
			currentAnnotation = map[string]string{
				names.OVNRaftClusterInitiator: clusterInitiator,
			}
		} else {
			currentAnnotation[names.OVNRaftClusterInitiator] = clusterInitiator
		}
		conf.SetAnnotations(currentAnnotation)
	}

	// Retrieve existing daemonsets or statefulsets status - used for deciding if upgrades should happen
	var nsn types.NamespacedName
	nodeStatus := &bootstrap.OVNUpdateStatus{}         // for both interconnect multizone and single zone
	masterStatus := &bootstrap.OVNUpdateStatus{}       // for interconnect single zone (necessary for upgrades 4.13 -> 4.14)
	controlPlaneStatus := &bootstrap.OVNUpdateStatus{} // for interconnect multizone (default)
	ipsecStatus := &bootstrap.OVNUpdateStatus{}
	prepullerStatus := &bootstrap.OVNUpdateStatus{}

	namespaceForControlPlane := util.OVN_NAMESPACE
	clusterClientForControlPlane := kubeClient.ClientFor("")

	if hc.Enabled {
		clusterClientForControlPlane = kubeClient.ClientFor(names.ManagementClusterName)
		namespaceForControlPlane = hc.Namespace

		// only for 4.13 (during upgrade) and 4.14 single-zone
		masterStatefulSet := &appsv1.StatefulSet{
			TypeMeta: metav1.TypeMeta{
				Kind:       "StatefulSet",
				APIVersion: appsv1.SchemeGroupVersion.String(),
			},
		}
		nsn = types.NamespacedName{Namespace: namespaceForControlPlane, Name: util.OVN_MASTER}
		if err := clusterClientForControlPlane.CRClient().Get(context.TODO(), nsn, masterStatefulSet); err != nil {
			if !apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("Failed to retrieve existing %s statefulset: %w", util.OVN_MASTER, err)
			} else {
				klog.Infof("%s StatefulSet not running", util.OVN_MASTER)
				masterStatus = nil
			}
		} else {
			masterStatus.Kind = "StatefulSet"
			masterStatus.Namespace = masterStatefulSet.Namespace
			masterStatus.Name = masterStatefulSet.Name
			masterStatus.IPFamilyMode = masterStatefulSet.GetAnnotations()[names.NetworkIPFamilyModeAnnotation]
			masterStatus.ClusterNetworkCIDRs = masterStatefulSet.GetAnnotations()[names.ClusterNetworkCIDRsAnnotation]
			masterStatus.Version = masterStatefulSet.GetAnnotations()["release.openshift.io/version"]
			masterStatus.Progressing = statefulSetProgressing(masterStatefulSet)
			masterStatus.InterConnectEnabled = isInterConnectEnabledOnMasterStatefulSet(masterStatefulSet)
			masterStatus.InterConnectZoneMode = string(zoneModeSingleZone) // master is only used for single zone

			klog.Infof("%s StatefulSet status: IC=%t, zone-mode=%s, progressing=%t",
				util.OVN_MASTER, masterStatus.InterConnectEnabled, masterStatus.InterConnectZoneMode,
				masterStatus.Progressing)
		}

	} else {
		// only for 4.13 (during upgrade) and 4.14 single-zone
		masterDaemonSet := &appsv1.DaemonSet{
			TypeMeta: metav1.TypeMeta{
				Kind:       "DaemonSet",
				APIVersion: appsv1.SchemeGroupVersion.String(),
			},
		}

		nsn = types.NamespacedName{Namespace: util.OVN_NAMESPACE, Name: util.OVN_MASTER}
		var errMaster error
		if errMaster = kubeClient.ClientFor("").CRClient().Get(context.TODO(), nsn, masterDaemonSet); errMaster != nil {
			if !apierrors.IsNotFound(errMaster) {
				return nil, fmt.Errorf("Failed to retrieve existing %s DaemonSet: %w", util.OVN_MASTER, errMaster)
			} else {
				masterStatus = nil
				klog.Infof("%s DaemonSet not running", util.OVN_MASTER)
			}
		} else {
			masterStatus.Kind = "DaemonSet"
			masterStatus.Namespace = masterDaemonSet.Namespace
			masterStatus.Name = masterDaemonSet.Name
			masterStatus.IPFamilyMode = masterDaemonSet.GetAnnotations()[names.NetworkIPFamilyModeAnnotation]
			masterStatus.ClusterNetworkCIDRs = masterDaemonSet.GetAnnotations()[names.ClusterNetworkCIDRsAnnotation]
			masterStatus.Version = masterDaemonSet.GetAnnotations()["release.openshift.io/version"]
			masterStatus.Progressing = daemonSetProgressing(masterDaemonSet, false)
			masterStatus.InterConnectEnabled = isInterConnectEnabledOnMasterDaemonset(masterDaemonSet)
			masterStatus.InterConnectZoneMode = string(zoneModeSingleZone) // master is only used for single zone

			klog.Infof("%s DaemonSet status: IC=%t, zone-mode=%s, progressing=%t",
				util.OVN_MASTER, masterStatus.InterConnectEnabled, masterStatus.InterConnectZoneMode,
				masterStatus.Progressing)
		}

	}

	// control plane deployment (multizone only)
	controlPlaneDeployment := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Deployment",
			APIVersion: appsv1.SchemeGroupVersion.String(),
		},
	}

	nsn = types.NamespacedName{Namespace: namespaceForControlPlane, Name: util.OVN_CONTROL_PLANE}
	if err := clusterClientForControlPlane.CRClient().Get(context.TODO(), nsn, controlPlaneDeployment); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("Failed to retrieve %s deployment: %w", util.OVN_CONTROL_PLANE, err)
		} else {
			klog.Infof("%s deployment not running", util.OVN_CONTROL_PLANE)
			controlPlaneStatus = nil
		}
	} else {
		controlPlaneStatus.Kind = "Deployment"
		controlPlaneStatus.Namespace = controlPlaneDeployment.Namespace
		controlPlaneStatus.Name = controlPlaneDeployment.Name
		controlPlaneStatus.IPFamilyMode = controlPlaneDeployment.GetAnnotations()[names.NetworkIPFamilyModeAnnotation]
		controlPlaneStatus.ClusterNetworkCIDRs = controlPlaneDeployment.GetAnnotations()[names.ClusterNetworkCIDRsAnnotation]
		controlPlaneStatus.Version = controlPlaneDeployment.GetAnnotations()["release.openshift.io/version"]
		controlPlaneStatus.Progressing = deploymentProgressing(controlPlaneDeployment)
		controlPlaneStatus.InterConnectEnabled = true
		controlPlaneStatus.InterConnectZoneMode = string(zoneModeMultiZone) // only deployed in multizone

		klog.Infof("%s deployment status: zone-mode=%s, progressing=%t",
			util.OVN_CONTROL_PLANE, controlPlaneStatus.InterConnectZoneMode, controlPlaneStatus.Progressing)

	}

	// node daemonset: 4.13, 4.14 single-zone and multizone
	nodeDaemonSet := &appsv1.DaemonSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "DaemonSet",
			APIVersion: appsv1.SchemeGroupVersion.String(),
		},
	}
	nsn = types.NamespacedName{Namespace: util.OVN_NAMESPACE, Name: util.OVN_NODE}
	if err := kubeClient.ClientFor("").CRClient().Get(context.TODO(), nsn, nodeDaemonSet); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("Failed to retrieve existing ovnkube-node DaemonSet: %w", err)
		} else {
			nodeStatus = nil
			klog.Infof("ovnkube-node DaemonSet not running")
		}
	} else {
		nodeStatus.Kind = "DaemonSet"
		nodeStatus.Namespace = nodeDaemonSet.Namespace
		nodeStatus.Name = nodeDaemonSet.Name
		nodeStatus.IPFamilyMode = nodeDaemonSet.GetAnnotations()[names.NetworkIPFamilyModeAnnotation]
		nodeStatus.ClusterNetworkCIDRs = nodeDaemonSet.GetAnnotations()[names.ClusterNetworkCIDRsAnnotation]
		nodeStatus.Version = nodeDaemonSet.GetAnnotations()["release.openshift.io/version"]
		nodeStatus.Progressing = daemonSetProgressing(nodeDaemonSet, true)
		nodeStatus.InterConnectEnabled = isInterConnectEnabledOnNodeDaemonset(nodeDaemonSet)
		nodeStatus.InterConnectZoneMode = string(getInterConnectZoneModeForNodeDaemonSet(nodeDaemonSet))

		klog.Infof("ovnkube-node DaemonSet status: IC=%t,  zone-mode=%s, progressing=%t",
			nodeStatus.InterConnectEnabled, nodeStatus.InterConnectZoneMode, nodeStatus.Progressing)

	}

	prePullerDaemonSet := &appsv1.DaemonSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "DaemonSet",
			APIVersion: appsv1.SchemeGroupVersion.String(),
		},
	}
	nsn = types.NamespacedName{Namespace: util.OVN_NAMESPACE, Name: "ovnkube-upgrades-prepuller"}
	if err := kubeClient.ClientFor("").CRClient().Get(context.TODO(), nsn, prePullerDaemonSet); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("Failed to retrieve existing prepuller DaemonSet: %w", err)
		} else {
			prepullerStatus = nil
		}
	} else {
		prepullerStatus.Namespace = prePullerDaemonSet.Namespace
		prepullerStatus.Name = prePullerDaemonSet.Name
		prepullerStatus.IPFamilyMode = prePullerDaemonSet.GetAnnotations()[names.NetworkIPFamilyModeAnnotation]
		prepullerStatus.Version = prePullerDaemonSet.GetAnnotations()["release.openshift.io/version"]
		prepullerStatus.Progressing = daemonSetProgressing(prePullerDaemonSet, true)
	}

	ipsecDaemonSet := &appsv1.DaemonSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "DaemonSet",
			APIVersion: appsv1.SchemeGroupVersion.String(),
		},
	}
	nsn = types.NamespacedName{Namespace: util.OVN_NAMESPACE, Name: "ovn-ipsec"}
	if err := kubeClient.ClientFor("").CRClient().Get(context.TODO(), nsn, ipsecDaemonSet); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("Failed to retrieve existing ipsec DaemonSet: %w", err)
		} else {
			ipsecStatus = nil
		}
	} else {
		ipsecStatus.Namespace = ipsecDaemonSet.Namespace
		ipsecStatus.Name = ipsecDaemonSet.Name
		ipsecStatus.IPFamilyMode = ipsecDaemonSet.GetAnnotations()[names.NetworkIPFamilyModeAnnotation]
		ipsecStatus.Version = ipsecDaemonSet.GetAnnotations()["release.openshift.io/version"]
	}

	// If we are upgrading from 4.13 -> 4.14 set new API for IP Forwarding mode to Global.
	// This is to ensure backwards compatibility.
	if masterStatus != nil {
		klog.Infof("4.13 -> 4.14 upgrade detected. Will set IP Forwarding API to Global mode for backwards compatibility")
		if conf.Spec.DefaultNetwork.OVNKubernetesConfig.GatewayConfig == nil {
			conf.Spec.DefaultNetwork.OVNKubernetesConfig.GatewayConfig = &operv1.GatewayConfig{}
		}
		conf.Spec.DefaultNetwork.OVNKubernetesConfig.GatewayConfig.IPForwarding = operv1.IPForwardingGlobal
	}

	res := bootstrap.OVNBootstrapResult{
		MasterAddresses:          ovnMasterAddresses,
		ClusterInitiator:         clusterInitiator,
		ControlPlaneUpdateStatus: controlPlaneStatus,
		MasterUpdateStatus:       masterStatus, // TODO to be removed in 4.15, after making 4.13->4.14 upgrade required
		NodeUpdateStatus:         nodeStatus,
		IPsecUpdateStatus:        ipsecStatus,
		PrePullerUpdateStatus:    prepullerStatus,
		OVNKubernetesConfig:      ovnConfigResult,
		FlowsConfig:              bootstrapFlowsConfig(kubeClient.ClientFor("").CRClient()),
	}
	return &res, nil
}

// bootstrapFlowsConfig looks for the openshift-network-operator/ovs-flows-config configmap, and
// returns it or returns nil if it does not exist (or can't be properly parsed).
// Usually, the second argument will be net.LookupIP
func bootstrapFlowsConfig(cl crclient.Reader) *bootstrap.FlowsConfig {
	cm := corev1.ConfigMap{}
	if err := cl.Get(context.TODO(), types.NamespacedName{
		Name:      OVSFlowsConfigMapName,
		Namespace: OVSFlowsConfigNamespace,
	}, &cm); err != nil {
		if !apierrors.IsNotFound(err) {
			klog.Warningf("%s: error fetching configmap: %v", OVSFlowsConfigMapName, err)
		}
		// ovs-flows-config is not defined. Ignoring from bootstrap
		return nil
	}
	fc := bootstrap.FlowsConfig{}
	// fetching string fields and transforming them to OVS format
	if st, ok := cm.Data["sharedTarget"]; ok {
		fc.Target = st
	} else if np, ok := cm.Data["nodePort"]; ok {
		// empty host will be interpreted as Node IP by ovn-kubernetes
		fc.Target = ":" + np
	} else {
		klog.Warningf("%s: wrong data section: either sharedTarget or nodePort sections are needed: %+v",
			OVSFlowsConfigMapName, cm.Data)
		return nil
	}

	if catStr, ok := cm.Data["cacheActiveTimeout"]; ok {
		if catd, err := time.ParseDuration(catStr); err != nil {
			klog.Warningf("%s: wrong cacheActiveTimeout value %s. Ignoring: %v",
				OVSFlowsConfigMapName, catStr, err)
		} else {
			catf := catd.Seconds()
			catu := uint(catf)
			if catf != float64(catu) {
				klog.Warningf("%s: cacheActiveTimeout %s will be truncated to %d seconds",
					OVSFlowsConfigMapName, catStr, catu)
			}
			fc.CacheActiveTimeout = &catu
		}
	}

	if cmfStr, ok := cm.Data["cacheMaxFlows"]; ok {
		if cmf, err := strconv.ParseUint(cmfStr, 10, 32); err != nil {
			klog.Warningf("%s: wrong cacheMaxFlows value %s. Ignoring: %v",
				OVSFlowsConfigMapName, cmfStr, err)
		} else {
			cmfu := uint(cmf)
			fc.CacheMaxFlows = &cmfu
		}
	}

	if sStr, ok := cm.Data["sampling"]; ok {
		if sampling, err := strconv.ParseUint(sStr, 10, 32); err != nil {
			klog.Warningf("%s: wrong sampling value %s. Ignoring: %v",
				OVSFlowsConfigMapName, sStr, err)
		} else {
			su := uint(sampling)
			fc.Sampling = &su
		}
	}

	return &fc
}

func currentInitiatorExists(ovnMasterAddresses []string, configInitiator string) bool {
	for _, masterIP := range ovnMasterAddresses {
		if masterIP == configInitiator {
			return true
		}
	}
	return false
}

func dbList(masterIPs []string, port string) string {
	addrs := make([]string, len(masterIPs))
	for i, ip := range masterIPs {
		addrs[i] = "ssl:" + net.JoinHostPort(ip, port)
	}
	return strings.Join(addrs, ",")
}

func listenDualStack(masterIP string) string {
	if strings.Contains(masterIP, ":") {
		// IPv6 master, make the databases listen dual-stack
		return ":[::]"
	} else {
		// IPv4 master, be IPv4-only for backward-compatibility
		return ""
	}
}

func getClusterCIDRsFromConfig(conf *operv1.NetworkSpec) string {
	// pretty print the clusterNetwork CIDR (possibly only one) in its annotation
	var clusterNetworkCIDRs []string
	for _, c := range conf.ClusterNetwork {
		clusterNetworkCIDRs = append(clusterNetworkCIDRs, c.CIDR)
	}
	return strings.Join(clusterNetworkCIDRs, ",")
}

func getIPFamilyAndClusterCIDRsAnnotationOrConfig(conf *operv1.NetworkSpec, ovn bootstrap.OVNBootstrapResult, configValue string) (string, string) {
	var IPFamilyMode, clusterNetworkCIDRs, source string

	if ovn.NodeUpdateStatus != nil && ovn.NodeUpdateStatus.IPFamilyMode != "" && ovn.NodeUpdateStatus.IPFamilyMode != configValue {
		IPFamilyMode = ovn.NodeUpdateStatus.IPFamilyMode
		clusterNetworkCIDRs = ovn.NodeUpdateStatus.ClusterNetworkCIDRs
		source = util.OVN_NODE

	} else if ovn.MasterUpdateStatus != nil && ovn.MasterUpdateStatus.IPFamilyMode != "" && ovn.MasterUpdateStatus.IPFamilyMode != configValue {
		IPFamilyMode = ovn.MasterUpdateStatus.IPFamilyMode
		clusterNetworkCIDRs = ovn.MasterUpdateStatus.ClusterNetworkCIDRs
		source = util.OVN_MASTER

	} else if ovn.ControlPlaneUpdateStatus != nil && ovn.ControlPlaneUpdateStatus.IPFamilyMode != "" && ovn.ControlPlaneUpdateStatus.IPFamilyMode != configValue {
		IPFamilyMode = ovn.ControlPlaneUpdateStatus.IPFamilyMode
		clusterNetworkCIDRs = ovn.ControlPlaneUpdateStatus.ClusterNetworkCIDRs
		source = util.OVN_CONTROL_PLANE
	} else {
		IPFamilyMode = configValue
		clusterNetworkCIDRs = getClusterCIDRsFromConfig(conf)
		source = "config"
	}

	klog.Infof("Got IPFamily=%s and ClusterNetworkCIDRs=%s from %s", IPFamilyMode, clusterNetworkCIDRs, source)
	return IPFamilyMode, clusterNetworkCIDRs
}

func handleOVNKUpdateUponOpenshiftUpgrade(conf *operv1.NetworkSpec, ovn bootstrap.OVNBootstrapResult) (updateNode, updateMaster, updateControlPlane bool) {
	var updateMasterOrControlPlane bool
	masterOrControlPlaneStatus := ovn.ControlPlaneUpdateStatus // in multizone
	if ovn.MasterUpdateStatus != nil {                         // only in single zone mode
		masterOrControlPlaneStatus = ovn.MasterUpdateStatus
	}

	updateNode, updateMasterOrControlPlane = shouldUpdateOVNKonUpgrade(ovn, masterOrControlPlaneStatus, os.Getenv("RELEASE_VERSION"))

	return updateNode, updateMasterOrControlPlane, updateMasterOrControlPlane

}

// handleIPFamilyAnnotationAndIPFamilyChange reads the desired IP family mode (single or dual stack) from config,
// and annotates the ovnk DaemonSet/StatefulSet/deployment with that value.
// If the config value is different from the current mode and we're not already migrating zone mode, then it applies
// the new mode first to the ovnkube-node DaemonSet and then to the control plane (or master, in single zone). If zone
// migration is ongoing, it continues to annotate the ovnk DaemonSet/StatefulSet/deployment with the current value until
// the zone mode migration is over, at which point it starts the migration to the new IP family mode.
func handleIPFamilyAnnotationAndIPFamilyChange(conf *operv1.NetworkSpec, ovn bootstrap.OVNBootstrapResult, objs *[]*uns.Unstructured,
	zoneModeMigrationIsOngoing, updateNode, updateMaster, updateControlPlane bool) (bool, bool, bool, error) {

	newUpdateNode, newUpdateMaster, newUpdateControlPlane := updateNode, updateMaster, updateControlPlane

	// obtain the new IP family mode from config: single or dual stack
	ipFamilyModeFromConfig := names.IPFamilySingleStack
	if len(conf.ServiceNetwork) == 2 {
		ipFamilyModeFromConfig = names.IPFamilyDualStack
	}
	ipFamilyMode := ipFamilyModeFromConfig
	var clusterNetworkCIDRs string

	if !zoneModeMigrationIsOngoing && updateNode && updateMaster && updateControlPlane {
		clusterNetworkCIDRs = getClusterCIDRsFromConfig(conf)

		var updateMasterOrControlPlane bool
		masterOrControlPlaneStatus := ovn.ControlPlaneUpdateStatus // in multizone
		if ovn.MasterUpdateStatus != nil {                         // only in single zone mode
			masterOrControlPlaneStatus = ovn.MasterUpdateStatus
		}
		// check if the IP family mode has changed and control the conversion process.
		newUpdateNode, updateMasterOrControlPlane = shouldUpdateOVNKonIPFamilyChange(ovn, masterOrControlPlaneStatus, ipFamilyMode)
		klog.Infof("IP family change: updateNode=%t, updateMasterOrControlPlane=%t", updateNode, updateMasterOrControlPlane)

		newUpdateMaster = updateMasterOrControlPlane
		newUpdateControlPlane = updateMasterOrControlPlane

	} else {
		// skip IP family migration if we're already switching zone mode
		// Annotate DaemonSet/StatefulSet/deployment with old value: get to new value once zone migration is over
		klog.Infof("IP family migration (if any) is post-poned until zone migration is complete")
		ipFamilyMode, clusterNetworkCIDRs = getIPFamilyAndClusterCIDRsAnnotationOrConfig(conf, ovn, ipFamilyMode)

	}
	// (always) annotate the daemonset and the daemonset template with the current IP family mode.
	// This triggers a daemonset restart if there are changes.
	err := setOVNObjectAnnotation(*objs, names.NetworkIPFamilyModeAnnotation, ipFamilyMode)
	if err != nil {
		return true, true, true, errors.Wrapf(err, "failed to set IP family %s annotation on daemonsets or statefulsets", ipFamilyMode)
	}

	err = setOVNObjectAnnotation(*objs, names.ClusterNetworkCIDRsAnnotation, clusterNetworkCIDRs)
	if err != nil {
		return true, true, true, errors.Wrapf(err, "failed to set %s annotation on daemonsets/statefulsets/deployments", clusterNetworkCIDRs)
	}

	updateNode = newUpdateNode && updateNode
	updateMaster = newUpdateMaster && updateMaster
	updateControlPlane = newUpdateControlPlane && updateControlPlane

	return updateNode, updateMaster, updateControlPlane, nil
}

// shouldUpdateOVNKonIPFamilyChange determines if we should roll out changes to
// the master/control-plane and node objects on IP family configuration changes.
// We rollout changes on master/control-plane first when there is a configuration change.
// Configuration changes take precedence over upgrades.
func shouldUpdateOVNKonIPFamilyChange(ovn bootstrap.OVNBootstrapResult, masterOrControlPlaneStatus *bootstrap.OVNUpdateStatus, ipFamilyMode string) (updateNode, updateMaster bool) {
	// Fresh cluster - full steam ahead!
	if ovn.NodeUpdateStatus == nil || masterOrControlPlaneStatus == nil {
		return true, true
	}
	// check current IP family mode
	nodeIPFamilyMode := ovn.NodeUpdateStatus.IPFamilyMode
	masterOrControlPlaneIPFamilyMode := masterOrControlPlaneStatus.IPFamilyMode
	klog.Infof("IP family mode: node=%s, masterOrControlPlane=%s", nodeIPFamilyMode, masterOrControlPlaneIPFamilyMode)
	// if there are no annotations this is a fresh cluster
	if nodeIPFamilyMode == "" || masterOrControlPlaneIPFamilyMode == "" {
		return true, true
	}
	// return if there are no IP family mode changes
	if nodeIPFamilyMode == ipFamilyMode && masterOrControlPlaneIPFamilyMode == ipFamilyMode {
		return true, true
	}
	// If the master/control-plane config has changed update only the master/control-plane, the node will be updated later
	if masterOrControlPlaneIPFamilyMode != ipFamilyMode {
		klog.V(2).Infof("IP family mode change detected to %s, updating OVN-Kubernetes master", ipFamilyMode)
		return false, true
	}
	// Don't rollout the changes on nodes until the master/control-plane rollout has finished
	if masterOrControlPlaneStatus.Progressing {
		klog.V(2).Infof("Waiting for IP family mode rollout of OVN-Kubernetes master/control-plane before updating node")
		return false, true
	}

	klog.V(2).Infof("OVN-Kubernetes master/control-plane rollout complete, updating IP family mode on node daemonset")
	return true, true
}

// shouldUpdateOVNKonPrepull implements a simple pre-pulling daemonset. It ensures the ovn-k
// container image is (probably) already pulled by every node.
// If the existing node daemonset has a different version then what we would like to apply, we first
// roll out a no-op daemonset. Then, when that has rolled out to 100% of the cluster or has stopped
// progressing, proceed with the node upgrade.
func shouldUpdateOVNKonPrepull(ovn bootstrap.OVNBootstrapResult, releaseVersion string) (updateNode, renderPrepull bool) {
	// Fresh cluster - full steam ahead! No need to wait for pre-puller.
	if ovn.NodeUpdateStatus == nil {
		klog.V(3).Infof("Fresh cluster, no need for prepuller")
		return true, false
	}

	// if node is already upgraded, then no need to pre-pull
	// Return true so that we reconcile any changes that somehow could have happened.
	existingNodeVersion := ovn.NodeUpdateStatus.Version
	if existingNodeVersion == releaseVersion {
		klog.V(3).Infof("OVN-Kubernetes node is already in the expected release.")
		return true, false
	}

	// at this point, we've determined we need an upgrade
	if ovn.PrePullerUpdateStatus == nil {
		klog.Infof("Rolling out the no-op prepuller daemonset...")
		return false, true
	}

	// If pre-puller just pulled a new upgrade image and then we
	// downgrade immediately, we might wanna make prepuller pull the downgrade image.
	existingPrePullerVersion := ovn.PrePullerUpdateStatus.Version
	if existingPrePullerVersion != releaseVersion {
		klog.Infof("Rendering prepuller daemonset to update its image...")
		return false, true
	}

	if ovn.PrePullerUpdateStatus.Progressing {
		klog.Infof("Waiting for ovnkube-upgrades-prepuller daemonset to finish pulling the image before updating node")
		return false, true
	}

	klog.Infof("OVN-Kube upgrades-prepuller daemonset rollout complete, now starting node rollouts")
	return true, false
}

// shouldUpdateOVNKonUpgrade determines if we should roll out changes to
// the master and node daemonsets and control plane deployment (according to the cluster zone mode) upon upgrades.
// We roll out node first, then master and/or control plane. In downgrades, we do the opposite.
func shouldUpdateOVNKonUpgrade(ovn bootstrap.OVNBootstrapResult, masterOrControlPlaneStatus *bootstrap.OVNUpdateStatus, releaseVersion string) (updateNode, updateMaster bool) {
	// Fresh cluster - full steam ahead!
	if ovn.NodeUpdateStatus == nil || masterOrControlPlaneStatus == nil {
		return true, true
	}

	nodeVersion := ovn.NodeUpdateStatus.Version
	masterVersion := masterOrControlPlaneStatus.Version

	// shortcut - we're all rolled out.
	// Return true so that we reconcile any changes that somehow could have happened.
	if nodeVersion == releaseVersion && masterVersion == releaseVersion {
		klog.V(2).Infof("OVN-Kubernetes master/control-plane and node already at release version %s; no changes required", releaseVersion)
		return true, true
	}

	// compute version delta
	// versionUpgrade means the existing daemonSet needs an upgrade.
	masterDelta := compareVersions(masterVersion, releaseVersion)
	nodeDelta := compareVersions(nodeVersion, releaseVersion)

	if masterDelta == versionUnknown || nodeDelta == versionUnknown {
		klog.Warningf("could not determine ovn-kubernetes daemonset update directions; node: %s, master/control-plane: %s, release: %s",
			nodeVersion, masterVersion, releaseVersion)
		return true, true
	}

	klog.V(2).Infof("OVN-Kubernetes master/control-plane version %s -> latest %s; delta %s", masterVersion, releaseVersion, masterDelta)
	klog.V(2).Infof("OVN-Kubernetes node version %s -> latest %s; delta %s", nodeVersion, releaseVersion, nodeDelta)

	// 9 cases
	// +-------------+---------------+-----------------+------------------+
	// |    Delta    |  master upg.  |    master OK    |   master downg.  |
	// +-------------+---------------+-----------------+------------------+
	// | node upg.   | upgrade node  | error           | error            |
	// | node OK     | wait for node | done            | error            |
	// | node downg. | error         | wait for master | downgrade master |
	// +-------------+---------------+-----------------+------------------++

	// both older (than CNO)
	// Update node only.
	if masterDelta == versionUpgrade && nodeDelta == versionUpgrade {
		klog.V(2).Infof("Upgrading OVN-Kubernetes node before master/control-plane")
		return true, false
	}

	// master older, node updated
	// update master if node is rolled out
	if masterDelta == versionUpgrade && nodeDelta == versionSame {
		if ovn.NodeUpdateStatus.Progressing {
			klog.V(2).Infof("Waiting for OVN-Kubernetes node update to roll out before updating master/control-plane")
			return true, false
		}
		klog.V(2).Infof("OVN-Kubernetes node update rolled out; now updating master/control-plane")
		return true, true
	}

	// both newer
	// downgrade master before node
	if masterDelta == versionDowngrade && nodeDelta == versionDowngrade {
		klog.V(2).Infof("Downgrading OVN-Kubernetes master/control-plane before node")
		return false, true
	}

	// master same, node needs downgrade
	// wait for master rollout
	if masterDelta == versionSame && nodeDelta == versionDowngrade {
		if masterOrControlPlaneStatus.Progressing {
			klog.V(2).Infof("Waiting for OVN-Kubernetes master/control-plane downgrade to roll out before downgrading node")
			return false, true
		}
		klog.V(2).Infof("OVN-Kubernetes master/control-plane update rolled out; now downgrading node")
		return true, true
	}

	// unlikely, should be caught above
	if masterDelta == versionSame && nodeDelta == versionSame {
		return true, true
	}

	klog.Warningf("OVN-Kubernetes daemonset versions inconsistent. node: %s, master/control-plane: %s, release: %s",
		nodeVersion, masterVersion, releaseVersion)
	return true, true
}

func getProgressingState(ovn bootstrap.OVNBootstrapResult) string {
	var node, master, controlPlane string

	node = "nil"
	if ovn.NodeUpdateStatus != nil {
		node = fmt.Sprintf("%t", ovn.NodeUpdateStatus.Progressing)
	}

	master = "nil"
	if ovn.MasterUpdateStatus != nil {
		master = fmt.Sprintf("%t", ovn.MasterUpdateStatus.Progressing)
	}

	controlPlane = "nil"
	if ovn.ControlPlaneUpdateStatus != nil {
		controlPlane = fmt.Sprintf("%t", ovn.ControlPlaneUpdateStatus.Progressing)
	}

	return fmt.Sprintf("progressing: %s=%s, %s=%s, %s=%s",
		util.OVN_NODE, node, util.OVN_MASTER, master, util.OVN_CONTROL_PLANE, controlPlane)
}

// shouldUpdateOVNKonInterConnectZoneModeChange determines if we should roll out changes to
// the node daemonset (single zone, multizone), to the master daemonset (single zone only) and
// to the control plane deployment (multizone only), when the interconnect zone mode changes.
//
// When switching from single zone to multizone, we start with:
// - single-zone node DaemonSet
// - (single-zone) master DaemonSet
// We then go through an intermediate step with:
// - multizone node DaemonSet
// - (multizone) control plane deployment
// - (single-zone) master DaemonSet
// This allows us to always have an instance of cluster manager running throughout the zone mode change.
// We reach this intermediate step by first rolling out the multizone node DaemonSet and then (at the same time)
// the control plane deployment and master DaemonSet.
// Once all three components have rolled out, we simply remove the old (single-zone) master DaemonSet.
//
// For multizone to single zone, which is not officially supported, we do the same steps backwards.
//
// The whole procedure allows us to always have a working deployed ovnk while changing zone mode.
//
// To sum up:
// - single zone -> multizone:   first roll out node,   then master+control plane; finally, remove master.
// - multizone   -> single zone: first roll out master + control plane, then node; finally, remove control plane.
func shouldUpdateOVNKonInterConnectZoneModeChange(ovn bootstrap.OVNBootstrapResult, targetZoneMode InterConnectZoneMode) (updateNode, updateMaster, addControlPlane bool) {

	if ovn.NodeUpdateStatus == nil || ovn.MasterUpdateStatus == nil && ovn.ControlPlaneUpdateStatus == nil {
		// Fresh cluster - full steam ahead!
		return true, true, true
	}

	// When both DaemonSets are in 4.13, we're in phase 1 of the upgrade from a non-IC version;
	// phase 1 is carried out by shouldUpdateOVNKonUpgrade. Nothing to do here.
	if !ovn.NodeUpdateStatus.InterConnectEnabled ||
		ovn.MasterUpdateStatus != nil && !ovn.MasterUpdateStatus.InterConnectEnabled {
		return true, true, true
	}

	// The statuses of ovnkube-node, ovnkube-master and ovnkube-control-plane must be taken
	// into account. The logic is simplified because:
	// - ovnkube-control-plane can only be multizone, since it doesn't exist in single zone
	// - ovnkube-master can only be single zone, since it doesn't exist in multizone

	if targetZoneMode == zoneModeMultiZone {

		// First step, node is still in single zone: update it to multizone,
		// leave master as is (no update anyway) and don't add control plane yet
		if ovn.NodeUpdateStatus.InterConnectZoneMode == string(zoneModeSingleZone) {
			// Note that updateMaster=false has actually no effect, since the YAML
			// of single-zone master and multi-zone-tmp master are the same
			klog.Infof("target=multizone: ovnkube-node DaemonSet is single zone, update it to multizone")
			return true, false, false

		} else if ovn.NodeUpdateStatus.InterConnectZoneMode == string(zoneModeMultiZone) {
			// Second step, node is already multizone: leave master as is and add control plane only if node DaemonSet is done progressing
			if ovn.NodeUpdateStatus.Progressing {
				klog.Infof("target=multizone: wait for multizone ovnkube-node to roll out before rolling "+
					"out ovnkube-control-plane (%s)", getProgressingState(ovn))
				return true, false, false
			}
			klog.Infof("target=multizone: ovnkube-node is already multizone, add ovnkube-control-plane " +
				"if not already present (and do a no-op update on ovnkube-master)")
			return true, true, true
		} else {
			klog.Warningf("target=multizone: undefined zone mode for ovnkube-node")
			return true, true, true
		}

	} else if targetZoneMode == zoneModeSingleZone {
		// Multizone -> single zone
		// First roll out master + control plane, then node; then remove control plane

		// Node is already single zone: master should have been added already,
		// control plane should have been kept (or removed), so update all at this point
		if ovn.NodeUpdateStatus.InterConnectZoneMode == string(zoneModeSingleZone) {
			klog.Infof("target=singlezone: ovnkube-node DaemonSet is already single zone")
			return true, true, true

		} else if ovn.NodeUpdateStatus.InterConnectZoneMode == string(zoneModeMultiZone) {
			// node is still multizone: update node only if master and control plane (if any) are done progressing
			if ovn.MasterUpdateStatus != nil && ovn.MasterUpdateStatus.Progressing ||
				ovn.ControlPlaneUpdateStatus != nil && ovn.ControlPlaneUpdateStatus.Progressing {
				klog.Infof("target=singlezone: wait for ovnkube-master and ovnkube-control-plane to roll out before rolling "+
					"out single-zone ovnkube-node (%s)", getProgressingState(ovn))
				return false, true, true
			}
			klog.Infof("target=singlezone: ovnkube-master and ovnkube-control-plane have rolled out, update node to single zone")
			return true, true, true

		}
		klog.Warningf("target=singlezone: undefined zone mode for ovnkube-node")
		return true, true, true // should not happen
	}
	klog.Warningf("undefined target zone mode")
	return true, true, true // should not happen

}

// daemonSetProgressing returns true if a daemonset is rolling out a change.
// If allowHung is true, then treat a daemonset hung at 90% as "done" for our purposes.
func daemonSetProgressing(ds *appsv1.DaemonSet, allowHung bool) bool {
	status := ds.Status

	// Copy-pasted from status_manager: Determine if a DaemonSet is progressing
	progressing := (status.UpdatedNumberScheduled < status.DesiredNumberScheduled ||
		status.NumberUnavailable > 0 ||
		status.NumberAvailable == 0 ||
		ds.Generation > status.ObservedGeneration)

	s := "progressing"
	if !progressing {
		s = "complete"
	}
	klog.V(2).Infof("daemonset %s/%s rollout %s; %d/%d scheduled; %d unavailable; %d available; generation %d -> %d",
		ds.Namespace, ds.Name, s, status.UpdatedNumberScheduled, status.DesiredNumberScheduled,
		status.NumberUnavailable, status.NumberAvailable, ds.Generation, status.ObservedGeneration)

	if !progressing {
		klog.V(2).Infof("daemonset %s/%s rollout complete", ds.Namespace, ds.Name)
		return false
	}

	// If we're hung, but max(90% of nodes, 1) have been updated, then act as if not progressing
	if allowHung {
		_, hung := ds.GetAnnotations()[names.RolloutHungAnnotation]
		maxBehind := int(math.Max(1, math.Floor(float64(status.DesiredNumberScheduled)*0.1)))
		numBehind := int(status.DesiredNumberScheduled - status.UpdatedNumberScheduled)
		if hung && numBehind <= maxBehind {
			klog.Warningf("daemonset %s/%s rollout seems to have hung with %d/%d behind, force-continuing", ds.Namespace, ds.Name, numBehind, status.DesiredNumberScheduled)
			return false
		}
	}

	return true
}

// statefulSetProgressing returns true if a statefulset is rolling out a change.
func statefulSetProgressing(ss *appsv1.StatefulSet) bool {
	status := ss.Status

	progressing := status.UpdatedReplicas < status.Replicas ||
		status.AvailableReplicas < status.Replicas ||
		ss.Generation > status.ObservedGeneration

	s := "progressing"
	if !progressing {
		s = "complete"
	}
	klog.V(2).Infof("statefulset %s/%s rollout %s; %d/%d scheduled; %d available; generation %d -> %d",
		ss.Namespace, ss.Name, s, status.ReadyReplicas, status.Replicas,
		status.AvailableReplicas, ss.Generation, status.ObservedGeneration)

	if !progressing {
		klog.V(2).Infof("statefulset %s/%s rollout complete", ss.Namespace, ss.Name)
		return false
	}

	return true
}

// deploymentProgressing returns true if a deployment is rolling out a change.
func deploymentProgressing(d *appsv1.Deployment) bool {
	status := d.Status

	progressing := status.UpdatedReplicas < status.Replicas ||
		status.AvailableReplicas < status.Replicas ||
		d.Generation > status.ObservedGeneration

	s := "progressing"
	if !progressing {
		s = "complete"
	}
	klog.V(2).Infof("deployment %s/%s rollout %s; %d/%d scheduled; %d available; generation %d -> %d",
		d.Namespace, d.Name, s, status.ReadyReplicas, status.Replicas,
		status.AvailableReplicas, d.Generation, status.ObservedGeneration)

	if !progressing {
		klog.V(2).Infof("deployment %s/%s rollout complete", d.Namespace, d.Name)
		return false
	}

	return true
}

// setOVNObjectAnnotation annotates the OVNkube master, node and control plane
// it also annotated the template with the provided key and value to force the rollout
func setOVNObjectAnnotation(objs []*uns.Unstructured, key, value string) error {
	for _, obj := range objs {
		if obj.GetAPIVersion() == "apps/v1" &&
			(obj.GetKind() == "DaemonSet" || obj.GetKind() == "StatefulSet" || obj.GetKind() == "Deployment") &&
			(obj.GetName() == util.OVN_NODE ||
				obj.GetName() == util.OVN_MASTER ||
				obj.GetName() == util.OVN_CONTROL_PLANE) {
			// set daemonset annotation
			anno := obj.GetAnnotations()
			if anno == nil {
				anno = map[string]string{}
			}
			anno[key] = value
			obj.SetAnnotations(anno)

			// set pod template annotation
			anno, _, _ = uns.NestedStringMap(obj.Object, "spec", "template", "metadata", "annotations")
			if anno == nil {
				anno = map[string]string{}
			}
			anno[key] = value
			if err := uns.SetNestedStringMap(obj.Object, anno, "spec", "template", "metadata", "annotations"); err != nil {
				return err
			}
		}
	}
	return nil
}

func isV4InternalSubnetLargeEnough(conf *operv1.NetworkSpec) bool {
	var maxNodesNum int
	subnet := conf.DefaultNetwork.OVNKubernetesConfig.V4InternalSubnet
	addrLen := 32
	for _, n := range conf.ClusterNetwork {
		if utilnet.IsIPv6CIDRString(n.CIDR) {
			continue
		}
		mask, _ := strconv.Atoi(strings.Split(n.CIDR, "/")[1])
		nodesNum := 1 << (int(n.HostPrefix) - mask)
		maxNodesNum += nodesNum
	}
	// We need to ensure each node can be assigned an IP address from the internal subnet
	intSubnetMask, _ := strconv.Atoi(strings.Split(subnet, "/")[1])
	// reserve one IP for the gw, one IP for network and one for broadcasting
	return maxNodesNum < (1<<(addrLen-intSubnetMask) - 3)
}

func isV6InternalSubnetLargeEnough(conf *operv1.NetworkSpec) bool {
	var addrLen uint32
	maxNodesNum, nodesNum, capacity := new(big.Int), new(big.Int), new(big.Int)
	subnet := conf.DefaultNetwork.OVNKubernetesConfig.V6InternalSubnet
	addrLen = 128
	for _, n := range conf.ClusterNetwork {
		if !utilnet.IsIPv6CIDRString(n.CIDR) {
			continue
		}
		mask, _ := strconv.Atoi(strings.Split(n.CIDR, "/")[1])
		nodesNum.Lsh(big.NewInt(1), uint(n.HostPrefix)-uint(mask))
		maxNodesNum.Add(maxNodesNum, nodesNum)
	}
	// We need to ensure each node can be assigned an IP address from the internal subnet
	intSubnetMask, _ := strconv.Atoi(strings.Split(subnet, "/")[1])
	capacity.Lsh(big.NewInt(1), uint(addrLen)-uint(intSubnetMask))
	// reserve one IP for the gw, one IP for network and one for broadcasting
	return capacity.Cmp(maxNodesNum.Add(maxNodesNum, big.NewInt(3))) != -1
}

// Determine the zone mode by looking for a known container name in multizone mode.
func getInterConnectZoneModeForNodeDaemonSet(ds *appsv1.DaemonSet) InterConnectZoneMode {
	for _, container := range ds.Spec.Template.Spec.Containers {
		if container.Name == "nbdb" {
			return zoneModeMultiZone
		}
	}
	return zoneModeSingleZone
}

func isInterConnectEnabledOnMasterStatefulSet(ss *appsv1.StatefulSet) bool {
	for _, container := range ss.Spec.Template.Spec.Containers {
		if container.Name == util.OVN_MASTER {
			for _, c := range container.Command {
				if strings.Contains(c, "--enable-interconnect") {
					return true
				}
			}
			break
		}
	}
	return false
}

func isInterConnectEnabledOnDaemonset(ds *appsv1.DaemonSet, containerName string) bool {
	for _, container := range ds.Spec.Template.Spec.Containers {
		if container.Name == containerName {
			for _, c := range container.Command {
				if strings.Contains(c, "--enable-interconnect") {
					return true
				}
			}
			break
		}
	}
	return false
}

func isInterConnectEnabledOnNodeDaemonset(ds *appsv1.DaemonSet) bool {
	return isInterConnectEnabledOnDaemonset(ds, util.OVN_NODE) || isInterConnectEnabledOnDaemonset(ds, util.OVN_CONTROLLER)
}

func isInterConnectEnabledOnMasterDaemonset(ds *appsv1.DaemonSet) bool {
	return isInterConnectEnabledOnDaemonset(ds, util.OVN_MASTER)
}

// migration from single zone to multizone is about to start if:
// - target is multizone during a simple zone migration, single zone during an upgrade from a non-interconnect version (< 4.14)
// - node is running, is >= 4.14 (--interconnect-enabled), is in single zone and is not progressing
// - master is running, is >= 4.14 (--interconnect-enabled), is in single zone and is not progressing
// - control plane is not running
func isMigrationToMultiZoneAboutToStart(ovn bootstrap.OVNBootstrapResult, targetZoneMode *targetZoneModeType, assessEndOfUpgradePhase1 bool) bool {
	targetZoneModeValue := zoneModeMultiZone
	if assessEndOfUpgradePhase1 {
		targetZoneModeValue = zoneModeSingleZone
	}
	return targetZoneMode.zoneMode == targetZoneModeValue &&
		ovn.NodeUpdateStatus != nil && ovn.MasterUpdateStatus != nil && ovn.ControlPlaneUpdateStatus == nil &&
		ovn.NodeUpdateStatus.InterConnectEnabled &&
		ovn.MasterUpdateStatus.InterConnectEnabled &&
		ovn.NodeUpdateStatus.InterConnectZoneMode == string(zoneModeSingleZone) &&
		!ovn.NodeUpdateStatus.Progressing &&
		!ovn.MasterUpdateStatus.Progressing
}

// migration from single zone to multizone is ongoing if:
//   - target is multizone
//   - node is running, is >=4.14, is already in multizone (progressing or not)
//   - master is running, is >= 4/14 (expected not to be progressing, but let's relax this condition
//     in case any error occurs on the pod, causing any master pod to restart and its status to be shown as "progressing")
//   - control plane either is not running (at the start, when multizone node is rolling out) or
//     is progressing (at the end, when node is already multizone)
func isMigrationToMultiZoneOngoing(ovn bootstrap.OVNBootstrapResult, targetZoneMode *targetZoneModeType) bool {
	return targetZoneMode.zoneMode == zoneModeMultiZone &&
		ovn.NodeUpdateStatus != nil &&
		ovn.NodeUpdateStatus.InterConnectEnabled &&
		ovn.NodeUpdateStatus.InterConnectZoneMode == string(zoneModeMultiZone) && // can be progressing or not

		ovn.MasterUpdateStatus != nil &&
		ovn.MasterUpdateStatus.InterConnectEnabled &&
		(ovn.ControlPlaneUpdateStatus == nil ||
			ovn.ControlPlaneUpdateStatus != nil && ovn.ControlPlaneUpdateStatus.Progressing && !ovn.NodeUpdateStatus.Progressing)
}

// migration from single zone to multizone is complete if:
//   - target is multizone
//   - node, is running, is >=4.14, is in multizone, is not progressing
//   - master is either running, is >= 4/14, is not progressing  or it's not running at all;
//     warning: master will be removed in the final step
//   - control plane either is running and not progressing
func isMigrationToMultiZoneComplete(ovn bootstrap.OVNBootstrapResult, targetZoneMode *targetZoneModeType) bool {
	return targetZoneMode.zoneMode == zoneModeMultiZone &&
		ovn.NodeUpdateStatus != nil && ovn.ControlPlaneUpdateStatus != nil &&
		ovn.NodeUpdateStatus.InterConnectEnabled &&
		ovn.NodeUpdateStatus.InterConnectZoneMode == string(zoneModeMultiZone) &&
		!ovn.NodeUpdateStatus.Progressing &&
		!ovn.ControlPlaneUpdateStatus.Progressing &&

		// master is still up and updated to 4.14; converge to end of zone migration also if master is already gone
		(ovn.MasterUpdateStatus == nil || ovn.MasterUpdateStatus.InterConnectEnabled && !ovn.MasterUpdateStatus.Progressing)
}

func isMigrationToMultiZoneAboutToStartOrOngoing(ovn bootstrap.OVNBootstrapResult, targetZoneMode *targetZoneModeType) bool {
	return isMigrationToMultiZoneAboutToStart(ovn, targetZoneMode, false) || isMigrationToMultiZoneOngoing(ovn, targetZoneMode)
}

// Multizone -> single-zone migration is a failure mode to permanently have a 4.14 single-zone cluster,
// in case something goes wrong with multizone.

// migration from multizone to single zone is about to start if target is single zone and the cluster
// is on multizone:
// - node is running, is >= 4.14, is in multizone and is not progressing
// - master is not running
// - control plane is running and is not progressing
func isMigrationToSingleZoneAboutToStart(ovn bootstrap.OVNBootstrapResult, targetZoneMode *targetZoneModeType) bool {
	return targetZoneMode.zoneMode == zoneModeSingleZone &&
		ovn.NodeUpdateStatus != nil &&
		ovn.MasterUpdateStatus == nil &&
		ovn.ControlPlaneUpdateStatus != nil &&
		ovn.NodeUpdateStatus.InterConnectEnabled &&
		ovn.NodeUpdateStatus.InterConnectZoneMode == string(zoneModeMultiZone) &&
		!ovn.NodeUpdateStatus.Progressing &&
		!ovn.ControlPlaneUpdateStatus.Progressing
}

// migration from multizone to single zone first rolls out master, then node. It is ongoing if:
// - target is single zone
// - node is running, is >=4.14
// - master is running, is >= 4.14, it rolls out when node is still in multizone;
// - control plane is not running when node is rolling out in single zone
func isMigrationToSingleZoneOngoing(ovn bootstrap.OVNBootstrapResult, targetZoneMode *targetZoneModeType) bool {
	return targetZoneMode.zoneMode == zoneModeSingleZone &&
		ovn.NodeUpdateStatus != nil && ovn.NodeUpdateStatus.InterConnectEnabled &&
		ovn.MasterUpdateStatus != nil &&
		ovn.MasterUpdateStatus.InterConnectEnabled &&

		// node is not progressing (and in multizone) && master is progressing && master is single zone
		(!ovn.NodeUpdateStatus.Progressing &&
			ovn.NodeUpdateStatus.InterConnectZoneMode == string(zoneModeMultiZone) &&
			ovn.MasterUpdateStatus.Progressing &&
			ovn.MasterUpdateStatus.InterConnectZoneMode == string(zoneModeSingleZone) ||

			// OR node is progressing and in single zone && master is not progressing && control plane was removed
			ovn.NodeUpdateStatus.Progressing &&
				ovn.NodeUpdateStatus.InterConnectZoneMode == string(zoneModeSingleZone) &&
				!ovn.MasterUpdateStatus.Progressing &&
				ovn.ControlPlaneUpdateStatus == nil)
}

func isMigrationToSingleZoneAboutToStartOrOngoing(ovn bootstrap.OVNBootstrapResult, targetZoneMode *targetZoneModeType) bool {
	return isMigrationToSingleZoneAboutToStart(ovn, targetZoneMode) || isMigrationToSingleZoneOngoing(ovn, targetZoneMode)
}

func isZoneModeMigrationAboutToStartOrOngoing(ovn bootstrap.OVNBootstrapResult, targetZoneMode *targetZoneModeType) bool {
	return isMigrationToMultiZoneAboutToStartOrOngoing(ovn, targetZoneMode) || isMigrationToSingleZoneAboutToStartOrOngoing(ovn, targetZoneMode)
}

func doesVersionEnableInterConnect(string) bool {
	return isVersionGreaterThanOrEqualTo(os.Getenv("RELEASE_VERSION"), 4, 14)
}

// prepareUpgradeToInterConnect sets everything in motion for an upgrade from non-interconnect ovnk (< 4.14) to
// interconnect ovn-k (>= 4.14). In all other cases, it's a no-op.
// If we're upgrading from a 4.13 cluster, which has no OVN InterConnect support, three phases are necessary.
// Phase 1:
//
//	a) prepareUpgradeToInterConnect pushes a configMap with zone-mode=singlezone, temporary=true;
//	b) renderOVNKubernetes selects the YAMLs from the single-zone folder
//	   (node DaemonSet, master DaemonSet [StatefulSet for hypershift], ovnkube-sbdb route [hypershift]);
//	c) shouldUpdateOVNKonUpgrade rolls out first node DaemonSet then master DaemonSet/StatefulSet in single-zone mode
//	   (there's no zone-mode change, since the 4.13 architecture is equivalent to single zone).
//
// Phase 2tmp:
//
//	a) Master and Node DaemonSet/StatefulSet are now single zone, so prepareUpgradeToInterConnect overrides the configMap
//	   with zone-mode=multizone, temporary=false;
//	b) renderOVNKubernetes selects the YAMLs from the multi-zone-tmp folder (node DaemonSet, master DaemonSet/StatefulSet,
//	   control plane deployment, ovnkube-sbdb route [hypershift]);
//	c) shouldUpdateOVNKonInterConnectZoneModeChange applies a zone mode change (single->multi) by rolling out
//	   first node DaemonSet and then master DaemonSet/StatefulSet+control plane deployment.
//
// Phase 2:
//
//	a) node DaemonSet is multizone, control plane is multizone, but we still have singlezone master DaemonSet/StatefulSet;
//	b) renderOVNKubernetes selects the YAMLs from the multi-zone folder (node DaemonSet, master DaemonSet), effectively
//	   removing the old ovnk master DaemonSet/StatefulSet and, in hypershift, the old ovnkube-sbdb route;
//	c) prepareUpgradeToInterConnect removes the configMap.
//
// At the end, we have a 4.14 cluster in multizone mode.
//
// TODO: 4.15 CNO won't need this extra complexity, since only multizone will be supported, so
// no need to keep this extra logic, once we've made the 4.14->4.15 upgrade mandatory.
func prepareUpgradeToInterConnect(ovn bootstrap.OVNBootstrapResult, client cnoclient.Client, targetZoneMode *targetZoneModeType) error {

	// [start of phase 1]
	// if node and master DaemonSets are <= 4.13 (no IC support) and we're upgrading to >= 4.14 (IC),
	// go through an intermediate step with IC single-zone DaemonSets. Track this temporary state
	// by pushing a configmap.
	if ovn.NodeUpdateStatus != nil && ovn.MasterUpdateStatus != nil && ovn.ControlPlaneUpdateStatus == nil &&
		!ovn.NodeUpdateStatus.InterConnectEnabled &&
		!ovn.MasterUpdateStatus.InterConnectEnabled &&
		doesVersionEnableInterConnect(os.Getenv("RELEASE_VERSION")) &&
		!ovn.NodeUpdateStatus.Progressing &&
		!ovn.MasterUpdateStatus.Progressing &&
		!targetZoneMode.configMapFound {

		klog.Infof("Upgrade to interconnect, phase1: creating IC configmap for single zone")

		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      util.OVN_INTERCONNECT_CONFIGMAP_NAME,
				Namespace: util.OVN_NAMESPACE,
			},
			Data: map[string]string{
				"zone-mode": fmt.Sprint(zoneModeSingleZone),
				"temporary": "true",
				// we lookup ongoing-upgrade in SetFromPods (pod_status.go) to distinguish a
				// configmap pushed during a non-IC-> IC upgrade from one that is added
				// outside of an upgrade by a cluster admin to change the zone mode.
				"ongoing-upgrade": "",
			},
		}
		if err := client.ClientFor("").CRClient().Create(context.TODO(), configMap); err != nil {
			return fmt.Errorf("could not create interconnect configmap: %w", err)
		}
		targetZoneMode.configMapFound = true
		targetZoneMode.zoneMode = zoneModeSingleZone
		targetZoneMode.temporary = true

	} else if isMigrationToMultiZoneAboutToStart(ovn, targetZoneMode, true) && targetZoneMode.temporary {
		// [start of phase 2]
		// if node and master DaemonSets have already upgraded to >= 4.14 single zone and
		// we previously pushed a configmap for temporary single zone,
		// patch the configmap and proceed with the migration to multizone.
		klog.Infof("Upgrade to interconnect, phase2: patching tmp configmap for multizone")

		patch := []map[string]interface{}{
			{
				"op":    "replace",
				"path":  "/data/zone-mode",
				"value": fmt.Sprint(zoneModeMultiZone),
			},
			{
				"op":    "replace",
				"path":  "/data/temporary",
				"value": "false",
			},
		}

		patchBytes, err := json.Marshal(patch)
		if err != nil {
			return fmt.Errorf("could not marshal patch for interconnect configmap: %w", err)
		}
		if _, err = client.ClientFor("").Kubernetes().CoreV1().ConfigMaps(util.OVN_NAMESPACE).Patch(
			context.TODO(), util.OVN_INTERCONNECT_CONFIGMAP_NAME,
			types.JSONPatchType, patchBytes, metav1.PatchOptions{}); err != nil {
			return fmt.Errorf("could not patch existing interconnect configmap: %w", err)
		}

		targetZoneMode.zoneMode = zoneModeMultiZone
		targetZoneMode.temporary = false

	} else if isMigrationToMultiZoneComplete(ovn, targetZoneMode) && !targetZoneMode.temporary && targetZoneMode.ongoingUpgrade {
		// [after completion of phase 2]
		// daemonsets have rolled out in multizone mode
		// Remove the configmap: this won't trigger any further roll out, but along with the annotation
		// added further below, we're signaling CNO status manager to update the operator version it reports.
		klog.Infof("Upgrade to interconnect, end of phase2: deleting IC configmap, upgrade is done")
		if err := client.Default().Kubernetes().CoreV1().ConfigMaps(util.OVN_NAMESPACE).Delete(
			context.TODO(), util.OVN_INTERCONNECT_CONFIGMAP_NAME, metav1.DeleteOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				klog.Warningf("Upgrade to interconnect, end of phase2: IC configmap not found")
			} else {
				return fmt.Errorf("upgrade to interconnect, end of phase2: could not delete interconnect configmap: %w", err)
			}
		}

		// HACK Once we're here, there are no more updates to the DaemonSets and CNO won't update
		// the version in its status unless we add a dummy annotation to a watched resource
		return annotateNodeDaemonset(client.ClientFor("").Kubernetes())
	}
	return nil

}

// hack to trigger pod status update so that cno status reports new version at the very end of the upgrade to interconnect
func annotateNodeDaemonset(kubeClient kubernetes.Interface) error {
	nodeDaemonSet, err := kubeClient.AppsV1().DaemonSets(util.OVN_NAMESPACE).Get(context.TODO(), util.OVN_NODE, metav1.GetOptions{})
	if err != nil {
		return err
	}

	nodeDaemonSet.Annotations["interconnect-upgrade"] = "done"

	if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		_, err = kubeClient.AppsV1().DaemonSets(util.OVN_NAMESPACE).Update(context.TODO(), nodeDaemonSet, metav1.UpdateOptions{})
		return err
	}); err != nil {
		return err
	}
	return nil
}
