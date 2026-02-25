package network

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	yaml "github.com/ghodss/yaml"
	configv1 "github.com/openshift/api/config/v1"
	apifeatures "github.com/openshift/api/features"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	utilnet "k8s.io/utils/net"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/hypershift"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/render"
	"github.com/openshift/cluster-network-operator/pkg/util"
	iputil "github.com/openshift/cluster-network-operator/pkg/util/ip"
	"github.com/openshift/cluster-network-operator/pkg/util/k8s"
	mcutil "github.com/openshift/cluster-network-operator/pkg/util/machineconfig"
	"github.com/openshift/cluster-network-operator/pkg/version"
	"k8s.io/apimachinery/pkg/util/validation"
)

const CLUSTER_CONFIG_NAME = "cluster-config-v1"
const CLUSTER_CONFIG_NAMESPACE = "kube-system"
const OVN_CERT_CN = "ovn"
const OVN_LOCAL_GW_MODE = "local"
const OVN_SHARED_GW_MODE = "shared"
const OVN_LOG_PATTERN_CONSOLE = "%D{%Y-%m-%dT%H:%M:%S.###Z}|%05N|%c%T|%p|%m"
const OVN_NODE_MODE_FULL = "full"
const OVN_NODE_MODE_DPU_HOST = "dpu-host"
const OVN_NODE_MODE_DPU = "dpu"
const OVN_NODE_MODE_SMART_NIC = "smart-nic"
const OVN_NODE_SELECTOR_DEFAULT_DPU_HOST = "network.operator.openshift.io/dpu-host="
const OVN_NODE_SELECTOR_DEFAULT_DPU = "network.operator.openshift.io/dpu="
const OVN_NODE_SELECTOR_DEFAULT_SMART_NIC = "network.operator.openshift.io/smart-nic="
const OVN_NODE_IDENTITY_CERT_DURATION = "24h"

// gRPC healthcheck port. See: https://github.com/openshift/enhancements/pull/1209
const OVN_EGRESSIP_HEALTHCHECK_PORT = "9107"

const frrK8sNamespace = "openshift-frr-k8s"

const (
	OVSFlowsConfigMapName              = "ovs-flows-config"
	OVNKubernetesConfigOverridesCMName = "ovn-kubernetes-config-overrides"

	OVSFlowsConfigNamespace = names.APPLIED_NAMESPACE

	defaultV4MasqueradeSubnet = "169.254.0.0/17"
	defaultV6MasqueradeSubnet = "fd69::/112"
)

