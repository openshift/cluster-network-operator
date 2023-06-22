package network

import (
	"context"
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
	iputil "github.com/openshift/cluster-network-operator/pkg/util/ip"
	"github.com/openshift/cluster-network-operator/pkg/util/k8s"
	hyperv1 "github.com/openshift/hypershift/api/v1beta1"
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	types "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
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
const OVN_NODE_INTERCONNECT_ZONE = "k8s.ovn.org/zone-name"
const OVN_NODE_INTERCONNECT_ZONE_GLOBAL = "global"
const OVN_INTERCONNECT_CONFIGMAP_NAME = "ovn-interconnect-configuration"
const OVN_NAMESPACE = "openshift-ovn-kubernetes"

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
func renderOVNKubernetes(conf *operv1.NetworkSpec, bootstrapResult *bootstrap.BootstrapResult, manifestDir string, client cnoclient.Client) ([]*uns.Unstructured, bool, error) {
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
	if err != nil || targetZoneMode.zoneMode == zoneModeUndefined {
		return nil, progressing, errors.Wrap(
			err, "failed to render manifests, could not determine interconnect zone")
	}

	// render the manifests on disk
	data := render.MakeRenderData()
	data.Data["ReleaseVersion"] = os.Getenv("RELEASE_VERSION")
	data.Data["OvnImage"] = os.Getenv("OVN_IMAGE")
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
		data.Data["NorthdThreads"] = 4
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

	if bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.Enabled {
		manifestSubDir = "network/ovn-kubernetes/managed"
		manifestDirs = append(manifestDirs, filepath.Join(manifestDir, manifestSubDir))
	} else {
		manifestSubDir = "network/ovn-kubernetes/self-hosted/single-zone-interconnect"
		manifestDirs = append(manifestDirs, filepath.Join(manifestDir, manifestSubDir))
	}

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

	// obtain the current IP family mode.
	ipFamilyMode := names.IPFamilySingleStack
	if len(conf.ServiceNetwork) == 2 {
		ipFamilyMode = names.IPFamilyDualStack
	}
	// check if the IP family mode has changed and control the conversion process.
	updateNode, updateMaster := shouldUpdateOVNKonIPFamilyChange(bootstrapResult.OVN, ipFamilyMode)
	// annotate the daemonset and the daemonset template with the current IP family mode,
	// this triggers a daemonset restart if there are changes.
	err = setOVNObjectAnnotation(objs, names.NetworkIPFamilyModeAnnotation, ipFamilyMode)
	if err != nil {
		return nil, progressing, errors.Wrapf(err, "failed to set IP family %s annotation on daemonsets or statefulsets", ipFamilyMode)
	}

	// logic to pretty print the clusterNetwork CIDR (possibly only one) in its annotation
	var clusterNetworkCIDRs []string
	for _, c := range conf.ClusterNetwork {
		clusterNetworkCIDRs = append(clusterNetworkCIDRs, c.CIDR)
	}

	err = setOVNObjectAnnotation(objs, names.ClusterNetworkCIDRsAnnotation, strings.Join(clusterNetworkCIDRs, ","))
	if err != nil {
		return nil, progressing, errors.Wrapf(err, "failed to set %s annotation on daemonsets or statefulsets", clusterNetworkCIDRs)
	}

	// don't process interconnect zone mode change if we are handling a dual-stack conversion.
	if updateMaster && updateNode {
		updateNode, updateMaster = shouldUpdateOVNKonInterConnectZoneModeChange(bootstrapResult.OVN, targetZoneMode.zoneMode)
	}

	// don't process upgrades if we are handling an interconnect zone mode change
	if updateMaster && updateNode {
		updateNode, updateMaster = shouldUpdateOVNKonUpgrade(bootstrapResult.OVN, os.Getenv("RELEASE_VERSION"))
	}

	renderPrePull := false
	if updateNode {
		updateNode, renderPrePull = shouldUpdateOVNKonPrepull(bootstrapResult.OVN, os.Getenv("RELEASE_VERSION"))
	}

	// If we need to delay master or node daemonset rollout, then we'll tag that daemonset with "create-only"
	if !updateMaster {
		kind := bootstrapResult.OVN.MasterUpdateStatus.Kind
		namespace := bootstrapResult.OVN.MasterUpdateStatus.Namespace
		name := bootstrapResult.OVN.MasterUpdateStatus.Name
		k8s.UpdateObjByGroupKindName(objs, "apps", kind, namespace, name, func(o *uns.Unstructured) {
			anno := o.GetAnnotations()
			if anno == nil {
				anno = map[string]string{}
			}
			anno[names.CreateOnlyAnnotation] = "true"
			o.SetAnnotations(anno)
		})
	}
	if !updateNode {
		kind := bootstrapResult.OVN.NodeUpdateStatus.Kind
		namespace := bootstrapResult.OVN.NodeUpdateStatus.Namespace
		name := bootstrapResult.OVN.NodeUpdateStatus.Name
		k8s.UpdateObjByGroupKindName(objs, "apps", kind, namespace, name, func(o *uns.Unstructured) {
			anno := o.GetAnnotations()
			if anno == nil {
				anno = map[string]string{}
			}
			anno[names.CreateOnlyAnnotation] = "true"
			o.SetAnnotations(anno)
		})
	}

	if !renderPrePull {
		// remove prepull from the list of objects to render.
		objs = k8s.RemoveObjByGroupKindName(objs, "apps", "DaemonSet", OVN_NAMESPACE, "ovnkube-upgrades-prepuller")
	}

	if bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.Enabled && bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.OVNSbDbRouteHost == "" {
		k8s.UpdateObjByGroupKindName(objs, "apps", "DaemonSet", OVN_NAMESPACE, "ovnkube-node", func(o *uns.Unstructured) {
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
// TODO revisit this if necessary
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

// TODO move this to the top of the file
type InterConnectZoneMode string

const (
	zoneModeMultiZone  InterConnectZoneMode = "multizone"  // every node is assigned a different zone
	zoneModeSingleZone InterConnectZoneMode = "singlezone" // all nodes are assigned to one zone
	zoneModeUndefined  InterConnectZoneMode = "undefined"  // the cluster is neither multizone nor onezone
)

type targetZoneModeType struct {
	zoneMode       InterConnectZoneMode
	temporary      bool
	configMapFound bool
}

func getInterConnectConfigMap(kubeClient cnoclient.Client) (*corev1.ConfigMap, error) {
	configMap := &corev1.ConfigMap{}
	configMapLookup := types.NamespacedName{Name: OVN_INTERCONNECT_CONFIGMAP_NAME, Namespace: OVN_NAMESPACE}
	err := kubeClient.ClientFor("").CRClient().Get(context.TODO(), configMapLookup, configMap)
	return configMap, err
}

// getTargetInterConnectZoneMode determines the desired interconnect zone mode for the cluster.
// Available modes are two: multizone (default, one node per zone) and single zone (all nodes in the same zone).
// A configmap is looked up in order to switch to non-default single zone. In absence of this configmap, multizone is applied.
func getTargetInterConnectZoneMode(kubeClient cnoclient.Client) (targetZoneModeType, error) {
	targetZoneMode := targetZoneModeType{}

	interConnectConfigMap, err := getInterConnectConfigMap(kubeClient)
	if err != nil {
		if apierrors.IsNotFound(err) {
			klog.Infof("riccardo: No OVN InterConnect configMap found, applying default: multizone")
			targetZoneMode.zoneMode = zoneModeMultiZone
			return targetZoneMode, nil
		}
		return targetZoneMode, fmt.Errorf("riccardo: Unable to bootstrap OVN, unable to retrieve interconnect configMap: %v", err)
	}
	targetZoneMode.configMapFound = true
	if zoneModeFromConfigMap, ok := interConnectConfigMap.Data["zone-mode"]; ok {
		switch strings.ToLower(zoneModeFromConfigMap) {
		case string(zoneModeSingleZone):
			targetZoneMode.zoneMode = zoneModeSingleZone
		case string(zoneModeMultiZone):
			targetZoneMode.zoneMode = zoneModeMultiZone
		default:
			klog.Infof("[getTargetInterConnectZoneMode] riccardo: zoneModeFromConfigMap=%s, defaulting to multizone",
				zoneModeFromConfigMap)
			targetZoneMode.zoneMode = zoneModeMultiZone // default
		}
	} else {
		klog.Infof("[getTargetInterConnectZoneMode] riccardo: no valid value in configMap, defaulting to multizone")
		targetZoneMode.zoneMode = zoneModeMultiZone // default
	}

	if temporaryFromConfigMap, ok := interConnectConfigMap.Data["temporary"]; ok {
		targetZoneMode.temporary = strings.ToLower(temporaryFromConfigMap) == "true"
	}

	klog.Infof("[getTargetInterConnectZoneMode] riccardo zone from configmap: %+v", targetZoneMode)

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
	// any OVN cluster which is bootstrapped here, to the same initiator (should it still exists), hence we annotate the
	// network.operator.openshift.io CRD with this information and always try to re-use the same member for the OVN RAFT
	// cluster initialization
	// TODO this is only needed in single-zone mode
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
	// TODO in hypershift mode add the zone-mode in which master and node are
	// (when switching to one mode to another, one DS can be in one mode and the other DS in the other mode)
	var nsn types.NamespacedName
	masterStatus := &bootstrap.OVNUpdateStatus{}
	nodeStatus := &bootstrap.OVNUpdateStatus{}
	ipsecStatus := &bootstrap.OVNUpdateStatus{}
	prepullerStatus := &bootstrap.OVNUpdateStatus{}

	if hc.Enabled {
		masterSS := &appsv1.StatefulSet{
			TypeMeta: metav1.TypeMeta{
				Kind:       "StatefulSet",
				APIVersion: appsv1.SchemeGroupVersion.String(),
			},
		}
		nsn = types.NamespacedName{Namespace: hc.Namespace, Name: "ovnkube-master"}
		if err := kubeClient.ClientFor(names.ManagementClusterName).CRClient().Get(context.TODO(), nsn, masterSS); err != nil {
			if !apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("Failed to retrieve existing master DaemonSet: %w", err)
			} else {
				masterStatus = nil
			}
		} else {
			masterStatus.Kind = "StatefulSet"
			masterStatus.Namespace = masterSS.Namespace
			masterStatus.Name = masterSS.Name
			masterStatus.IPFamilyMode = masterSS.GetAnnotations()[names.NetworkIPFamilyModeAnnotation]
			masterStatus.Version = masterSS.GetAnnotations()["release.openshift.io/version"]
			masterStatus.Progressing = statefulSetProgressing(masterSS)
		}
	} else {
		masterDS := &appsv1.DaemonSet{
			TypeMeta: metav1.TypeMeta{
				Kind:       "DaemonSet",
				APIVersion: appsv1.SchemeGroupVersion.String(),
			},
		}
		// TODO1 decide whether we should have the name ovnkube-master for both zone modes... it'd be less prone to errors
		// The following commented code retrieves ovkube-control-plane DS first and, if it's missing, ovnkube-master
		// nsn = types.NamespacedName{Namespace: OVN_NAMESPACE, Name: "ovnkube-control-plane"} // for multizone IC
		// var errMaster error
		// masterZoneMode := zoneModeUndefined
		// if errMaster = kubeClient.ClientFor("").CRClient().Get(context.TODO(), nsn, masterDS); errMaster != nil {
		// 	if !apierrors.IsNotFound(errMaster) {
		// 		return nil, fmt.Errorf("Failed to retrieve existing ovnkube-control-plane DaemonSet: %w", errMaster)
		// 	} else {
		// 		// if there's no ovnkube-control-plane, see if we're in single-zone mode
		// 		nsnSingleZone := types.NamespacedName{Namespace: OVN_NAMESPACE, Name: "ovnkube-master"} // for single-zone IC
		// 		if errMaster = kubeClient.ClientFor("").CRClient().Get(context.TODO(), nsnSingleZone, masterDS); errMaster != nil {
		// 			if !apierrors.IsNotFound(errMaster) {
		// 				return nil, fmt.Errorf("Failed to retrieve existing single-zone master DaemonSet: %w", errMaster)
		// 			} else {
		// 				masterStatus = nil
		// 			}
		// 		} else {
		// 			masterZoneMode = zoneModeSingleZone

		// 		}
		// 	}
		// } else {
		// 	masterZoneMode = zoneModeMultiZone
		// }

		// if errMaster == nil {
		// 	masterStatus.Kind = "DaemonSet"
		// 	masterStatus.Namespace = masterDS.Namespace
		// 	masterStatus.Name = masterDS.Name
		// 	masterStatus.IPFamilyMode = masterDS.GetAnnotations()[names.NetworkIPFamilyModeAnnotation]
		// 	masterStatus.Version = masterDS.GetAnnotations()["release.openshift.io/version"] // current version for master DS
		// 	masterStatus.Progressing = daemonSetProgressing(masterDS, false)
		// 	masterStatus.InterConnectZoneMode = string(masterZoneMode)
		// }
		nsn = types.NamespacedName{Namespace: OVN_NAMESPACE, Name: "ovnkube-master"} // for multizone IC
		var errMaster error
		if errMaster = kubeClient.ClientFor("").CRClient().Get(context.TODO(), nsn, masterDS); errMaster != nil {
			if !apierrors.IsNotFound(errMaster) {
				return nil, fmt.Errorf("Failed to retrieve existing ovnkube-control-plane DaemonSet: %w", errMaster)
			} else {
				masterStatus = nil
				klog.Infof("riccardo: master DS not found")
			}
		} else {
			masterStatus.Kind = "DaemonSet"
			masterStatus.Namespace = masterDS.Namespace
			masterStatus.Name = masterDS.Name
			masterStatus.IPFamilyMode = masterDS.GetAnnotations()[names.NetworkIPFamilyModeAnnotation]
			masterStatus.Version = masterDS.GetAnnotations()["release.openshift.io/version"] // current version for master DS
			masterStatus.Progressing = daemonSetProgressing(masterDS, false)
			masterStatus.InterConnectZoneMode = string(getInterConnectZoneModeForMasterDaemonSet(masterDS))

			klog.Infof("riccardo: master DS zone-mode=%s, progressing=%t", masterStatus.InterConnectZoneMode, masterStatus.Progressing)
		}

	}

	nodeDS := &appsv1.DaemonSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "DaemonSet",
			APIVersion: appsv1.SchemeGroupVersion.String(),
		},
	}
	nsn = types.NamespacedName{Namespace: OVN_NAMESPACE, Name: "ovnkube-node"}
	if err := kubeClient.ClientFor("").CRClient().Get(context.TODO(), nsn, nodeDS); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("Failed to retrieve existing node DaemonSet: %w", err)
		} else {
			nodeStatus = nil
			klog.Infof("riccardo: node DS not found")
		}
	} else {
		nodeStatus.Kind = "DaemonSet"
		nodeStatus.Namespace = nodeDS.Namespace
		nodeStatus.Name = nodeDS.Name
		nodeStatus.IPFamilyMode = nodeDS.GetAnnotations()[names.NetworkIPFamilyModeAnnotation]
		nodeStatus.Version = nodeDS.GetAnnotations()["release.openshift.io/version"] // current version for node DS
		nodeStatus.Progressing = daemonSetProgressing(nodeDS, true)
		nodeStatus.InterConnectZoneMode = string(getInterConnectZoneModeForNodeDaemonSet(nodeDS))

		klog.Infof("riccardo: node DS zone-mode=%s, progressing=%t", nodeStatus.InterConnectZoneMode, nodeStatus.Progressing)

	}

	prePullerDS := &appsv1.DaemonSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "DaemonSet",
			APIVersion: appsv1.SchemeGroupVersion.String(),
		},
	}
	nsn = types.NamespacedName{Namespace: OVN_NAMESPACE, Name: "ovnkube-upgrades-prepuller"}
	if err := kubeClient.ClientFor("").CRClient().Get(context.TODO(), nsn, prePullerDS); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("Failed to retrieve existing prepuller DaemonSet: %w", err)
		} else {
			prepullerStatus = nil
		}
	} else {
		prepullerStatus.Namespace = prePullerDS.Namespace
		prepullerStatus.Name = prePullerDS.Name
		prepullerStatus.IPFamilyMode = prePullerDS.GetAnnotations()[names.NetworkIPFamilyModeAnnotation]
		prepullerStatus.Version = prePullerDS.GetAnnotations()["release.openshift.io/version"]
		prepullerStatus.Progressing = daemonSetProgressing(prePullerDS, true)
	}

	ipsecDS := &appsv1.DaemonSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "DaemonSet",
			APIVersion: appsv1.SchemeGroupVersion.String(),
		},
	}
	nsn = types.NamespacedName{Namespace: OVN_NAMESPACE, Name: "ovn-ipsec"}
	if err := kubeClient.ClientFor("").CRClient().Get(context.TODO(), nsn, ipsecDS); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("Failed to retrieve existing ipsec DaemonSet: %w", err)
		} else {
			ipsecStatus = nil
		}
	} else {
		ipsecStatus.Namespace = ipsecDS.Namespace
		ipsecStatus.Name = ipsecDS.Name
		ipsecStatus.IPFamilyMode = ipsecDS.GetAnnotations()[names.NetworkIPFamilyModeAnnotation]
		ipsecStatus.Version = ipsecDS.GetAnnotations()["release.openshift.io/version"]
	}

	res := bootstrap.OVNBootstrapResult{
		MasterAddresses:       ovnMasterAddresses,
		ClusterInitiator:      clusterInitiator,
		MasterUpdateStatus:    masterStatus,
		NodeUpdateStatus:      nodeStatus,
		IPsecUpdateStatus:     ipsecStatus,
		PrePullerUpdateStatus: prepullerStatus,
		OVNKubernetesConfig:   ovnConfigResult,
		FlowsConfig:           bootstrapFlowsConfig(kubeClient.ClientFor("").CRClient()),
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

// shouldUpdateOVNKonIPFamilyChange determines if we should roll out changes to
// the master and node daemonsets on IP family configuration changes.
// We rollout changes on masters first when there is a configuration change.
// Configuration changes take precedence over upgrades.
func shouldUpdateOVNKonIPFamilyChange(ovn bootstrap.OVNBootstrapResult, ipFamilyMode string) (updateNode, updateMaster bool) {
	// Fresh cluster - full steam ahead!
	if ovn.NodeUpdateStatus == nil || ovn.MasterUpdateStatus == nil {
		return true, true
	}
	// check current IP family mode

	nodeIPFamilyMode := ovn.NodeUpdateStatus.IPFamilyMode
	masterIPFamilyMode := ovn.MasterUpdateStatus.IPFamilyMode
	// if there are no annotations this is a fresh cluster
	if nodeIPFamilyMode == "" || masterIPFamilyMode == "" {
		return true, true
	}
	// exit if there are no IP family mode changes
	if nodeIPFamilyMode == ipFamilyMode && masterIPFamilyMode == ipFamilyMode {
		return true, true
	}
	// If the master config has changed update only the master, the node will be updated later
	if masterIPFamilyMode != ipFamilyMode {
		klog.V(2).Infof("IP family mode change detected to %s, updating OVN-Kubernetes master", ipFamilyMode)
		return false, true
	}
	// Don't rollout the changes on nodes until the master daemonset rollout has finished
	if ovn.MasterUpdateStatus.Progressing {
		klog.V(2).Infof("Waiting for OVN-Kubernetes master daemonset IP family mode rollout before updating node")
		return false, true
	}

	klog.V(2).Infof("OVN-Kubernetes master daemonset rollout complete, updating IP family mode on node daemonset")
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
// the master and node daemonsets on upgrades. We roll out nodes first,
// then masters. Downgrades, we do the opposite.
func shouldUpdateOVNKonUpgrade(ovn bootstrap.OVNBootstrapResult, releaseVersion string) (updateNode, updateMaster bool) {
	// Fresh cluster - full steam ahead!
	if ovn.NodeUpdateStatus == nil || ovn.MasterUpdateStatus == nil {
		return true, true
	}

	nodeVersion := ovn.NodeUpdateStatus.Version
	masterVersion := ovn.MasterUpdateStatus.Version

	// shortcut - we're all rolled out.
	// Return true so that we reconcile any changes that somehow could have happened.
	if nodeVersion == releaseVersion && masterVersion == releaseVersion {
		klog.V(2).Infof("OVN-Kubernetes master and node already at release version %s; no changes required", releaseVersion)
		return true, true
	}

	// compute version delta
	// versionUpgrade means the existing daemonSet needs an upgrade.
	masterDelta := compareVersions(masterVersion, releaseVersion)
	nodeDelta := compareVersions(nodeVersion, releaseVersion)

	if masterDelta == versionUnknown || nodeDelta == versionUnknown {
		klog.Warningf("could not determine ovn-kubernetes daemonset update directions; node: %s, master: %s, release: %s",
			nodeVersion, masterVersion, releaseVersion)
		return true, true
	}

	klog.V(2).Infof("OVN-Kubernetes master version %s -> latest %s; delta %s", masterVersion, releaseVersion, masterDelta)
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
		klog.V(2).Infof("Upgrading OVN-Kubernetes node before master")
		return true, false
	}

	// master older, node updated
	// update master if node is rolled out
	if masterDelta == versionUpgrade && nodeDelta == versionSame {
		if ovn.NodeUpdateStatus.Progressing {
			klog.V(2).Infof("Waiting for OVN-Kubernetes node update to roll out before updating master")
			return true, false
		}
		klog.V(2).Infof("OVN-Kubernetes node update rolled out; now updating master")
		return true, true
	}

	// both newer
	// downgrade master before node
	if masterDelta == versionDowngrade && nodeDelta == versionDowngrade {
		klog.V(2).Infof("Downgrading OVN-Kubernetes master before node")
		return false, true
	}

	// master same, node needs downgrade
	// wait for master rollout
	if masterDelta == versionSame && nodeDelta == versionDowngrade {
		if ovn.MasterUpdateStatus.Progressing {
			klog.V(2).Infof("Waiting for OVN-Kubernetes master downgrade to roll out before downgrading node")
			return false, true
		}
		klog.V(2).Infof("OVN-Kubernetes master update rolled out; now downgrading node")
		return true, true
	}

	// unlikely, should be caught above
	if masterDelta == versionSame && nodeDelta == versionSame {
		return true, true
	}

	klog.Warningf("OVN-Kubernetes daemonset versions inconsistent. node: %s, master: %s, release: %s",
		nodeVersion, masterVersion, releaseVersion)
	return true, true
}

// shouldUpdateOVNKonInterConnectZoneModeChange determines if we should roll out changes to
// the master and node daemonsets when the interconnect zone mode changes.
// When switching from multizone to single zone, we first roll out the new ovnk master DS
// and then the new ovnk node DS.  For single zone to multizone, we do the opposite.
// When switching from single zone to multizone, as in upgrades from 4.13 to 4.14,
// we first roll out the new ovnk node DSand then the new ovnk master DS.
// For single zone to multizone (for internal use only), we do the opposite.
// This allows us to always have a working deployed ovnk while changing zone mode.
// To sum up:
// - single zone -> multizone:   first roll out node,   then master
// - multizone   -> single zone: first roll out master, then node
func shouldUpdateOVNKonInterConnectZoneModeChange(ovn bootstrap.OVNBootstrapResult, targetZoneMode InterConnectZoneMode) (updateNode, updateMaster bool) {
	// Fresh cluster - full steam ahead!
	if ovn.NodeUpdateStatus == nil || ovn.MasterUpdateStatus == nil {
		return true, true
	}

	// if we're upgrading from a 4.13 cluster, which has no OVN InterConnect support, two phases are necessary.
	// Phase 1: a) CNO pushes a configMap with zone-mode=singlezone, temporary=true;
	//          b) shouldUpdateOVNKonUpgrade rolls out first node DS then master DS in single-zone mode
	//          (there's no zone-mode change, since the 4.13 architecture is equivalent to single zone);
	// Phase 2: a) Master and Node Daemonsets are now 4.14, so CNO removes the configMap;
	//          b) shouldUpdateOVNKonInterConnectZoneModeChange rolls out first node DS and then master DS,
	//             since without the configmap the desired zone mode is multizone and at the end of Phase 1
	//             both DS's are in single zone.
	// At the end, we have a 4.14 cluster in multizone mode.

	// When both DSs are in 4.13, we're in Phase 1 above, carried out by shouldUpdateOVNKonUpgrade. Nothing to do here.
	if isVersionLessThanOrEqualTo(ovn.NodeUpdateStatus.Version, 4, 13) || isVersionLessThanOrEqualTo(ovn.MasterUpdateStatus.Version, 4, 13) {
		return true, true
	}

	if targetZoneMode == zoneModeMultiZone {
		// no zone change: roll out both node and master DSs.
		if ovn.NodeUpdateStatus.InterConnectZoneMode == string(zoneModeMultiZone) &&
			ovn.MasterUpdateStatus.InterConnectZoneMode == string(zoneModeMultiZone) {
			klog.Infof("riccardo: [targetZoneMode=multizone] Master and Node are already in multizone")
			return true, true
		}

		// first step of single zone -> multizone. Roll out node DS first.
		if ovn.NodeUpdateStatus.InterConnectZoneMode == string(zoneModeSingleZone) &&
			ovn.MasterUpdateStatus.InterConnectZoneMode == string(zoneModeSingleZone) {
			klog.Infof("riccardo: [targetZoneMode=multizone] Master and Node are both in single zone: update node first")
			return true, false
		}

		// second (and final) step of single zone -> multizone. Rollout master DS.
		if ovn.NodeUpdateStatus.InterConnectZoneMode == string(zoneModeMultiZone) &&
			ovn.MasterUpdateStatus.InterConnectZoneMode == string(zoneModeSingleZone) {
			if ovn.NodeUpdateStatus.Progressing {
				klog.Infof("riccardo: [targetZoneMode=multizone] Wait for multizone node to roll out before rolling out multizone master")
				return true, false
			}
			klog.Infof("riccardo: [targetZoneMode=multizone] Node is already multizone, update master now")
			return true, true
		}

		// unexpected state of single zone -> multizone. Node is still in single zone,
		// master is already in multizone (the opposite should happen). Converge to multizone
		// for node as well, but emit warning.
		if ovn.NodeUpdateStatus.InterConnectZoneMode == string(zoneModeSingleZone) &&
			ovn.MasterUpdateStatus.InterConnectZoneMode == string(zoneModeMultiZone) {
			klog.Warningf("riccardo: [targetZoneMode=multizone] unexpected state: node is single zone, master is multizone. Update node too.")
			return true, true
		}

		klog.Warningf("riccardo: [targetZoneMode=multizone] undefined zone mode for master and node")
		return true, true

	} else if targetZoneMode == zoneModeSingleZone {
		// no zone change: roll out both node and master DSs.
		if ovn.NodeUpdateStatus.InterConnectZoneMode == string(zoneModeSingleZone) &&
			ovn.MasterUpdateStatus.InterConnectZoneMode == string(zoneModeSingleZone) {
			klog.Infof("riccardo: [targetZoneMode=singlezone] Master and Node are already in singlezone")
			return true, true
		}

		// first step of multizone -> single zone. Roll out master DS first.
		if ovn.NodeUpdateStatus.InterConnectZoneMode == string(zoneModeMultiZone) &&
			ovn.MasterUpdateStatus.InterConnectZoneMode == string(zoneModeMultiZone) {
			klog.Infof("riccardo: [targetZoneMode=singlezone] Master and Node are both in multizone: update master first")
			return false, true
		}

		// second (and final) step of multi zone -> single zone. Rollout node DS.
		if ovn.NodeUpdateStatus.InterConnectZoneMode == string(zoneModeMultiZone) &&
			ovn.MasterUpdateStatus.InterConnectZoneMode == string(zoneModeSingleZone) {
			if ovn.MasterUpdateStatus.Progressing {
				klog.V(2).Infof("riccardo: [targetZoneMode=singlezone] Wait for single-zone master to roll out before rolling out single-zone node")
				return false, true
			}
			klog.Infof("riccardo: [targetZoneMode=singlezone] Master is already single zone, roll out node now")
			return true, false
		}

		// unexpected state of multi zone -> single zone. Node is already in single zone,
		// master is still in multizone (the opposite should happen). Converge to single zone
		// for master as well, but emit warning.
		if ovn.NodeUpdateStatus.InterConnectZoneMode == string(zoneModeSingleZone) &&
			ovn.MasterUpdateStatus.InterConnectZoneMode == string(zoneModeMultiZone) {
			klog.Warningf("riccardo: [targetZoneMode=singlezone] unexpected state: node is single zone, master is multizone. Updating master too.")
			return true, true
		}

		klog.Warningf("riccardo: [targetZoneMode=singlezone] undefined zone mode for master and node")
		return true, true
	}
	klog.Warningf("riccardo: undefined target zone mode")
	return true, true
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
// If allowHung is true, then treat a statefulset hung at 90% as "done" for our purposes.
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

// setOVNObjectAnnotation annotates the OVNkube master and node daemonset
// it also annotated the template with the provided key and value to force the rollout
func setOVNObjectAnnotation(objs []*uns.Unstructured, key, value string) error {
	for _, obj := range objs {
		if obj.GetAPIVersion() == "apps/v1" &&
			(obj.GetKind() == "DaemonSet" || obj.GetKind() == "StatefulSet") &&
			(obj.GetName() == "ovnkube-master" || obj.GetName() == "ovnkube-node") {
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
func getInterConnectZoneModeForDaemonSet(ds *appsv1.DaemonSet, knownContainerForMultiZone string) InterConnectZoneMode {
	for _, container := range ds.Spec.Template.Spec.Containers {
		if container.Name == knownContainerForMultiZone {
			return zoneModeMultiZone
		}
	}
	return zoneModeSingleZone
}

func getInterConnectZoneModeForMasterDaemonSet(ds *appsv1.DaemonSet) InterConnectZoneMode {
	return getInterConnectZoneModeForDaemonSet(ds, "ovnkube-control-plane")
}

func getInterConnectZoneModeForNodeDaemonSet(ds *appsv1.DaemonSet) InterConnectZoneMode {
	return getInterConnectZoneModeForDaemonSet(ds, "nbdb")
}
