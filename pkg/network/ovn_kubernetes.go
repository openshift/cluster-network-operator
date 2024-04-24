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
	"strconv"
	"strings"
	"time"

	yaml "github.com/ghodss/yaml"
	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/hypershift"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/render"
	"github.com/openshift/cluster-network-operator/pkg/util"
	iputil "github.com/openshift/cluster-network-operator/pkg/util/ip"
	"github.com/openshift/cluster-network-operator/pkg/util/k8s"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	utilnet "k8s.io/utils/net"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
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
const OVN_NODE_SELECTOR_DEFAULT_DPU_HOST = "network.operator.openshift.io/dpu-host"
const OVN_NODE_SELECTOR_DEFAULT_DPU = "network.operator.openshift.io/dpu"
const OVN_NODE_SELECTOR_DEFAULT_SMART_NIC = "network.operator.openshift.io/smart-nic"
const OVN_NODE_IDENTITY_CERT_DURATION = "24h"

// gRPC healthcheck port. See: https://github.com/openshift/enhancements/pull/1209
const OVN_EGRESSIP_HEALTHCHECK_PORT = "9107"

const (
	OVSFlowsConfigMapName        = "ovs-flows-config"
	OVSFlowsConfigNamespace      = names.APPLIED_NAMESPACE
	defaultV4InternalSubnet      = "100.64.0.0/16"
	defaultV6InternalSubnet      = "fd98::/64"
	defaultV4TransitSwitchSubnet = "100.88.0.0/16"
	defaultV6TransitSwitchSubnet = "fd97::/64"
	defaultV4MasqueradeSubnet    = "169.254.169.0/29"
	defaultV6MasqueradeSubnet    = "fd69::/125"
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
		return nil, progressing, fmt.Errorf("Unable to render OVN in a cluster with an external control plane")
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
	// v4 and v6InternalMasqueradeSubnet are used when the user wants to use the addresses that we reserve in ovn-k for ip masquerading
	if c.GatewayConfig != nil && c.GatewayConfig.IPv4.InternalMasqueradeSubnet != "" {
		data.Data["V4InternalMasqueradeSubnet"] = c.GatewayConfig.IPv4.InternalMasqueradeSubnet
	}
	if c.GatewayConfig != nil && c.GatewayConfig.IPv6.InternalMasqueradeSubnet != "" {
		data.Data["V6InternalMasqueradeSubnet"] = c.GatewayConfig.IPv6.InternalMasqueradeSubnet
	}
	data.Data["EnableUDPAggregation"] = !bootstrapResult.OVN.OVNKubernetesConfig.DisableUDPAggregation
	data.Data["NETWORK_NODE_IDENTITY_ENABLE"] = bootstrapResult.Infra.NetworkNodeIdentityEnabled
	data.Data["NodeIdentityCertDuration"] = OVN_NODE_IDENTITY_CERT_DURATION
	data.Data["IsNetworkTypeLiveMigration"] = false

	if conf.Migration != nil {
		if conf.Migration.MTU != nil && conf.Migration.Mode != operv1.LiveNetworkMigrationMode {
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
		if conf.Migration.Mode == operv1.LiveNetworkMigrationMode {
			data.Data["IsNetworkTypeLiveMigration"] = true
		}
	}
	data.Data["GenevePort"] = c.GenevePort
	data.Data["CNIConfDir"] = pluginCNIConfDir(conf)
	data.Data["CNIBinDir"] = CNIBinDir
	data.Data["OVN_NODE_MODE"] = OVN_NODE_MODE_FULL
	data.Data["DpuHostModeLabel"] = bootstrapResult.OVN.OVNKubernetesConfig.DpuHostModeLabel
	data.Data["DpuModeLabel"] = bootstrapResult.OVN.OVNKubernetesConfig.DpuModeLabel
	data.Data["SmartNicModeLabel"] = bootstrapResult.OVN.OVNKubernetesConfig.SmartNicModeLabel
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

	// Set progressing to true until IPsec DaemonSet is rendered when EW IPsec config is enabled.
	// TODO Do a poor man's job mapping machine config pool status to CNO progressing state for now.
	// This has two problems:
	// - Not a great feedback to the user on why we are progressing other than `Waiting to render manifests`.
	// - If pool status degrades due to CNO's changes, CNO stays progressing where it would be
	//   potentially better to report it as degraded as well.
	// Overall, mapping machine config pool status to CNO status should better be done in status manager.
	// Future efforts on this are tracked in https://issues.redhat.com/browse/SDN-4829.
	progressing = OVNIPsecDaemonsetEnable && !renderIPsecHostDaemonSet && !renderIPsecContainerizedDaemonSet

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

	// leverage feature gates
	data.Data["OVN_ADMIN_NETWORK_POLICY_ENABLE"] = featureGates.Enabled(configv1.FeatureGateAdminNetworkPolicy)
	data.Data["DNS_NAME_RESOLVER_ENABLE"] = featureGates.Enabled(configv1.FeatureGateDNSNameResolver)

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
		data.Data["CAConfigMap"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.CAConfigMap
		data.Data["CAConfigMapKey"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.CAConfigMapKey
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

	// When upgrading a legacy IPsec deployment, avoid any updates until IPsec MachineConfigs
	// are active.
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
		// The legacy ovn-ipsec deployment is only rendered during upgrades until we
		// are ready to remove it.
		ovnIPsecLegacyDS := &appsv1.DaemonSet{
			TypeMeta: metav1.TypeMeta{
				Kind:       "DaemonSet",
				APIVersion: appsv1.SchemeGroupVersion.String(),
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "ovn-ipsec",
				Namespace: util.OVN_NAMESPACE,
				// We never update the legacy ovn-ipsec daemonset.
				Annotations: map[string]string{names.CreateWaitAnnotation: "true"},
			},
		}
		obj, err := k8s.ToUnstructured(ovnIPsecLegacyDS)
		if err != nil {
			return nil, progressing, fmt.Errorf("unable to render legacy ovn-ipsec daemonset: %w", err)
		}
		objs = append(objs, obj)
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

// shouldRenderIPsec method ensures the have following IPsec states for upgrade path from 4.14 to 4.15 or later versions:
// When 4.14 cluster is already installed with MachineConfig for IPsec extension and ipsecConfig is set in network operator
// config (i.e. IPsec for NS+EW), then render CNO's IPsec MC extension and ipsec-host daemonset.
// When 4.14 cluster is just running with ipsecConfig set in network operator config (i.e. IPsec for EW only), then activate
// IPsec MachineConfig and render ipsec-host daemonset.
// When 4.14 cluster is just installed with MachineConfig for IPsec extension (i.e. IPsec for NS only), then just keep MachineConfig
// to be in the same state without rendering IPsec daemonsets.
// When 4.14 cluster is Hypershift cluster running with ipsecConfig set, then just render ovn-ipsec-containerized daemonset as
// MachineConfig kind is not supported there.
// For Upgrade path from pre-4.14 to 5.15 or later versions:
// When pre-4.14 cluster is just running with ipsecConfig set in network operator config (i.e. IPsec for EW only), then activate
// IPsec MachineConfig and render ipsec-host daemonset.
// When pre-4.14 cluster is Hypershift cluster running with ipsecConfig set, then just render ovn-ipsec-containerized daemonset as
// MachineConfig kind is not supported there.
// All Other cases are not supported in pre-4.14 deployments.
func shouldRenderIPsec(conf *operv1.OVNKubernetesConfig, bootstrapResult *bootstrap.BootstrapResult) (renderCNOIPsecMachineConfig, renderIPsecDaemonSet,
	renderIPsecOVN, renderIPsecHostDaemonSet, renderIPsecContainerizedDaemonSet, renderIPsecDaemonSetAsCreateWaitOnly bool) {
	isHypershiftHostedCluster := bootstrapResult.Infra.HostedControlPlane != nil
	isIpsecUpgrade := bootstrapResult.OVN.IPsecUpdateStatus != nil && bootstrapResult.OVN.IPsecUpdateStatus.LegacyIPsecUpgrade
	isOVNIPsecActive := bootstrapResult.OVN.IPsecUpdateStatus != nil && bootstrapResult.OVN.IPsecUpdateStatus.OVNIPsecActive

	mode := GetIPsecMode(conf)

	// On upgrade, we will just remove any existing ipsec deployment without making any
	// change to them. So during upgrade, we must keep track if IPsec MachineConfigs are
	// active or not for non Hybrid hosted cluster.
	isIPsecMachineConfigActive := isIPsecMachineConfigActive(bootstrapResult.Infra)
	isIPsecMachineConfigNotActiveOnUpgrade := isIpsecUpgrade && !isIPsecMachineConfigActive && !isHypershiftHostedCluster
	isMachineConfigClusterOperatorReady := bootstrapResult.Infra.MachineConfigClusterOperatorReady
	isCNOIPsecMachineConfigPresent := isCNOIPsecMachineConfigPresent(bootstrapResult.Infra)

	// We render the ipsec deployment if IPsec is already active in OVN
	// or if EW IPsec config is enabled.
	renderIPsecDaemonSet = isOVNIPsecActive || mode == operv1.IPsecModeFull

	// If ipsec is enabled, we render the host ipsec deployment except for
	// hypershift hosted clusters and we need to wait for the ipsec MachineConfig
	// extensions to be active first. We must also render host ipsec deployment
	// at the time of upgrade though user created IPsec Machine Config is not
	// present/active.
	renderIPsecHostDaemonSet = (renderIPsecDaemonSet && isIPsecMachineConfigActive && !isHypershiftHostedCluster) || isIPsecMachineConfigNotActiveOnUpgrade

	// The containerized ipsec deployment is only rendered during upgrades or
	// for hypershift hosted clusters.
	renderIPsecContainerizedDaemonSet = (renderIPsecDaemonSet && isHypershiftHostedCluster) || isIPsecMachineConfigNotActiveOnUpgrade

	// MachineConfig IPsec extensions rollout is needed for the ipsec enablement and are used in both External and Full modes.
	// except  when the containerized deployment is used in hypershift hosted clusters.
	renderCNOIPsecMachineConfig = (mode != operv1.IPsecModeDisabled || renderIPsecDaemonSet) && !isHypershiftHostedCluster
	// Wait for MCO to be ready unless we had already rendered the IPsec MachineConfig
	renderCNOIPsecMachineConfig = renderCNOIPsecMachineConfig && (isCNOIPsecMachineConfigPresent || isMachineConfigClusterOperatorReady)

	// We render OVN IPsec if East-West IPsec is enabled or it's upgrade is in progress.
	// If NS IPsec is enabled as well, we need to wait to IPsec MachineConfig
	// to be active if it's not an upgrade and not a hypershift hosted cluster.
	renderIPsecOVN = (renderIPsecHostDaemonSet || renderIPsecContainerizedDaemonSet) && mode == operv1.IPsecModeFull

	// While OVN ipsec is being upgraded and IPsec MachineConfigs deployment is in progress
	// (or) IPsec config in OVN is being disabled, then ipsec deployment is not updated.
	renderIPsecDaemonSetAsCreateWaitOnly = isIPsecMachineConfigNotActiveOnUpgrade || (isOVNIPsecActive && !renderIPsecOVN)

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
	switch hcp.ControllerAvailabilityPolicy {
	case hypershift.HighlyAvailable:
		ovnHypershiftResult.ControlPlaneReplicas = 3
	default:
		ovnHypershiftResult.ControlPlaneReplicas = 1
	}
	return ovnHypershiftResult, nil
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

	hc := hypershift.NewHyperShiftConfig()
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
		// Retrieve OVN IPsec status from ovnkube-node daemonset as this is being used to rollout IPsec
		// config from 4.14.
		ovnIPsecStatus.OVNIPsecActive = !isOVNIPsecNotActiveInDaemonSet(nodeDaemonSet)
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

	ipsecStatus := &bootstrap.OVNIPsecStatus{}

	// The IPsec daemonset name is ovn-ipsec if we are upgrading from <= 4.13.
	nsn = types.NamespacedName{Namespace: util.OVN_NAMESPACE, Name: "ovn-ipsec"}
	if err := kubeClient.ClientFor("").CRClient().Get(context.TODO(), nsn, ipsecDaemonSet); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("Failed to retrieve existing pre-4.14 ipsec DaemonSet: %w", err)
		} else {
			ipsecStatus = nil
		}
	} else {
		ipsecStatus.LegacyIPsecUpgrade = true
	}

	if ipsecStatus == nil {
		ipsecStatus = &bootstrap.OVNIPsecStatus{}
		ipsecContainerizedDaemonSet := &appsv1.DaemonSet{
			TypeMeta: metav1.TypeMeta{
				Kind:       "DaemonSet",
				APIVersion: appsv1.SchemeGroupVersion.String(),
			},
		}
		ipsecHostDaemonSet := &appsv1.DaemonSet{
			TypeMeta: metav1.TypeMeta{
				Kind:       "DaemonSet",
				APIVersion: appsv1.SchemeGroupVersion.String(),
			},
		}
		// Retrieve container based IPsec daemonset with name ovn-ipsec-containerized.
		nsn = types.NamespacedName{Namespace: util.OVN_NAMESPACE, Name: "ovn-ipsec-containerized"}
		if err := kubeClient.ClientFor("").CRClient().Get(context.TODO(), nsn, ipsecContainerizedDaemonSet); err != nil {
			if !apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("Failed to retrieve existing ipsec containerized DaemonSet: %w", err)
			} else {
				ipsecContainerizedDaemonSet = nil
			}
		}
		// Retrieve host based IPsec daemonset with name ovn-ipsec-host
		nsn = types.NamespacedName{Namespace: util.OVN_NAMESPACE, Name: "ovn-ipsec-host"}
		if err := kubeClient.ClientFor("").CRClient().Get(context.TODO(), nsn, ipsecHostDaemonSet); err != nil {
			if !apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("Failed to retrieve existing ipsec host DaemonSet: %w", err)
			} else {
				ipsecHostDaemonSet = nil
			}
		}
		if ipsecContainerizedDaemonSet != nil && ipsecHostDaemonSet != nil {
			// Both IPsec daemonset versions exist, so this is an upgrade from 4.14.
			ipsecStatus.LegacyIPsecUpgrade = true
		} else if ipsecContainerizedDaemonSet == nil && ipsecHostDaemonSet == nil {
			ipsecStatus = nil
		}
	}

	// set OVN IPsec status into ipsecStatus only when IPsec daemonset(s) exists in the cluster.
	if ipsecStatus != nil {
		ipsecStatus.OVNIPsecActive = ovnIPsecStatus.OVNIPsecActive
	}

	res := bootstrap.OVNBootstrapResult{
		ControlPlaneReplicaCount: controlPlaneReplicaCount,
		ControlPlaneUpdateStatus: controlPlaneStatus,
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
			if containsNetworkOwnerRef(mc.OwnerReferences) {
				return true
			}
		}
		return false
	}
	return isCNOIPsecMachineConfigPresentIn(infra.MasterIPsecMachineConfigs) &&
		isCNOIPsecMachineConfigPresentIn(infra.WorkerIPsecMachineConfigs)
}