// renderOVNKubernetes returns the manifests for the ovn-kubernetes.
// This creates
// - the openshift-ovn-kubernetes namespace
// - the ovn-config ConfigMap
// - the ovnkube-node daemonset
// - the ovnkube-control-plane deployment
// and some other small things.
func renderOVNKubernetes(conf *operv1.NetworkSpec, bootstrapResult *bootstrap.BootstrapResult, manifestDir string,
	client cnoclient.Client, featureGates featuregates.FeatureGate) ([]*uns.Unstructured, bool, error) {
	var progressing bool

	// TODO: Fix operator behavior when running in a cluster with an externalized control plane.
	// For now, return an error since we don't have any master nodes to run the ovnkube-control-plane deployment.
	externalControlPlane := bootstrapResult.Infra.ControlPlaneTopology == configv1.ExternalTopologyMode
	if externalControlPlane && !bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.Enabled {
		return nil, progressing, fmt.Errorf("unable to render OVN in a cluster with an external control plane")
	}

	c := conf.DefaultNetwork.OVNKubernetesConfig

	objs := []*uns.Unstructured{}
	apiServer := bootstrapResult.Infra.APIServers[bootstrap.APIServerDefault]
	localAPIServer := bootstrapResult.Infra.APIServers[bootstrap.APIServerDefaultLocal]

	// render the manifests on disk
	data := render.MakeRenderData()
	data.Data["ReleaseVersion"] = os.Getenv("RELEASE_VERSION")
	data.Data["OvnImage"] = os.Getenv("OVN_IMAGE")
	data.Data["OvnControlPlaneImage"] = os.Getenv("OVN_IMAGE")
	if bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.Enabled {
		data.Data["OvnControlPlaneImage"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.ControlPlaneImage
	}
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
	// v4 and v6 join subnet are used when the user wants to use the addresses that we reserve for the join subnet in ovn-k
	// TODO: this field is being deprecated and will turn into c.GatewayConfig.IPv4/6.InternalJoinSubnet when we introduce the transit switch config into the api
	data.Data["V4JoinSubnet"] = ""
	data.Data["V6JoinSubnet"] = ""
	data.Data["V4TransitSwitchSubnet"] = ""
	data.Data["V6TransitSwitchSubnet"] = ""
	data.Data["V4MasqueradeSubnet"] = bootstrapResult.OVN.DefaultV4MasqueradeSubnet
	data.Data["V6MasqueradeSubnet"] = bootstrapResult.OVN.DefaultV6MasqueradeSubnet
	if c.GatewayConfig != nil && c.GatewayConfig.IPv4.InternalMasqueradeSubnet != "" {
		data.Data["V4MasqueradeSubnet"] = c.GatewayConfig.IPv4.InternalMasqueradeSubnet
	}
	if c.GatewayConfig != nil && c.GatewayConfig.IPv6.InternalMasqueradeSubnet != "" {
		data.Data["V6MasqueradeSubnet"] = c.GatewayConfig.IPv6.InternalMasqueradeSubnet
	}

	// Set DefaultMasqueradeNetworkCIDRs to bootstrapResult.OVN.DefaultV[4|6]MasqueradeSubnet
	// so the current default values are persisted through an annotation.
	var defaultMasqueradeNetworkCIDRs []string
	if bootstrapResult.OVN.DefaultV4MasqueradeSubnet != "" {
		defaultMasqueradeNetworkCIDRs = append(defaultMasqueradeNetworkCIDRs, bootstrapResult.OVN.DefaultV4MasqueradeSubnet)
	}
	if bootstrapResult.OVN.DefaultV6MasqueradeSubnet != "" {
		defaultMasqueradeNetworkCIDRs = append(defaultMasqueradeNetworkCIDRs, bootstrapResult.OVN.DefaultV6MasqueradeSubnet)
	}
	data.Data["DefaultMasqueradeNetworkCIDRs"] = strings.Join(defaultMasqueradeNetworkCIDRs, ",")

	data.Data["V4JoinSubnet"] = c.V4InternalSubnet
	data.Data["V6JoinSubnet"] = c.V6InternalSubnet
	if c.IPv4 != nil {
		if c.IPv4.InternalJoinSubnet != "" {
			data.Data["V4JoinSubnet"] = c.IPv4.InternalJoinSubnet
		}
		if c.IPv4.InternalTransitSwitchSubnet != "" {
			data.Data["V4TransitSwitchSubnet"] = c.IPv4.InternalTransitSwitchSubnet
		}
	}
	if c.IPv6 != nil {
		if c.IPv6.InternalJoinSubnet != "" {
			data.Data["V6JoinSubnet"] = c.IPv6.InternalJoinSubnet
		}
		if c.IPv6.InternalTransitSwitchSubnet != "" {
			data.Data["V6TransitSwitchSubnet"] = c.IPv6.InternalTransitSwitchSubnet
		}
	}

	data.Data["EnableUDPAggregation"] = !bootstrapResult.OVN.OVNKubernetesConfig.DisableUDPAggregation
	data.Data["NETWORK_NODE_IDENTITY_ENABLE"] = bootstrapResult.Infra.NetworkNodeIdentityEnabled
	data.Data["NodeIdentityCertDuration"] = OVN_NODE_IDENTITY_CERT_DURATION
	data.Data["AdvertisedUDNIsolationMode"] = bootstrapResult.OVN.OVNKubernetesConfig.ConfigOverrides["advertised-udn-isolation-mode"]

	if conf.Migration != nil {
		if conf.Migration.MTU != nil {
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
	}
	data.Data["GenevePort"] = c.GenevePort
	data.Data["CNIConfDir"] = pluginCNIConfDir(conf)
	data.Data["CNIBinDir"] = CNIBinDir
	data.Data["OVN_NODE_MODE"] = OVN_NODE_MODE_FULL
	data.Data["DpuHostModeLabel"] = bootstrapResult.OVN.OVNKubernetesConfig.DpuHostModeLabel
	data.Data["DpuHostModeValue"] = bootstrapResult.OVN.OVNKubernetesConfig.DpuHostModeValue
	data.Data["DpuModeLabel"] = bootstrapResult.OVN.OVNKubernetesConfig.DpuModeLabel
	data.Data["SmartNicModeLabel"] = bootstrapResult.OVN.OVNKubernetesConfig.SmartNicModeLabel
	data.Data["SmartNicModeValue"] = bootstrapResult.OVN.OVNKubernetesConfig.SmartNicModeValue
	data.Data["MgmtPortResourceName"] = bootstrapResult.OVN.OVNKubernetesConfig.MgmtPortResourceName
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
	// Tell northd to sleep a bit to save CPU
	data.Data["OVN_NORTHD_BACKOFF_MS"] = "300"

	// Hypershift
	data.Data["ManagementClusterName"] = names.ManagementClusterName
	data.Data["HostedClusterNamespace"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.Namespace
	data.Data["ReleaseImage"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.ReleaseImage
	data.Data["ClusterID"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.ClusterID
	data.Data["ClusterIDLabel"] = hypershift.ClusterIDLabel
	data.Data["HCPNodeSelector"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.HCPNodeSelector
	data.Data["HCPLabels"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.HCPLabels
	data.Data["HCPTolerations"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.HCPTolerations
	data.Data["CAConfigMap"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.CAConfigMap
	data.Data["CAConfigMapKey"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.CAConfigMapKey
	data.Data["RunAsUser"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.RunAsUser
	data.Data["PriorityClass"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.PriorityClass
	data.Data["TokenMinterResourceRequestCPU"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.TokenMinterResourceRequestCPU
	data.Data["TokenMinterResourceRequestMemory"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.TokenMinterResourceRequestMemory
	data.Data["OVNControlPlaneResourceRequestCPU"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.OVNControlPlaneResourceRequestCPU
	data.Data["OVNControlPlaneResourceRequestMemory"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.OVNControlPlaneResourceRequestMemory
	data.Data["Socks5ProxyResourceRequestCPU"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.Socks5ProxyResourceRequestCPU
	data.Data["Socks5ProxyResourceRequestMemory"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.Socks5ProxyResourceRequestMemory
	data.Data["OVN_NB_INACTIVITY_PROBE"] = nb_inactivity_probe
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

	IPsecMachineConfigEnable, OVNIPsecDaemonsetEnable, OVNIPsecEnable, renderIPsecHostDaemonSet, renderIPsecContainerizedDaemonSet,
		renderIPsecDaemonSetAsCreateWaitOnly := shouldRenderIPsec(c, bootstrapResult)
	data.Data["IPsecMachineConfigEnable"] = IPsecMachineConfigEnable
	data.Data["OVNIPsecDaemonsetEnable"] = OVNIPsecDaemonsetEnable
	data.Data["OVNIPsecEnable"] = OVNIPsecEnable
	data.Data["IPsecServiceCheckOnHost"] = renderIPsecHostDaemonSet && renderIPsecContainerizedDaemonSet
	data.Data["OVNIPsecEncap"] = operv1.EncapsulationAuto
	if OVNIPsecEnable && c.IPsecConfig.Full != nil {
		data.Data["OVNIPsecEncap"] = c.IPsecConfig.Full.Encapsulation
	}

	klog.V(5).Infof("IPsec: is MachineConfig enabled: %v, is East-West DaemonSet enabled: %v", data.Data["IPsecMachineConfigEnable"], data.Data["OVNIPsecDaemonsetEnable"])

	if c.GatewayConfig != nil && c.GatewayConfig.RoutingViaHost {
		data.Data["OVN_GATEWAY_MODE"] = OVN_LOCAL_GW_MODE
	} else {
		data.Data["OVN_GATEWAY_MODE"] = OVN_SHARED_GW_MODE
	}

	// We accept 3 valid inputs:
	// c.GatewayConfig.IPForwarding not set --> defaults to "".
	// c.GatewayConfig.IPForwarding set to "Restricted".
	// c.GatewayConfig.IPForwarding set to "Global".
	// For "" and "Restricted" (which behave the exact same) and any invalid value, the ConfigMap's .IP_FORWARDING_MODE
	// shall always be "".
	// For "Global", the ConfigMap's .IP_FORWARDING_MODE shall be "Global".
	data.Data["IP_FORWARDING_MODE"] = ""
	if c.GatewayConfig != nil && c.GatewayConfig.IPForwarding == operv1.IPForwardingGlobal {
		data.Data["IP_FORWARDING_MODE"] = c.GatewayConfig.IPForwarding
	}

	// No-overlay mode configuration
	// The NoOverlayMode feature gate enables no-overlay networking for both the default network
	// and CUDNs (Cluster User-Defined Networks). BGP managed configuration is cluster-wide and
	// applies to any network using no-overlay mode with managed routing.
	data.Data["DefaultNetworkTransport"] = ""
	data.Data["NoOverlayEnabled"] = false
	data.Data["NoOverlayOutboundSNAT"] = ""
	data.Data["NoOverlayRouting"] = ""
	data.Data["NoOverlayManagedEnabled"] = false
	data.Data["NoOverlayManagedASNumber"] = ""
	data.Data["NoOverlayManagedTopology"] = ""
	data.Data["FRRK8sNamespace"] = frrK8sNamespace

	noOverlayFeatureEnabled := isFeatureGateEnabled(featureGates, apifeatures.FeatureGateNoOverlayMode)

	if noOverlayFeatureEnabled && c.Transport == operv1.TransportOptionNoOverlay {
		data.Data["DefaultNetworkTransport"] = "no-overlay"
		data.Data["NoOverlayEnabled"] = true

		// No-overlay specific options for the default network
		if c.NoOverlayConfig.OutboundSNAT != "" {
			// Convert API value (e.g., "Enabled") to lowercase for ovn-kubernetes config ("enable", "disabled")
			data.Data["NoOverlayOutboundSNAT"] = strings.ToLower(string(c.NoOverlayConfig.OutboundSNAT))
		}
		if c.NoOverlayConfig.Routing != "" {
			// Convert API value (e.g., "Managed") to lowercase for ovn-kubernetes config ("managed", "unmanaged")
			data.Data["NoOverlayRouting"] = strings.ToLower(string(c.NoOverlayConfig.Routing))
		}
	}

	// BGP managed configuration is cluster-wide and applies to any network (default or CUDN)
	// using no-overlay mode with managed routing.
	// BGPTopology is required when BGPManagedConfig is specified.
	if noOverlayFeatureEnabled && c.BGPManagedConfig.BGPTopology != "" {
		data.Data["NoOverlayManagedEnabled"] = true
		klog.V(2).Infof("BGP managed configuration enabled for no-overlay mode")

		// ASNumber is optional, will have a default if not set
		if c.BGPManagedConfig.ASNumber > 0 {
			data.Data["NoOverlayManagedASNumber"] = c.BGPManagedConfig.ASNumber
		}

		var topology string
		switch c.BGPManagedConfig.BGPTopology {
		case operv1.BGPTopologyFullMesh:
			topology = "full-mesh"
		default:
			return nil, progressing, fmt.Errorf("unsupported BGP topology: %s", c.BGPManagedConfig.BGPTopology)
		}
		data.Data["NoOverlayManagedTopology"] = topology
	}

	// leverage feature gates
	data.Data["DNS_NAME_RESOLVER_ENABLE"] = featureGates.Enabled(apifeatures.FeatureGateDNSNameResolver)
	data.Data["OVN_OBSERVABILITY_ENABLE"] = featureGates.Enabled(apifeatures.FeatureGateOVNObservability)
	data.Data["OVN_ROUTE_ADVERTISEMENTS_ENABLE"] = c.RouteAdvertisements == operv1.RouteAdvertisementsEnabled
	// OVN_EVPN_ENABLE_API depends only on the feature gate and controls whether EVPN fields
	// are present in the CUDN CRD schema. It must remain stable regardless of runtime config
	// to avoid removing API fields and potentially losing data from existing custom resources.
	// OVN_EVPN_ENABLE depends on both the feature gate and route advertisements being enabled,
	// and controls deployment of EVPN runtime components (VTEP CRD, RBAC, FRR containers).
	data.Data["OVN_EVPN_ENABLE_API"] = featureGates.Enabled(apifeatures.FeatureGateEVPN)
	data.Data["OVN_EVPN_ENABLE"] = featureGates.Enabled(apifeatures.FeatureGateEVPN) && c.RouteAdvertisements == operv1.RouteAdvertisementsEnabled

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

	data.Data["NorthdThreads"] = 1
	data.Data["IsSNO"] = bootstrapResult.OVN.ControlPlaneReplicaCount == 1

	data.Data["OVN_MULTI_NETWORK_ENABLE"] = true
	data.Data["OVN_MULTI_NETWORK_POLICY_ENABLE"] = false
	if conf.DisableMultiNetwork != nil && *conf.DisableMultiNetwork {
		data.Data["OVN_MULTI_NETWORK_ENABLE"] = false
	} else if conf.UseMultiNetworkPolicy != nil && *conf.UseMultiNetworkPolicy {
		// Multi-network policy support requires multi-network support to be
		// enabled
		data.Data["OVN_MULTI_NETWORK_POLICY_ENABLE"] = true
	}

	//there only needs to be two cluster managers
	clusterManagerReplicas := 2
	if bootstrapResult.OVN.ControlPlaneReplicaCount < 2 {
		clusterManagerReplicas = bootstrapResult.OVN.ControlPlaneReplicaCount
	}
	data.Data["ClusterManagerReplicas"] = clusterManagerReplicas

	commonManifestDir := filepath.Join(manifestDir, "network/ovn-kubernetes/common")

	cmPaths := []string{
		filepath.Join(commonManifestDir, "008-script-lib.yaml"),
	}

	// Many ovnkube config options are stored in ConfigMaps; the ovnkube
	// daemonsets need to know when those ConfigMaps change so they can
	// restart with the new options. Render those ConfigMaps first and
	// embed a hash of their data into the ovnkube-node daemonsets.
	h := sha1.New()
	for _, path := range cmPaths {
		manifests, err := render.RenderTemplate(path, &data)
		if err != nil {
			return nil, progressing, errors.Wrapf(err, "failed to render ConfigMap template %q", path)
		}

		// Hash each rendered ConfigMap object's data
		for _, m := range manifests {
			bytes, err := json.Marshal(m)
			if err != nil {
				return nil, progressing, errors.Wrapf(err, "failed to marshal ConfigMap %q manifest", path)
			}
			if _, err := h.Write(bytes); err != nil {
				return nil, progressing, errors.Wrapf(err, "failed to hash ConfigMap %q data", path)
			}
		}
	}
	data.Data["OVNKubeConfigHash"] = hex.EncodeToString(h.Sum(nil))

	manifestDirs := make([]string, 0, 2)
	manifestDirs = append(manifestDirs, commonManifestDir)

	productFlavor := "self-hosted"
	if bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.Enabled {
		productFlavor = "managed"
	}
	manifestSubDir := filepath.Join(manifestDir, "network/ovn-kubernetes", productFlavor)
	manifestDirs = append(manifestDirs, manifestSubDir)

	manifests, err := render.RenderDirs(manifestDirs, &data)
	if err != nil {
		return nil, progressing, errors.Wrap(err, "failed to render manifests")
	}
	objs = append(objs, manifests...)

	err = setOVNObjectAnnotation(objs, names.NetworkHybridOverlayAnnotation, hybridOverlayStatus)
	if err != nil {
		return nil, progressing, errors.Wrapf(err, "failed to set the status of hybrid overlay %s annotation on ovnkube daemonset and deployment", hybridOverlayStatus)
	}

	if len(bootstrapResult.OVN.OVNKubernetesConfig.SmartNicModeNodes) > 0 {
		data.Data["OVN_NODE_MODE"] = OVN_NODE_MODE_SMART_NIC
		manifests, err = render.RenderTemplate(filepath.Join(manifestSubDir, "ovnkube-node.yaml"), &data)
		if err != nil {
			return nil, progressing, errors.Wrap(err, "failed to render manifests for smart-nic")
		}
		objs = append(objs, manifests...)
	}

	if len(bootstrapResult.OVN.OVNKubernetesConfig.DpuHostModeNodes) > 0 {
		data.Data["OVN_NODE_MODE"] = OVN_NODE_MODE_DPU_HOST
		manifests, err = render.RenderTemplate(filepath.Join(manifestSubDir, "ovnkube-node.yaml"), &data)
		if err != nil {
			return nil, progressing, errors.Wrap(err, "failed to render manifests for dpu-host")
		}
		objs = append(objs, manifests...)
	}

	if len(bootstrapResult.OVN.OVNKubernetesConfig.DpuModeNodes) > 0 {
		// "OVN_NODE_MODE" not set when render.RenderDir() called above,
		// so render just the error-cni.yaml with "OVN_NODE_MODE" set.
		data.Data["OVN_NODE_MODE"] = OVN_NODE_MODE_DPU
		manifests, err = render.RenderTemplate(filepath.Join(commonManifestDir, "error-cni.yaml"), &data)
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
	updateNode, updateControlPlane, err := handleIPFamilyAnnotationAndIPFamilyChange(conf, bootstrapResult.OVN, &objs)
	if err != nil {
		return nil, progressing, fmt.Errorf("unable to render OVN: failed to handle IP family annotation or change: %w", err)
	}

	// process upgrades only if we aren't already handling an IP family migration
	if updateNode && updateControlPlane {
		updateNode, updateControlPlane = shouldUpdateOVNKonUpgrade(bootstrapResult.OVN, os.Getenv("RELEASE_VERSION"))
	}

	renderPrePull := false
	if updateNode {
		updateNode, renderPrePull = shouldUpdateOVNKonPrepull(bootstrapResult.OVN, os.Getenv("RELEASE_VERSION"))
	}

	// Skip rendering ovn-ipsec-host daemonset when renderIPsecHostDaemonSet flag is not set.
	if !renderIPsecHostDaemonSet {
		objs = k8s.RemoveObjByGroupKindName(objs, "apps", "DaemonSet", util.OVN_NAMESPACE, "ovn-ipsec-host")
	}

	// Skip rendering ovn-ipsec-containerized daemonset when renderIPsecContainerizedDaemonSet flag is not set.
	if !renderIPsecContainerizedDaemonSet {
		objs = k8s.RemoveObjByGroupKindName(objs, "apps", "DaemonSet", util.OVN_NAMESPACE, "ovn-ipsec-containerized")
	}

	// When disabling IPsec deployment, avoid any updates until IPsec is completely
	// disabled from OVN.
	if renderIPsecDaemonSetAsCreateWaitOnly {
		k8s.UpdateObjByGroupKindName(objs, "apps", "DaemonSet", util.OVN_NAMESPACE, "ovn-ipsec-host", func(o *uns.Unstructured) {
			anno := o.GetAnnotations()
			if anno == nil {
				anno = map[string]string{}
			}
			anno[names.CreateWaitAnnotation] = "true"
			o.SetAnnotations(anno)
		})

		k8s.UpdateObjByGroupKindName(objs, "apps", "DaemonSet", util.OVN_NAMESPACE, "ovn-ipsec-containerized", func(o *uns.Unstructured) {
			anno := o.GetAnnotations()
			if anno == nil {
				anno = map[string]string{}
			}
			anno[names.CreateWaitAnnotation] = "true"
			o.SetAnnotations(anno)
		})
	}

	klog.Infof("ovnk components: ovnkube-node: isRunning=%t, update=%t; ovnkube-control-plane: isRunning=%t, update=%t",
		bootstrapResult.OVN.NodeUpdateStatus != nil, updateNode,
		bootstrapResult.OVN.ControlPlaneUpdateStatus != nil, updateControlPlane)

	// During an upgrade we need to delay the rollout of control plane, so we'll tag the corresponding
	// deployment object with "create-only"
	if !updateControlPlane { // no-op if object is not found
		annotationKey := names.CreateOnlyAnnotation // skip only if object doesn't exist already
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

	return objs, progressing, nil
}

// GetIPsecMode return the ipsec mode accounting for upgrade scenarios
// Find the IPsec mode from Ipsec.config
// Ipsec.config == nil (bw compatibility) || ipsecConfig == Off ==> ipsec is disabled
// ipsecConfig.mode == "" (bw compatibility) || ipsec.Config == Full ==> ipsec is enabled for NS and EW
// ipsecConfig.mode == External ==> ipsec is enabled for NS only
func GetIPsecMode(conf *operv1.OVNKubernetesConfig) operv1.IPsecMode {
	mode := operv1.IPsecModeDisabled // Should stay so if conf.IPsecConfig == nil
	if conf.IPsecConfig != nil {
		if conf.IPsecConfig.Mode != "" {
			mode = conf.IPsecConfig.Mode
		} else {
			mode = operv1.IPsecModeFull // Backward compatibility with existing configs
		}
	}

	klog.V(5).Infof("IPsec: after looking at %+v, ipsec mode=%s", conf.IPsecConfig, mode)
	return mode
}

// IsIPsecLegacyAPI returns true if the old (pre 4.15) IPsec API is used, and false otherwise.
func IsIPsecLegacyAPI(conf *operv1.OVNKubernetesConfig) bool {
	return conf.IPsecConfig == nil || conf.IPsecConfig.Mode == ""
}

// shouldRenderIPsec method ensures the needed states when enabling, disabling
// or upgrading IPsec
func shouldRenderIPsec(conf *operv1.OVNKubernetesConfig, bootstrapResult *bootstrap.BootstrapResult) (renderCNOIPsecMachineConfig, renderIPsecDaemonSet,
	renderIPsecOVN, renderIPsecHostDaemonSet, renderIPsecContainerizedDaemonSet, renderIPsecDaemonSetAsCreateWaitOnly bool) {

	// Note on IPsec install (or) upgrade for self managed clusters:
	// During this process both host and containerized daemonsets are rendered.
	// Internally, these damonsets coordinate when they are active or dormant:
	// before the IPsec MachineConfig extensions are active, the containerized
	// daemonset is active and the host daemonset is dormant; after rebooting
	// with the the IPsec MachineConfig extensions active, the containerized
	// daemonset is dormant and the host daemonset is active. When the upgrade
	// finishes, the containerized daemonset is then not rendered.
	//
	// The upgrade from 4.14 is handled very carefully to correctly migrate
	// from containerized ipsec deployment to the host ipsec deployment.
	//  1. OCP 4.14 with container ipsec deployment is active using libreswan
	//     4.6.3; and host ipsec deployment is dormant.
	//  2. Start the 4.15 upgrade.
	//  3. CNO upgrades to 4.15.
	//  4. CNO renders 4.15 versions of the container ipsec deployment and
	//     host ipsec deployment with no state change. However the host ipsec
	//     deployment mounts to top system level directories for the host ipsec
	//     path for this upgrade scenario. It fixes two problems.
	//     a) version mismatch between libreswan installed on the host and
	//        host ipsec deployment pod container.
	//     b) host ipsec deployment pod goes into pending state if we mount the
	//        binaries directly and libreswan has not been installed yet
	//        on the host by IPsec machine configs.
	//  5. CNO waits until MCO is upgraded to 4.15 and then deploys CNO ipsec
	//     machine configs that will install and run libreswan 4.6.3 on the
	//     host. Otherwise, without waiting for MCO 4.15, libreswan 4.9 may
	//     be installed from 4.14 MCO which has all known stability problems
	//     found from the bugs.
	//     https://issues.redhat.com/browse/OCPBUGS-41823
	//     https://issues.redhat.com/browse/OCPBUGS-42952
	//  6. Host ipsec deployment becomes active using libreswan 4.6.3 from the
	//     container which can successfully run against libreswan 4.6.3 running
	//     on the host.
	//  7. At the same time as step 6, containerized ipsec deployment becomes
	//     dormant, and eventually gets removed when the upgrade is done.

	isHypershiftHostedCluster := bootstrapResult.Infra.HostedControlPlane != nil
	isOVNIPsecActiveOrRollingOut := bootstrapResult.OVN.IPsecUpdateStatus != nil && bootstrapResult.OVN.IPsecUpdateStatus.IsOVNIPsecActiveOrRollingOut
	isCNOIPsecMachineConfigPresent := isCNOIPsecMachineConfigPresent(bootstrapResult.Infra)
	isUserDefinedIPsecMachineConfigPresent := isUserDefinedIPsecMachineConfigPresent(bootstrapResult.Infra)
	isIPsecMachineConfigActive := isIPsecMachineConfigActive(bootstrapResult.Infra)
	isMachineConfigClusterOperatorReady := bootstrapResult.Infra.MachineConfigClusterOperatorReady

	mode := GetIPsecMode(conf)

	// When OVN is rolling out, OVN IPsec might be fully or partially active or inactive.
	// If MachineConfigs are not present, we know its inactive since we only stop rendering them once inactive.
	isOVNIPsecActive := isOVNIPsecActiveOrRollingOut && (isCNOIPsecMachineConfigPresent || isUserDefinedIPsecMachineConfigPresent || isHypershiftHostedCluster)

	// We render the ipsec deployment if IPsec is already active in OVN
	// or if EW IPsec config is enabled.
	renderIPsecDaemonSet = isOVNIPsecActive || mode == operv1.IPsecModeFull

	// To enable IPsec, specific MachineConfig extensions need to be rolled out
	// first with the following exceptions:
	// - not needed for the containerized deployment is used in hypershift
	// hosted clusters
	// - not needed if the user already created their own
	renderCNOIPsecMachineConfig = (mode != operv1.IPsecModeDisabled || renderIPsecDaemonSet) && !isHypershiftHostedCluster &&
		!isUserDefinedIPsecMachineConfigPresent
	// Wait for MCO to be ready unless we had already rendered the IPsec MachineConfig.
	renderCNOIPsecMachineConfig = renderCNOIPsecMachineConfig && (isCNOIPsecMachineConfigPresent || isMachineConfigClusterOperatorReady)

	// We render the host ipsec deployment except for hypershift hosted clusters.
	// Until IPsec machine configs are rolled out completely, then its daemonset
	// pod(s) may be active or dormant based on machine config rollout progress
	// state on the node, Once it is completely rolled out, then daemonset pods
	// become active on all nodes.
	renderIPsecHostDaemonSet = renderIPsecDaemonSet && !isHypershiftHostedCluster

	// We render the containerized ipsec deployment for hypershift hosted clusters.
	// It's also rendered until IPsec machine configs are active on all nodes in the
	// cluster. This daemonset pod is active until IPsec machine config is rolled out
	// on the node, Once Machine Config rollout is complete, we stop rendering
	// containerized ipsec deployment.
	renderIPsecContainerizedDaemonSet = (renderIPsecDaemonSet && isHypershiftHostedCluster) || !isIPsecMachineConfigActive

	// We render OVN IPsec if EW IPsec is enabled and before the daemon sets are
	// rendered. If it is already rendered, keep it rendered unless disabled.
	renderIPsecOVN = (renderIPsecHostDaemonSet || renderIPsecContainerizedDaemonSet || isOVNIPsecActive) && mode == operv1.IPsecModeFull

	// Keep IPsec daemonsets updated (but avoid creating) in the following circumstance:
	// - when disabling OVN IPsec, we want to keep the daemonsets until after
	// OVN IPsec is disabled.
	renderIPsecDaemonSetAsCreateWaitOnly = isOVNIPsecActive && !renderIPsecOVN

	return
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

func bootstrapOVNHyperShiftConfig(hc *hypershift.HyperShiftConfig, kubeClient cnoclient.Client, infraStatus *bootstrap.InfraStatus) (*bootstrap.OVNHyperShiftBootstrapResult, error) {
	ovnHypershiftResult := &bootstrap.OVNHyperShiftBootstrapResult{
		Enabled:           hc.Enabled,
		Namespace:         hc.Namespace,
		RunAsUser:         hc.RunAsUser,
		ReleaseImage:      hc.ReleaseImage,
		ControlPlaneImage: hc.ControlPlaneImage,
		CAConfigMap:       hc.CAConfigMap,
		CAConfigMapKey:    hc.CAConfigMapKey,
	}

	if !hc.Enabled {
		return ovnHypershiftResult, nil
	}

	hcp := infraStatus.HostedControlPlane

	ovnHypershiftResult.ClusterID = hcp.ClusterID
	ovnHypershiftResult.HCPNodeSelector = hcp.NodeSelector
	ovnHypershiftResult.HCPLabels = hcp.Labels
	ovnHypershiftResult.HCPTolerations = hcp.Tolerations
	ovnHypershiftResult.PriorityClass = hcp.PriorityClass

	switch hcp.ControllerAvailabilityPolicy {
	case hypershift.HighlyAvailable:
		ovnHypershiftResult.ControlPlaneReplicas = 3
	default:
		ovnHypershiftResult.ControlPlaneReplicas = 1
	}

	// Preserve any customizations to the resource requests on the three containers in the ovn-control-plane pod
	controlPlaneClient := kubeClient.ClientFor(names.ManagementClusterName)
	tokenMinterCPURequest, tokenMinterMemoryRequest := getResourceRequestsForDeployment(controlPlaneClient.CRClient(), hc.Namespace, util.OVN_CONTROL_PLANE, "token-minter")
	if tokenMinterCPURequest > 0 {
		ovnHypershiftResult.TokenMinterResourceRequestCPU = strconv.FormatInt(tokenMinterCPURequest, 10)
	}
	if tokenMinterMemoryRequest > 0 {
		ovnHypershiftResult.TokenMinterResourceRequestMemory = strconv.FormatInt(tokenMinterMemoryRequest, 10)
	}

	ovnControlPlaneCPURequest, ovnControlPlaneMemoryRequest := getResourceRequestsForDeployment(controlPlaneClient.CRClient(), hc.Namespace, util.OVN_CONTROL_PLANE, "ovnkube-control-plane")
	if ovnControlPlaneCPURequest > 0 {
		ovnHypershiftResult.OVNControlPlaneResourceRequestCPU = strconv.FormatInt(ovnControlPlaneCPURequest, 10)
	}
	if ovnControlPlaneMemoryRequest > 0 {
		ovnHypershiftResult.OVNControlPlaneResourceRequestMemory = strconv.FormatInt(ovnControlPlaneMemoryRequest, 10)
	}

	socksProxyCPURequest, socksProxyMemoryRequest := getResourceRequestsForDeployment(controlPlaneClient.CRClient(), hc.Namespace, util.OVN_CONTROL_PLANE, "socks-proxy")
	if socksProxyCPURequest > 0 {
		ovnHypershiftResult.Socks5ProxyResourceRequestCPU = strconv.FormatInt(socksProxyCPURequest, 10)
	}
	if socksProxyMemoryRequest > 0 {
		ovnHypershiftResult.Socks5ProxyResourceRequestMemory = strconv.FormatInt(socksProxyMemoryRequest, 10)
	}

	return ovnHypershiftResult, nil
}

// getResourceRequestsForDeployment gets the cpu and memory resource requests for the specified deployment
// If the deployment or container is not found, or if the container doesn't have a cpu or memory resource request, then 0 is returned
func getResourceRequestsForDeployment(cl crclient.Reader, namespace string, deploymentName string, containerName string) (cpu int64, memory int64) {
	deployment := &appsv1.Deployment{}
	if err := cl.Get(context.TODO(), types.NamespacedName{
		Namespace: namespace,
		Name:      deploymentName,
	}, deployment); err != nil {
		if !apierrors.IsNotFound(err) {
			klog.Warningf("Error fetching %s deployment: %v", deploymentName, err)
		}
		return cpu, memory
	}

	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == containerName {
			if container.Resources.Requests != nil {
				if !container.Resources.Requests.Cpu().IsZero() {
					cpu = container.Resources.Requests.Cpu().MilliValue()
				}
				if !container.Resources.Requests.Memory().IsZero() {
					memory = container.Resources.Requests.Memory().Value() / bytesInMiB
				}
			}
			break
		}
	}

	return cpu, memory
}

func getDisableUDPAggregation(cl crclient.Reader) bool {
	disable := false

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
	switch disableUDPAggregation {
	case "true":
		disable = true
	case "false":
		disable = false
	default:
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

// validateLabel checks if a label string is valid according to Kubernetes label requirements.
// It validates both the format (key=value or key=) and the key/value according to Kubernetes rules.
// Returns true if valid, false if invalid.
func validateLabel(label string) bool {
	if label == "" {
		return false
	}

	// Parse labelKey to extract key and value - labels must contain "=" but can have an empty value
	parts := strings.SplitN(label, "=", 2)
	if len(parts) != 2 {
		return false
	}

	key := parts[0]
	value := parts[1]

	// Validate the key using Kubernetes validation
	if errs := validation.IsQualifiedName(key); len(errs) > 0 {
		return false
	}

	if value != "" {
		if errs := validation.IsValidLabelValue(value); len(errs) > 0 {
			return false
		}
	}

	return true
}

// getKeyValueFromLabel returns specified label key and value (if any).
// label format: "key=" (match any value, use operator: Exists) or "key=value" (match specific value, use operator: In)
func getKeyValueFromLabel(label string) (string, string, error) {
	// Parse labelKey to extract key and value - labels must contain "=" but can have an empty value
	parts := strings.SplitN(label, "=", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid label format %s, expected format: key= or key=value", label)
	}

	key := parts[0]
	value := parts[1]

	return key, value, nil
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
func bootstrapOVNConfig(conf *operv1.Network, kubeClient cnoclient.Client, hc *hypershift.HyperShiftConfig, infraStatus *bootstrap.InfraStatus) (*bootstrap.OVNConfigBoostrapResult, error) {
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
			return nil, fmt.Errorf("could not determine Node Mode: %w", err)
		}
	} else {
		dpuHostModeLabel, exists := cm.Data["dpu-host-mode-label"]
		if exists && validateLabel(dpuHostModeLabel) {
			ovnConfigResult.DpuHostModeLabel = dpuHostModeLabel
		} else if exists {
			klog.Warningf("Invalid dpu-host-mode-label format %q, using default %q", dpuHostModeLabel, OVN_NODE_SELECTOR_DEFAULT_DPU_HOST)
		}

		dpuModeLabel, exists := cm.Data["dpu-mode-label"]
		if exists && validateLabel(dpuModeLabel) {
			ovnConfigResult.DpuModeLabel = dpuModeLabel
		} else if exists {
			klog.Warningf("Invalid dpu-mode-label format %q, using default %q", dpuModeLabel, OVN_NODE_SELECTOR_DEFAULT_DPU)
		}

		smartNicModeLabel, exists := cm.Data["smart-nic-mode-label"]
		if exists && validateLabel(smartNicModeLabel) {
			ovnConfigResult.SmartNicModeLabel = smartNicModeLabel
		} else if exists {
			klog.Warningf("Invalid smart-nic-mode-label format %q, using default %q", smartNicModeLabel, OVN_NODE_SELECTOR_DEFAULT_SMART_NIC)
		}

		mgmtPortresourceName, exists := cm.Data["mgmt-port-resource-name"]
		if exists {
			ovnConfigResult.MgmtPortResourceName = mgmtPortresourceName
		}
	}

	// We want to see if there are any nodes that are labeled for specific modes such as Full/SmartNIC/DPU Host/DPU
	// Currently dpu-host and smart-nic are modes that allow CNO to render the corresponding daemonset pods.
	// For DPU-Host mode, CNO will set the DPU Host mode environment variable to render the OVN-Kubernetes pods in DPU Host mode.
	//   Additionally the management port interface is set from a SR-IOV interface.
	// For Smart NIC mode, CNO will set the mode to be Full mode and render the OVN-Kubernetes daemonset pods.
	//   The difference is that the management port is set from a SR-IOV interface.
	// For DPU mode, currently CNO does not render any OVN-Kubernetes daemonset pods (preventing any OVN-Kubernetes
	//   daemonset pods in DPU mode from running), it is done by an external operator.
	ovnConfigResult.DpuHostModeNodes, err = getNodeListByLabel(kubeClient, ovnConfigResult.DpuHostModeLabel)
	if err != nil {
		return nil, fmt.Errorf("could not get node list with label %s : %w", ovnConfigResult.DpuHostModeLabel, err)
	}
	ovnConfigResult.DpuHostModeLabel, ovnConfigResult.DpuHostModeValue, err = getKeyValueFromLabel(ovnConfigResult.DpuHostModeLabel)
	if err != nil {
		return nil, fmt.Errorf("could not get key and value from label %s : %w", ovnConfigResult.DpuHostModeLabel, err)
	}

	ovnConfigResult.DpuModeNodes, err = getNodeListByLabel(kubeClient, ovnConfigResult.DpuModeLabel)
	if err != nil {
		return nil, fmt.Errorf("could not get node list with label %s : %w", ovnConfigResult.DpuModeLabel, err)
	}
	ovnConfigResult.DpuModeLabel, _, err = getKeyValueFromLabel(ovnConfigResult.DpuModeLabel)
	if err != nil {
		return nil, fmt.Errorf("could not get key and value from label %s : %w", ovnConfigResult.DpuModeLabel, err)
	}

	ovnConfigResult.SmartNicModeNodes, err = getNodeListByLabel(kubeClient, ovnConfigResult.SmartNicModeLabel)
	if err != nil {
		return nil, fmt.Errorf("could not get node list with label %s : %w", ovnConfigResult.SmartNicModeLabel, err)
	}
	ovnConfigResult.SmartNicModeLabel, ovnConfigResult.SmartNicModeValue, err = getKeyValueFromLabel(ovnConfigResult.SmartNicModeLabel)
	if err != nil {
		return nil, fmt.Errorf("could not get key and value from label %s : %w", ovnConfigResult.SmartNicModeLabel, err)
	}

	// No node shall have any other label set. Each node should be ONLY be DPU, DPU Host, or Smart NIC.
	found, nodeName := findCommonNode(ovnConfigResult.DpuHostModeNodes, ovnConfigResult.DpuModeNodes, ovnConfigResult.SmartNicModeNodes)
	if found {
		return nil, fmt.Errorf("node %s has multiple hardware offload labels", nodeName)
	}

	ovnConfigResult.ConfigOverrides, err = getOVNKubernetesConfigOverrides(kubeClient)
	if err != nil {
		return nil, fmt.Errorf("could not get OVN Kubernetes config overrides: %w", err)
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
	}

	if err := validateOVNKubernetesSubnets(conf); err != nil {
		out = append(out, err)
	}
	return out
}

func getOVNEncapOverhead(conf *operv1.NetworkSpec) uint32 {
	const geneveOverhead = 100
	const ipsecOverhead = 46 // Transport mode, AES-GCM
	var encapOverhead uint32 = geneveOverhead
	mode := GetIPsecMode(conf.DefaultNetwork.OVNKubernetesConfig)
	if mode == operv1.IPsecModeFull {
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
		geneve := uint32(6081)
		sc.GenevePort = &geneve
	}

	if sc.PolicyAuditConfig == nil {
		sc.PolicyAuditConfig = &operv1.PolicyAuditConfig{}
	}

	if sc.PolicyAuditConfig.RateLimit == nil {
		ratelimit := uint32(20)
		sc.PolicyAuditConfig.RateLimit = &ratelimit
	}
	if sc.PolicyAuditConfig.MaxFileSize == nil {
		maxfilesize := uint32(50)
		sc.PolicyAuditConfig.MaxFileSize = &maxfilesize
	}
	if sc.PolicyAuditConfig.Destination == "" {
		sc.PolicyAuditConfig.Destination = "null"
	}
	if sc.PolicyAuditConfig.SyslogFacility == "" {
		sc.PolicyAuditConfig.SyslogFacility = "local0"
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

func bootstrapOVN(conf *operv1.Network, kubeClient cnoclient.Client, infraStatus *bootstrap.InfraStatus) (*bootstrap.OVNBootstrapResult, error) {
	clusterConfig := &corev1.ConfigMap{}
	clusterConfigLookup := types.NamespacedName{Name: CLUSTER_CONFIG_NAME, Namespace: CLUSTER_CONFIG_NAMESPACE}

	if err := kubeClient.ClientFor("").CRClient().Get(context.TODO(), clusterConfigLookup, clusterConfig); err != nil {
		return nil, fmt.Errorf("unable to bootstrap OVN, unable to retrieve cluster config: %s", err)
	}

	rcD := replicaCountDecoder{}
	if err := yaml.Unmarshal([]byte(clusterConfig.Data["install-config"]), &rcD); err != nil {
		return nil, fmt.Errorf("unable to bootstrap OVN, unable to unmarshal install-config: %s", err)
	}

	hc := hypershift.NewHyperShiftConfig()
	ovnConfigResult, err := bootstrapOVNConfig(conf, kubeClient, hc, infraStatus)
	if err != nil {
		return nil, fmt.Errorf("unable to bootstrap OVN config, err: %v", err)
	}

	var controlPlaneReplicaCount int
	if hc.Enabled {
		controlPlaneReplicaCount = ovnConfigResult.HyperShiftConfig.ControlPlaneReplicas
	} else {
		controlPlaneReplicaCount, _ = strconv.Atoi(rcD.ControlPlane.Replicas)
	}

	// Retrieve existing daemonset and deployment status - used for deciding if upgrades should happen
	var nsn types.NamespacedName
	nodeStatus := &bootstrap.OVNUpdateStatus{}
	controlPlaneStatus := &bootstrap.OVNUpdateStatus{}
	ovnIPsecStatus := &bootstrap.OVNIPsecStatus{}
	prepullerStatus := &bootstrap.OVNUpdateStatus{}

	namespaceForControlPlane := util.OVN_NAMESPACE
	clusterClientForControlPlane := kubeClient.ClientFor("")

	if hc.Enabled {
		clusterClientForControlPlane = kubeClient.ClientFor(names.ManagementClusterName)
		namespaceForControlPlane = hc.Namespace
	}
	// control plane deployment
	controlPlaneDeployment := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Deployment",
			APIVersion: appsv1.SchemeGroupVersion.String(),
		},
	}

	nsn = types.NamespacedName{Namespace: namespaceForControlPlane, Name: util.OVN_CONTROL_PLANE}
	if err := clusterClientForControlPlane.CRClient().Get(context.TODO(), nsn, controlPlaneDeployment); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("failed to retrieve %s deployment: %w", util.OVN_CONTROL_PLANE, err)
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

		klog.Infof("%s deployment status: progressing=%t",
			util.OVN_CONTROL_PLANE, controlPlaneStatus.Progressing)

	}

	// ovnkube-node daemonset
	nodeDaemonSet := &appsv1.DaemonSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "DaemonSet",
			APIVersion: appsv1.SchemeGroupVersion.String(),
		},
	}
	nsn = types.NamespacedName{Namespace: util.OVN_NAMESPACE, Name: util.OVN_NODE}
	if err := kubeClient.ClientFor("").CRClient().Get(context.TODO(), nsn, nodeDaemonSet); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("failed to retrieve existing ovnkube-node DaemonSet: %w", err)
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
		// Retrieve OVN IPsec status from ovnkube-node daemonset as this is being used to rollout IPsec
		// config.
		ovnIPsecStatus.IsOVNIPsecActiveOrRollingOut = !isOVNIPsecNotActiveInDaemonSet(nodeDaemonSet)
		klog.Infof("ovnkube-node DaemonSet status: progressing=%t", nodeStatus.Progressing)

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
			return nil, fmt.Errorf("failed to retrieve existing prepuller DaemonSet: %w", err)
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

	res := bootstrap.OVNBootstrapResult{
		ControlPlaneReplicaCount: controlPlaneReplicaCount,
		ControlPlaneUpdateStatus: controlPlaneStatus,
		NodeUpdateStatus:         nodeStatus,
		IPsecUpdateStatus:        ovnIPsecStatus,
		PrePullerUpdateStatus:    prepullerStatus,
		OVNKubernetesConfig:      ovnConfigResult,
		FlowsConfig:              bootstrapFlowsConfig(kubeClient.ClientFor("").CRClient()),
	}

	// preserve any default masquerade subnet values that might have been set previously
	if masqueradeCIDRs, ok := nodeDaemonSet.GetAnnotations()[names.MasqueradeCIDRsAnnotation]; ok {
		for _, masqueradeCIDR := range strings.Split(masqueradeCIDRs, ",") {
			if utilnet.IsIPv6CIDRString(masqueradeCIDR) {
				klog.Infof("Found the DefaultV6MasqueradeSubnet(%s) in the %q annotation", masqueradeCIDR, names.MasqueradeCIDRsAnnotation)
				res.DefaultV6MasqueradeSubnet = masqueradeCIDR
			} else if utilnet.IsIPv4CIDRString(masqueradeCIDR) {
				klog.Infof("Found the DefaultV4MasqueradeSubnet(%s) in the %q annotation", masqueradeCIDR, names.MasqueradeCIDRsAnnotation)
				res.DefaultV4MasqueradeSubnet = masqueradeCIDR
			} else {
				return nil, fmt.Errorf("invalid masquerade CIDR %q found in %q", masqueradeCIDR, masqueradeCIDRs)
			}
		}
	}

	// set the default masquerade CIDR for new clusters while ignoring upgrades
	if res.ControlPlaneUpdateStatus == nil || res.NodeUpdateStatus == nil {
		klog.Infof("Configuring the default masquerade subnets to %q and %q", defaultV4MasqueradeSubnet, defaultV6MasqueradeSubnet)
		res.DefaultV4MasqueradeSubnet = defaultV4MasqueradeSubnet
		res.DefaultV6MasqueradeSubnet = defaultV6MasqueradeSubnet
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

func getClusterCIDRsFromConfig(conf *operv1.NetworkSpec) string {
	// pretty print the clusterNetwork CIDR (possibly only one) in its annotation
	var clusterNetworkCIDRs []string
	for _, c := range conf.ClusterNetwork {
		clusterNetworkCIDRs = append(clusterNetworkCIDRs, c.CIDR)
	}
	return strings.Join(clusterNetworkCIDRs, ",")
}

// handleIPFamilyAnnotationAndIPFamilyChange reads the desired IP family mode (single or dual stack) from config,
// and annotates the ovnk DaemonSet and deployment with that value.
// If the config value is different from the current mode, then it applies
// the new mode first to the ovnkube-node DaemonSet and then to the control plane.
func handleIPFamilyAnnotationAndIPFamilyChange(conf *operv1.NetworkSpec, ovn bootstrap.OVNBootstrapResult, objs *[]*uns.Unstructured) (bool, bool, error) {

	// obtain the new IP family mode from config: single or dual stack
	ipFamilyModeFromConfig := names.IPFamilySingleStack
	if len(conf.ServiceNetwork) == 2 {
		ipFamilyModeFromConfig = names.IPFamilyDualStack
	}
	ipFamilyMode := ipFamilyModeFromConfig
	clusterNetworkCIDRs := getClusterCIDRsFromConfig(conf)

	// check if the IP family mode has changed and control the conversion process.
	updateNode, updateControlPlane := shouldUpdateOVNKonIPFamilyChange(ovn, ovn.ControlPlaneUpdateStatus, ipFamilyMode)
	klog.Infof("IP family change: updateNode=%t, updateControlPlane=%t", updateNode, updateControlPlane)

	// (always) annotate the daemonset and the daemonset template with the current IP family mode.
	// This triggers a daemonset restart if there are changes.
	err := setOVNObjectAnnotation(*objs, names.NetworkIPFamilyModeAnnotation, ipFamilyMode)
	if err != nil {
		return true, true, errors.Wrapf(err, "failed to set IP family %s annotation on daemonset and deployment", ipFamilyMode)
	}

	err = setOVNObjectAnnotation(*objs, names.ClusterNetworkCIDRsAnnotation, clusterNetworkCIDRs)
	if err != nil {
		return true, true, errors.Wrapf(err, "failed to set %s annotation on daemonset and deployment", clusterNetworkCIDRs)
	}

	return updateNode, updateControlPlane, nil
}

// shouldUpdateOVNKonIPFamilyChange determines if we should roll out changes to
// the control-plane and node objects on IP family configuration changes.
// We rollout changes on control-plane first when there is a configuration change.
// Configuration changes take precedence over upgrades.
// TODO is this really necessary now? MAYBE for IP family change, since IPAM is done in control plane?
func shouldUpdateOVNKonIPFamilyChange(ovn bootstrap.OVNBootstrapResult, controlPlaneStatus *bootstrap.OVNUpdateStatus, ipFamilyMode string) (updateNode, updateControlPlane bool) {
	// Fresh cluster - full steam ahead!
	if ovn.NodeUpdateStatus == nil || controlPlaneStatus == nil {
		return true, true
	}
	// check current IP family mode
	klog.Infof("IP family mode: node=%s, controlPlane=%s", ovn.NodeUpdateStatus.IPFamilyMode, ovn.NodeUpdateStatus.IPFamilyMode)
	// if there are no annotations this is a fresh cluster
	if ovn.NodeUpdateStatus.IPFamilyMode == "" || controlPlaneStatus.IPFamilyMode == "" {
		return true, true
	}
	// return if there are no IP family mode changes
	if ovn.NodeUpdateStatus.IPFamilyMode == ipFamilyMode && controlPlaneStatus.IPFamilyMode == ipFamilyMode {
		return true, true
	}
	// If the control-plane config has changed update only the control-plane, the node will be updated later
	if controlPlaneStatus.IPFamilyMode != ipFamilyMode {
		klog.V(2).Infof("IP family mode change detected to %s, updating OVN-Kubernetes control plane", ipFamilyMode)
		return false, true
	}
	// Don't rollout the changes on nodes until the control-plane rollout has finished
	if controlPlaneStatus.Progressing {
		klog.V(2).Infof("Waiting for IP family mode rollout of OVN-Kubernetes control-plane before updating node")
		return false, true
	}

	klog.V(2).Infof("OVN-Kubernetes control-plane rollout complete, updating IP family mode on node daemonset")
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

// isCNOIPsecMachineConfigPresent returns true if CNO owned MachineConfigs for IPsec plugin
// are already present in both master and worker nodes, otherwise returns false.
func isCNOIPsecMachineConfigPresent(infra bootstrap.InfraStatus) bool {
	isCNOIPsecMachineConfigPresentIn := func(mcs []*mcfgv1.MachineConfig) bool {
		for _, mc := range mcs {
			if k8s.ContainsNetworkOwnerRef(mc.OwnerReferences) {
				return true
			}
		}
		return false
	}
	return isCNOIPsecMachineConfigPresentIn(infra.MasterIPsecMachineConfigs) &&
		isCNOIPsecMachineConfigPresentIn(infra.WorkerIPsecMachineConfigs)
}

// isUserDefinedIPsecMachineConfigPresent returns true if user owned MachineConfigs for IPsec
// are already present in both master and worker nodes, otherwise returns false.
func isUserDefinedIPsecMachineConfigPresent(infra bootstrap.InfraStatus) bool {
	isUserDefinedMachineConfigPresentIn := func(mcs []*mcfgv1.MachineConfig) bool {
		for _, mc := range mcs {
			if mcutil.IsUserDefinedIPsecMachineConfig(mc) {
				return true
			}
		}
		return false
	}
	return isUserDefinedMachineConfigPresentIn(infra.MasterIPsecMachineConfigs) &&
		isUserDefinedMachineConfigPresentIn(infra.WorkerIPsecMachineConfigs)
}

// isIPsecMachineConfigActive returns true if both master and worker's machine config pools are ready with
// ipsec machine config extension rolled out, otherwise returns false.
func isIPsecMachineConfigActive(infra bootstrap.InfraStatus) bool {
	if infra.MasterIPsecMachineConfigs == nil || infra.WorkerIPsecMachineConfigs == nil {
		// One of the IPsec MachineConfig is not created yet, so return false.
		return false
	}
	if len(infra.MasterMCPStatuses) == 0 || len(infra.WorkerMCPStatuses) == 0 {
		// When none of MachineConfig pools exist, then return false. needed for unit test.
		return false
	}
	masterIPsecMachineConfigNames := sets.Set[string]{}
	for _, machineConfig := range infra.MasterIPsecMachineConfigs {
		masterIPsecMachineConfigNames.Insert(machineConfig.Name)
	}
	for _, masterMCPStatus := range infra.MasterMCPStatuses {
		if !mcutil.AreMachineConfigsRenderedOnPool(masterMCPStatus, masterIPsecMachineConfigNames) {
			return false
		}
	}
	workerIPsecMachineConfigNames := sets.Set[string]{}
	for _, machineConfig := range infra.WorkerIPsecMachineConfigs {
		workerIPsecMachineConfigNames.Insert(machineConfig.Name)
	}
	for _, workerMCPStatus := range infra.WorkerMCPStatuses {
		if !mcutil.AreMachineConfigsRenderedOnPool(workerMCPStatus, workerIPsecMachineConfigNames) {
			return false
		}
	}
	return true
}

// shouldUpdateOVNKonUpgrade determines if we should roll out changes to
// the node daemonset and the control plane deployment upon upgrades.
// We roll out node first, then control plane. In downgrades, we do the opposite.
func shouldUpdateOVNKonUpgrade(ovn bootstrap.OVNBootstrapResult, releaseVersion string) (updateNode, updateControlPlane bool) {
	// Fresh cluster - full steam ahead!
	if ovn.NodeUpdateStatus == nil || ovn.ControlPlaneUpdateStatus == nil {
		return true, true
	}

	nodeVersion := ovn.NodeUpdateStatus.Version
	controlPlaneVersion := ovn.ControlPlaneUpdateStatus.Version

	// shortcut - we're all rolled out.
	// Return true so that we reconcile any changes that somehow could have happened.
	if nodeVersion == releaseVersion && controlPlaneVersion == releaseVersion {
		klog.V(2).Infof("OVN-Kubernetes control-plane and node already at release version %s; no changes required", releaseVersion)
		return true, true
	}

	// compute version delta
	// versionUpgrade means the existing daemonSet needs an upgrade.
	controlPlaneDelta := version.CompareVersions(controlPlaneVersion, releaseVersion)
	nodeDelta := version.CompareVersions(nodeVersion, releaseVersion)

	if controlPlaneDelta == version.VersionUnknown || nodeDelta == version.VersionUnknown {
		klog.Warningf("could not determine ovn-kubernetes daemonset update directions; node: %s, control-plane: %s, release: %s",
			nodeVersion, controlPlaneVersion, releaseVersion)
		return true, true
	}

	klog.V(2).Infof("OVN-Kubernetes control-plane version %s -> latest %s; delta %s", controlPlaneVersion, releaseVersion, controlPlaneDelta)
	klog.V(2).Infof("OVN-Kubernetes node version %s -> latest %s; delta %s", nodeVersion, releaseVersion, nodeDelta)

	// **************************************
	// TODO: the table below can be further simplified along with the upgrade logic:
	// The OVNK control plane can be deployed /at the same time/ as ovnk node. No need to do one first and then the other.
	// **************************************
	// 9 cases
	// +-------------+----------------------+------------------------+-------------------------+
	// |    Delta    |  control plane upg.  |    control plane OK    |   control plane downg.  |
	// +-------------+----------------------+------------------------+-------------------------+
	// | node upg.   | upgrade node         | error                  | error                   |
	// | node OK     | wait for node        | done                   | error                   |
	// | node downg. | error                | wait for control plane | downgrade control plane |
	// +-------------+----------------------+------------------------+-------------------------++

	// both older (than CNO)
	// Update node only.
	if controlPlaneDelta == version.VersionUpgrade && nodeDelta == version.VersionUpgrade {
		klog.V(2).Infof("Upgrading OVN-Kubernetes node before control-plane")
		return true, false
	}

	// control plane older, node updated
	// update control plane if node is rolled out
	if controlPlaneDelta == version.VersionUpgrade && nodeDelta == version.VersionSame {
		if ovn.NodeUpdateStatus.Progressing {
			klog.V(2).Infof("Waiting for OVN-Kubernetes node update to roll out before updating control-plane")
			return true, false
		}
		klog.V(2).Infof("OVN-Kubernetes node update rolled out; now updating control-plane")
		return true, true
	}

	// both newer
	// downgrade control plane before node
	if controlPlaneDelta == version.VersionDowngrade && nodeDelta == version.VersionDowngrade {
		klog.V(2).Infof("Downgrading OVN-Kubernetes control-plane before node")
		return false, true
	}

	// control plane same, node needs downgrade
	// wait for control plane rollout
	if controlPlaneDelta == version.VersionSame && nodeDelta == version.VersionDowngrade {
		if ovn.ControlPlaneUpdateStatus.Progressing {
			klog.V(2).Infof("Waiting for OVN-Kubernetes control-plane downgrade to roll out before downgrading node")
			return false, true
		}
		klog.V(2).Infof("OVN-Kubernetes control-plane update rolled out; now downgrading node")
		return true, true
	}

	// unlikely, should be caught above
	if controlPlaneDelta == version.VersionSame && nodeDelta == version.VersionSame {
		return true, true
	}

	klog.Warningf("OVN-Kubernetes daemonset versions inconsistent. node: %s, control-plane: %s, release: %s",
		nodeVersion, controlPlaneVersion, releaseVersion)
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

// setOVNObjectAnnotation annotates the OVNkube node and control plane
// it also annotates the template with the provided key and value to force the rollout
func setOVNObjectAnnotation(objs []*uns.Unstructured, key, value string) error {
	for _, obj := range objs {
		if obj.GetAPIVersion() == "apps/v1" &&
			(obj.GetKind() == "DaemonSet" || obj.GetKind() == "Deployment") &&
			(obj.GetName() == util.OVN_NODE ||
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

func isV4NodeSubnetLargeEnough(cn []operv1.ClusterNetworkEntry, nodeSubnet string) bool {
	var maxNodesNum int
	addrLen := 32
	for _, n := range cn {
		if utilnet.IsIPv6CIDRString(n.CIDR) {
			continue
		}
		mask, _ := strconv.Atoi(strings.Split(n.CIDR, "/")[1])
		nodesNum := 1 << (int(n.HostPrefix) - mask)
		maxNodesNum += nodesNum
	}
	// We need to ensure each node can be assigned an IP address from the internal subnet
	intSubnetMask, _ := strconv.Atoi(strings.Split(nodeSubnet, "/")[1])
	// reserve one IP for the gw, one IP for network and one for broadcasting
	return maxNodesNum < (1<<(addrLen-intSubnetMask) - 3)
}

func isV6NodeSubnetLargeEnough(cn []operv1.ClusterNetworkEntry, nodeSubnet string) bool {
	var addrLen uint32
	maxNodesNum, nodesNum, capacity := new(big.Int), new(big.Int), new(big.Int)
	addrLen = 128
	for _, n := range cn {
		if !utilnet.IsIPv6CIDRString(n.CIDR) {
			continue
		}
		mask, _ := strconv.Atoi(strings.Split(n.CIDR, "/")[1])
		nodesNum.Lsh(big.NewInt(1), uint(n.HostPrefix)-uint(mask))
		maxNodesNum.Add(maxNodesNum, nodesNum)
	}
	// We need to ensure each node can be assigned an IP address from the internal subnet
	intSubnetMask, _ := strconv.Atoi(strings.Split(nodeSubnet, "/")[1])
	capacity.Lsh(big.NewInt(1), uint(addrLen)-uint(intSubnetMask))
	// reserve one IP for the gw, one IP for network and one for broadcasting
	return capacity.Cmp(maxNodesNum.Add(maxNodesNum, big.NewInt(3))) != -1
}

func isOVNIPsecNotActiveInDaemonSet(ds *appsv1.DaemonSet) bool {
	// If no daemonset, then return false.
	if ds == nil {
		return false
	}
	// When observed generation doesn't match with spec generation or in progressing state
	// then return false as we are not sure about IPsec state.
	if ds.Generation != ds.Status.ObservedGeneration || daemonSetProgressing(ds, true) {
		return false
	}
	annotations := ds.GetAnnotations()
	// If OVN daemonset is set with IPsecEnableAnnotation, then return false.
	if annotations[names.IPsecEnableAnnotation] != "" {
		return false
	}
	// If IPsec is running with older version and ipsec=true is found from nbdb container, then return false.
	if !version.IsVersionGreaterThanOrEqualTo(annotations["release.openshift.io/version"], 4, 15) &&
		isIPSecEnabledInPod(ds.Spec.Template, util.OVN_NBDB) {
		return false
	}
	// All other cases, return true.
	return true
}

func isIPSecEnabledInPod(pod corev1.PodTemplateSpec, containerName string) bool {
	for _, container := range pod.Spec.Containers {
		if container.Name == containerName {
			for _, c := range container.Lifecycle.PostStart.Exec.Command {
				if strings.Contains(c, "ipsec=true") {
					return true
				}
			}
			break
		}
	}
	return false
}

// Validate configurable subnets
//   - Checks whether any subnet overlaps with any other subnet or not
//   - Validates whether provided subnets belong to same IP family as ClusterNetwork or not
//   - Validates whether provided subnets have enough IPs to allocate to all nodes as per
//     Clusternetwork CIDR and hostPrefix
//   - Exhibits error if InternalJoinSubnet is not same as InternalSubnet if both are present
func validateOVNKubernetesSubnets(conf *operv1.NetworkSpec) error {
	if conf.DefaultNetwork.OVNKubernetesConfig == nil {
		return nil
	}
	out := []error{}
	pool := iputil.IPPool{}
	var cnHasIPv4, cnHasIPv6 bool
	for _, cn := range conf.ClusterNetwork {
		_, cidr, err := net.ParseCIDR(cn.CIDR)
		if err != nil {
			out = append(out, errors.Errorf("could not parse spec.clusterNetwork %s", cn.CIDR))
			continue
		}
		if utilnet.IsIPv6CIDRString(cn.CIDR) {
			cnHasIPv6 = true
		} else {
			cnHasIPv4 = true
		}
		if err := pool.Add(*cidr); err != nil {
			out = append(out, errors.Errorf("Whole or subset of ClusterNetwork CIDR %s is already in use: %s", cn.CIDR, err))
		}
	}
	for _, snet := range conf.ServiceNetwork {
		_, cidr, err := net.ParseCIDR(snet)
		if err != nil {
			out = append(out, errors.Wrapf(err, "could not parse spec.serviceNetwork %s", snet))
			continue
		}
		if err := pool.Add(*cidr); err != nil {
			out = append(out, errors.Errorf("Whole or subset of ServiceNetwork CIDR %s is already in use: %s", snet, err))
		}
	}

	oc := conf.DefaultNetwork.OVNKubernetesConfig
	// Note: oc.V4InternalSubnet will be deprecated in future as per k8s guidelines
	// oc.V4InternalSubnet and oc.IPv4.InternalJoinSubnet must be same if both are present
	v4InternalSubnet := oc.V4InternalSubnet
	if oc.IPv4 != nil && oc.IPv4.InternalJoinSubnet != "" {
		if v4InternalSubnet != "" && v4InternalSubnet != oc.IPv4.InternalJoinSubnet {
			out = append(out, errors.Errorf("v4InternalSubnet will be deprecated soon, until then it must be same as v4InternalJoinSubnet %s ", oc.IPv4.InternalJoinSubnet))
		}
		v4InternalSubnet = oc.IPv4.InternalJoinSubnet
	}
	if v4InternalSubnet != "" {
		if !cnHasIPv4 {
			out = append(out, errors.Errorf("JoinSubnet %s and ClusterNetwork must have matching IP families", v4InternalSubnet))
		}
		if err := validateOVNKubernetesSubnet("v4InternalJoinSubnet", v4InternalSubnet, &pool, conf.ClusterNetwork); err != nil {
			out = append(out, err)
		}
	}

	// Note: oc.V6InternalSubnet will be deprecated in future as per k8s guidelines
	// oc.V6InternalSubnet and oc.IPv6.InternalJoinSubnet must be same if both are present
	v6InternalSubnet := oc.V6InternalSubnet
	if oc.IPv6 != nil && oc.IPv6.InternalJoinSubnet != "" {
		if v6InternalSubnet != "" && v6InternalSubnet != oc.IPv6.InternalJoinSubnet {
			out = append(out, errors.Errorf("v6InternalSubnet will be deprecated soon, until then it must be same as v6InternalJoinSubnet %s ", oc.IPv6.InternalJoinSubnet))
		}
		v6InternalSubnet = oc.IPv6.InternalJoinSubnet
	}
	if v6InternalSubnet != "" {
		if !cnHasIPv6 {
			out = append(out, errors.Errorf("JoinSubnet %s and ClusterNetwork must have matching IP families", v6InternalSubnet))
		}
		if err := validateOVNKubernetesSubnet("v6InternalJoinSubnet", v6InternalSubnet, &pool, conf.ClusterNetwork); err != nil {
			out = append(out, err)
		}
	}

	if oc.IPv4 != nil && oc.IPv4.InternalTransitSwitchSubnet != "" {
		if !cnHasIPv4 {
			out = append(out, errors.Errorf("v4InternalTransitSwitchSubnet %s and ClusterNetwork must have matching IP families", oc.IPv4.InternalTransitSwitchSubnet))
		}
		if err := validateOVNKubernetesSubnet("v4InternalTransitSwitchSubnet", oc.IPv4.InternalTransitSwitchSubnet, &pool, conf.ClusterNetwork); err != nil {
			out = append(out, err)
		}
	}

	if oc.IPv6 != nil && oc.IPv6.InternalTransitSwitchSubnet != "" {
		if !cnHasIPv6 {
			out = append(out, errors.Errorf("v6InternalTransitSwitchSubnet %s and ClusterNetwork must have matching IP families", oc.IPv6.InternalTransitSwitchSubnet))
		}
		if err := validateOVNKubernetesSubnet("v6InternalTransitSwitchSubnet", oc.IPv6.InternalTransitSwitchSubnet, &pool, conf.ClusterNetwork); err != nil {
			out = append(out, err)
		}
	}

	// Gateway Configurable Subnet Checks
	// Validate whether masquerade CIDR is from same IP family as clusterNetwork.
	if oc.GatewayConfig != nil {
		if oc.GatewayConfig.IPv4.InternalMasqueradeSubnet != "" {
			if !cnHasIPv4 {
				out = append(out, errors.Errorf("v4InternalMasqueradeSubnet %s and ClusterNetwork must have matching IP families", oc.GatewayConfig.IPv4.InternalMasqueradeSubnet))
			}
			// Masquerade subnet does not need subnet length check. Sending ClusterNetwork
			// nil while calling validateOVNKubernetesSubnet to avoid subnet length check.
			if err := validateOVNKubernetesSubnet("v4InternalMasqueradeSubnet", oc.GatewayConfig.IPv4.InternalMasqueradeSubnet, &pool, nil); err != nil {
				out = append(out, err)
			}
		}
		if oc.GatewayConfig.IPv6.InternalMasqueradeSubnet != "" {
			if !cnHasIPv6 {
				out = append(out, errors.Errorf("v6InternalMasqueradeSubnet %s and ClusterNetwork must have matching IP families", oc.GatewayConfig.IPv6.InternalMasqueradeSubnet))
			}
			// Masquerade subnet does not need subnet length check. Sending ClusterNetwork
			// nil while calling validateOVNKubernetesSubnet to avoid subnet length check.
			if err := validateOVNKubernetesSubnet("v6InternalMasqueradeSubnet", oc.GatewayConfig.IPv6.InternalMasqueradeSubnet, &pool, nil); err != nil {
				out = append(out, err)
			}
		}
	}

	return kerrors.NewAggregate(out)
}

// Check subnet length and overlapping with other subnets
func validateOVNKubernetesSubnet(name, subnet string, otherSubnets *iputil.IPPool, cn []operv1.ClusterNetworkEntry) error {
	_, cidr, err := net.ParseCIDR(subnet)
	if err != nil {
		return fmt.Errorf("%s is invalid: %s", name, err)
	} else if cn != nil && !utilnet.IsIPv6CIDRString(subnet) {
		if !isV4NodeSubnetLargeEnough(cn, subnet) {
			return fmt.Errorf("%s %s is not large enough for the maximum number of nodes which can be supported by ClusterNetwork", name, subnet)
		}
	} else if cn != nil && utilnet.IsIPv6CIDRString(subnet) {
		if !isV6NodeSubnetLargeEnough(cn, subnet) {
			return fmt.Errorf("%s %s is not large enough for the maximum number of nodes which can be supported by ClusterNetwork", name, subnet)
		}
	}
	if err := otherSubnets.Add(*cidr); err != nil {
		return fmt.Errorf("whole or subset of %s CIDR %s is already in use: %s", name, subnet, err)
	}
	return nil
}

// getOVNKubernetesConfigOverrides retrieves OVN Kubernetes configuration overrides from the
// openshift-network-operator/ovn-kubernetes-config-overrides configmap.
// If the configmap exists, it returns the data as a map.
// If the configmap does not exist, it returns nil, indicating that no overrides are set
// and no error.
// If there is an error retrieving the configmap, it returns an error.
func getOVNKubernetesConfigOverrides(client cnoclient.Client) (map[string]string, error) {
	configMap := &corev1.ConfigMap{}
	if err := client.Default().CRClient().Get(context.TODO(),
		types.NamespacedName{Name: OVNKubernetesConfigOverridesCMName, Namespace: names.APPLIED_NAMESPACE}, configMap); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("unable to retrieve config from configmap %v: %s", OVNKubernetesConfigOverridesCMName, err)
	}
	return configMap.Data, nil
}

// isFeatureGateEnabled safely checks if a feature gate is enabled.
// It returns false if the feature gate is not known (not registered in the cluster's feature gates)
// to avoid panics from calling Enabled() on unknown feature gates.
func isFeatureGateEnabled(fg featuregates.FeatureGate, name configv1.FeatureGateName) bool {
	if !slices.Contains(fg.KnownFeatures(), name) {
		return false
	}
	return fg.Enabled(name)
}
