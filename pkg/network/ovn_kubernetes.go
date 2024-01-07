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
	goruntime "runtime"
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
	OVSFlowsConfigMapName   = "ovs-flows-config"
	OVSFlowsConfigNamespace = names.APPLIED_NAMESPACE
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
	data.Data["V4JoinSubnet"] = c.V4InternalSubnet
	data.Data["V6JoinSubnet"] = c.V6InternalSubnet
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

	//TO-REMOVE debug log msg
	klog.Infof("IPsec: MC (NS || EW): %v, DS (EW): %v", data.Data["IPsecMachineConfigEnable"], data.Data["OVNIPsecDaemonsetEnable"])

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
	if bootstrapResult.OVN.ControlPlaneReplicaCount == 1 {
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

// shouldRenderIPsec method ensures the have following IPsec states for upgrade path from 4.14 to 4.15 or later versions:
// When 4.14 cluster is already installed with MachineConfig for IPsec extension and ipsecConfig is set in network operator
// config (i.e. IPsec for NS+EW), then reuse the installed MC extension and render ipsec-host daemonset.
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
func shouldRenderIPsec(conf *operv1.OVNKubernetesConfig, bootstrapResult *bootstrap.BootstrapResult) (renderIPsecMachineConfig, renderIPsecDaemonSet,
	renderIPsecOVN, renderIPsecHostDaemonSet, renderIPsecContainerizedDaemonSet, renderIPsecDaemonSetAsCreateWaitOnly bool) {
	isHypershiftHostedCluster := bootstrapResult.Infra.HostedControlPlane != nil
	isIpsecUpgrade := bootstrapResult.OVN.IPsecUpdateStatus != nil && bootstrapResult.OVN.IPsecUpdateStatus.LegacyIPsecUpgrade
	isOVNIPsecActive := bootstrapResult.OVN.IPsecUpdateStatus != nil && bootstrapResult.OVN.IPsecUpdateStatus.OVNIPsecActive

	// Find the IPsec mode from Ipsec.config
	// Ipsec.config == nil (bw compatibility) || ipsecConfig == Off ==> ipsec is disabled
	// ipsecConfig.mode == "" (bw compatibility) || ipsec.Config == Full ==> ipsec is enabled for NS and EW
	// ipsecConfig.mode == External ==> ipsec is enabled for NS only

	mode := operv1.IPsecModeDisabled // Sould stay so if conf.IPsecConfig == nil
	klog.Infof("IPsec:  Lopoking at ipsecConfig = %+v", conf.IPsecConfig)
	if conf.IPsecConfig != nil {
		klog.Infof("IPsec ipsecConfig = %+v", conf.IPsecConfig)
		if conf.IPsecConfig.Mode != "" {
			mode = conf.IPsecConfig.Mode
		} else {
			mode = operv1.IPsecModeFull // BW compatiniglity with existing configs
			//TO-REMOVE debug log msg
			klog.Infof("IPsec mode is not set in ipsecConfig. Assuming upgrade: setting IPsec mode to Full")
			// For upgrade only - update the object to a valid value
			conf.IPsecConfig.Mode = operv1.IPsecModeFull
		}
	}
	isIPsecEnabled := mode != operv1.IPsecModeDisabled
	isIPsecFull := mode == operv1.IPsecModeFull	// Full mode is for NS+EW
	//TO-REMOVE debug log msg
	klog.Infof("IPsec mode: %s, isIPsecEnabled: %v", mode, isIPsecEnabled)

	// On upgrade, we will just remove any existing ipsec deployment without making any
	// change to them. So during upgrade, we must keep track if IPsec MachineConfigs are
	// active or not for non Hybrid hosted cluster.
	isUserIPsecMachineConfigPresent := isUserIPsecMachineConfigPresent(bootstrapResult.Infra)
	isIpsecMachineConfigActive := isIPsecMachineConfigActive(bootstrapResult.Infra)
	isIPsecMachineConfigNotActiveOnUpgrade := isIpsecUpgrade && !isIpsecMachineConfigActive && !isHypershiftHostedCluster

	// We render the ipsec deployment if IPsec is already active in OVN
	// or if EW IPsec config is enabled.
	renderIPsecDaemonSet = isOVNIPsecActive || isIPsecFull

	// If ipsec is enabled, we render the host ipsec deployment except for
	// hypershift hosted clusters and we need to wait for the ipsec MachineConfig
	// extensions to be active first. We must also render host ipsec deployment
	// at the time of upgrade though user created IPsec Machine Config is not
	// present/active.
	renderIPsecHostDaemonSet = (renderIPsecDaemonSet && isIpsecMachineConfigActive && !isHypershiftHostedCluster) || isIPsecMachineConfigNotActiveOnUpgrade

	// The containerized ipsec deployment is only rendered during upgrades or
	// for hypershift hosted clusters.
	renderIPsecContainerizedDaemonSet = (renderIPsecDaemonSet && isHypershiftHostedCluster) || isIPsecMachineConfigNotActiveOnUpgrade

	// MachineConfig IPsec extensions are needed for the ipsec deployment except
	// when the containerized deployment is used in hypershift hosted clusters.
	// We will rollout unless the user has rolled out its own.
	renderIPsecMachineConfig = (isIPsecEnabled || renderIPsecDaemonSet) && !isUserIPsecMachineConfigPresent && !isHypershiftHostedCluster

	// We render OVN IPsec if IPsec is enabled or it's upgrade is in progress.
	// If NS IPsec is enabled as well, we need to wait to IPsec MachineConfig
	// to be active if it's not an upgrade and not a hypershift hosted cluster.
	renderIPsecOVN = (renderIPsecHostDaemonSet || renderIPsecContainerizedDaemonSet) && isIPsecFull

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

// isUserIPsecMachineConfigPresent returns true if user owned MachineConfigs for IPsec plugin
// are already present either in master or worker nodes, otherwise returns false.
func isUserIPsecMachineConfigPresent(infra bootstrap.InfraStatus) bool {
	return (infra.MasterIPsecMachineConfig != nil && !containsNetworkOwnerRef(infra.MasterIPsecMachineConfig.OwnerReferences)) ||
		(infra.WorkerIPsecMachineConfig != nil && !containsNetworkOwnerRef(infra.WorkerIPsecMachineConfig.OwnerReferences))
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

// isIPsecMachineConfigActive returns true if both master and worker's machine config pool are ready with
// ipsec machine config extension rolled out, otherwise returns false.
func isIPsecMachineConfigActive(infra bootstrap.InfraStatus) bool {
	if infra.MasterIPsecMachineConfig == nil || infra.WorkerIPsecMachineConfig == nil {
		// One of the IPsec MachineConfig is not created yet, so return false.
		return false
	}
	ipSecPluginOnMasterNodes := hasSourceInMachineConfigStatus(infra.MasterMCPStatus, infra.MasterIPsecMachineConfig.Name)
	ipSecPluginOnWorkerNodes := hasSourceInMachineConfigStatus(infra.WorkerMCPStatus, infra.WorkerIPsecMachineConfig.Name)
	return ipSecPluginOnMasterNodes && ipSecPluginOnWorkerNodes
}

func hasSourceInMachineConfigStatus(machineConfigStatus mcfgv1.MachineConfigPoolStatus, sourceName string) bool {
	for _, source := range machineConfigStatus.Configuration.Source {
		if source.Name == sourceName {
			return true
		}
	}
	return false
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