func containsNetworkOwnerRef(ownerRefs []metav1.OwnerReference) bool {
	for _, ownerRef := range ownerRefs {
		if ownerRef.APIVersion == operv1.GroupVersion.String() && ownerRef.Kind == "Network" &&
			(ownerRef.Controller != nil && *ownerRef.Controller) && ownerRef.Name == "cluster" {
			return true
		}
	}
	return false
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
	ipSecPluginOnPool := func(status mcfgv1.MachineConfigPoolStatus, machineConfigs []*mcfgv1.MachineConfig) bool {
		return status.MachineCount == status.UpdatedMachineCount &&
			hasSourceInMachineConfigStatus(status, machineConfigs)
	}
	for _, masterMCPStatus := range infra.MasterMCPStatuses {
		if !ipSecPluginOnPool(masterMCPStatus, infra.MasterIPsecMachineConfigs) {
			return false
		}
	}
	for _, workerMCPStatus := range infra.WorkerMCPStatuses {
		if !ipSecPluginOnPool(workerMCPStatus, infra.WorkerIPsecMachineConfigs) {
			return false
		}
	}
	return true
}

func hasSourceInMachineConfigStatus(machineConfigStatus mcfgv1.MachineConfigPoolStatus, machineConfigs []*mcfgv1.MachineConfig) bool {
	ipSecMachineConfigNames := sets.New[string]()
	for _, machineConfig := range machineConfigs {
		ipSecMachineConfigNames.Insert(machineConfig.Name)
	}
	sourceNames := sets.New[string]()
	for _, source := range machineConfigStatus.Configuration.Source {
		sourceNames.Insert(source.Name)
	}
	return sourceNames.IsSuperset(ipSecMachineConfigNames)
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
	controlPlaneDelta := compareVersions(controlPlaneVersion, releaseVersion)
	nodeDelta := compareVersions(nodeVersion, releaseVersion)

	if controlPlaneDelta == versionUnknown || nodeDelta == versionUnknown {
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
	if controlPlaneDelta == versionUpgrade && nodeDelta == versionUpgrade {
		klog.V(2).Infof("Upgrading OVN-Kubernetes node before control-plane")
		return true, false
	}

	// control plane older, node updated
	// update control plane if node is rolled out
	if controlPlaneDelta == versionUpgrade && nodeDelta == versionSame {
		if ovn.NodeUpdateStatus.Progressing {
			klog.V(2).Infof("Waiting for OVN-Kubernetes node update to roll out before updating control-plane")
			return true, false
		}
		klog.V(2).Infof("OVN-Kubernetes node update rolled out; now updating control-plane")
		return true, true
	}

	// both newer
	// downgrade control plane before node
	if controlPlaneDelta == versionDowngrade && nodeDelta == versionDowngrade {
		klog.V(2).Infof("Downgrading OVN-Kubernetes control-plane before node")
		return false, true
	}

	// control plane same, node needs downgrade
	// wait for control plane rollout
	if controlPlaneDelta == versionSame && nodeDelta == versionDowngrade {
		if ovn.ControlPlaneUpdateStatus.Progressing {
			klog.V(2).Infof("Waiting for OVN-Kubernetes control-plane downgrade to roll out before downgrading node")
			return false, true
		}
		klog.V(2).Infof("OVN-Kubernetes control-plane update rolled out; now downgrading node")
		return true, true
	}

	// unlikely, should be caught above
	if controlPlaneDelta == versionSame && nodeDelta == versionSame {
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
	if !isVersionGreaterThanOrEqualTo(annotations["release.openshift.io/version"], 4, 15) &&
		isIPSecEnabledInPod(ds.Spec.Template, util.OVN_NBDB) {
		return false
	}
	// All other cases, return true.
	return true
}

func isIPSecEnabledInPod(pod v1.PodTemplateSpec, containerName string) bool {
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
		return fmt.Errorf("Whole or subset of %s CIDR %s is already in use: %s", name, subnet, err)
	}
	return nil
}

// GetInternalSubnets returns internal subnet values for both IP families
// It returns default values if conf is nil or the subnets are not configured
func GetInternalSubnets(conf *operv1.OVNKubernetesConfig) (v4Subnet, v6Subnet string) {
	v4Subnet = defaultV4InternalSubnet
	v6Subnet = defaultV6InternalSubnet

	if conf == nil {
		return
	}

	if conf.V4InternalSubnet != "" {
		v4Subnet = conf.V4InternalSubnet
	}
	if conf.IPv4 != nil {
		// conf.IPv4.InternalJoinSubnet takes precedence over conf.V4InternalSubnet
		if conf.IPv4.InternalJoinSubnet != "" {
			v4Subnet = conf.IPv4.InternalJoinSubnet
		}
	}

	if conf.V6InternalSubnet != "" {
		v6Subnet = conf.V6InternalSubnet
	}
	if conf.IPv6 != nil {
		// conf.IPv6.InternalJoinSubnet takes precedence over conf.V6InternalSubnet
		if conf.IPv6.InternalJoinSubnet != "" {
			v6Subnet = conf.IPv6.InternalJoinSubnet
		}
	}
	return
}

// GetTransitSwitchSubnets returns transit switch subnet values for both IP families
// It returns default values if conf is nil or the subnets are not configured
func GetTransitSwitchSubnets(conf *operv1.OVNKubernetesConfig) (v4Subnet, v6Subnet string) {
	v4Subnet = defaultV4TransitSwitchSubnet
	v6Subnet = defaultV6TransitSwitchSubnet

	if conf == nil {
		return
	}

	if conf.IPv4 != nil {
		if conf.IPv4.InternalTransitSwitchSubnet != "" {
			v4Subnet = conf.IPv4.InternalTransitSwitchSubnet
		}
	}

	if conf.IPv6 != nil {
		if conf.IPv6.InternalTransitSwitchSubnet != "" {
			v6Subnet = conf.IPv6.InternalTransitSwitchSubnet
		}
	}
	return
}

// GetMasqueradeSubnet returns masquerade subnet values for both IP families
// It returns default values if conf is nil or the subnets are not configured
func GetMasqueradeSubnet(conf *operv1.OVNKubernetesConfig) (v4Subnet, v6Subnet string) {
	v4Subnet = defaultV4MasqueradeSubnet
	v6Subnet = defaultV6MasqueradeSubnet

	if conf == nil {
		return
	}

	if conf.GatewayConfig != nil {
		if conf.GatewayConfig.IPv4.InternalMasqueradeSubnet != "" {
			v4Subnet = conf.GatewayConfig.IPv4.InternalMasqueradeSubnet
		}
		if conf.GatewayConfig.IPv6.InternalMasqueradeSubnet != "" {
			v4Subnet = conf.GatewayConfig.IPv6.InternalMasqueradeSubnet
		}
	}
	return
}
