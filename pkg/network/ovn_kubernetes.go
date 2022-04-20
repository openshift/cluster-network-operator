package network

import (
	"context"
	"fmt"
	"log"
	"math"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	yaml "github.com/ghodss/yaml"
	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	routev1 "github.com/openshift/api/route/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"github.com/openshift/cluster-network-operator/pkg/client"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/render"
	"github.com/openshift/cluster-network-operator/pkg/util/k8s"
	hyperv1 "github.com/openshift/hypershift/api/v1alpha1"
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
const OVN_NODE_SELECTOR_DPU = "network.operator.openshift.io/dpu: ''"

var OVN_MASTER_DISCOVERY_TIMEOUT = 250

const (
	// TODO: get this from the route Status
	OVN_SB_ROUTE_PORT       = "443"
	OVSFlowsConfigMapName   = "ovs-flows-config"
	OVSFlowsConfigNamespace = names.APPLIED_NAMESPACE
)

// renderOVNKubernetes returns the manifests for the ovn-kubernetes.
// This creates
// - the openshift-ovn-kubernetes namespace
// - the ovn-config ConfigMap
// - the ovnkube-node daemonset
// - the ovnkube-master deployment
// and some other small things.
func renderOVNKubernetes(conf *operv1.NetworkSpec, bootstrapResult *bootstrap.BootstrapResult, manifestDir string) ([]*uns.Unstructured, bool, error) {
	var progressing bool

	// TODO: Fix operator behavior when running in a cluster with an externalized control plane.
	// For now, return an error since we don't have any master nodes to run the ovn-master daemonset.
	if bootstrapResult.Infra.ExternalControlPlane && !bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.Enabled {
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
	data.Data["KubeRBACProxyImage"] = os.Getenv("KUBE_RBAC_PROXY_IMAGE")
	data.Data["KUBERNETES_SERVICE_HOST"] = apiServer.Host
	data.Data["KUBERNETES_SERVICE_PORT"] = apiServer.Port
	data.Data["K8S_APISERVER"] = fmt.Sprintf("https://%s:%s", apiServer.Host, apiServer.Port)
	data.Data["K8S_LOCAL_APISERVER"] = fmt.Sprintf("https://%s:%s", localAPIServer.Host, localAPIServer.Port)
	data.Data["HTTP_PROXY"] = bootstrapResult.Infra.Proxy.HTTPProxy
	data.Data["HTTPS_PROXY"] = bootstrapResult.Infra.Proxy.HTTPSProxy
	data.Data["NO_PROXY"] = bootstrapResult.Infra.Proxy.NoProxy

	data.Data["TokenMinterImage"] = os.Getenv("TOKEN_MINTER_IMAGE")
	// TOKEN_AUDIENCE is used by token-minter to identify the audience for the service account token which is verified by the apiserver
	data.Data["TokenAudience"] = os.Getenv("TOKEN_AUDIENCE")
	data.Data["MTU"] = c.MTU
	data.Data["RoutableMTU"] = nil

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
	data.Data["ManagementClusterName"] = client.ManagementClusterName
	data.Data["HostedClusterNamespace"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.Namespace
	data.Data["OvnkubeMasterReplicas"] = len(bootstrapResult.OVN.MasterAddresses)
	data.Data["ClusterID"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.ClusterID
	data.Data["ClusterIDLabel"] = ClusterIDLabel
	data.Data["OVNDbServiceType"] = corev1.ServiceTypeClusterIP
	data.Data["OVNSbDbRouteHost"] = nil
	data.Data["OVN_SB_NODE_PORT"] = nil
	pubStrategy := bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.ServicePublishingStrategy
	if pubStrategy != nil && pubStrategy.Type == hyperv1.Route {
		if pubStrategy.Route != nil && pubStrategy.Route.Hostname != "" {
			data.Data["OVNSbDbRouteHost"] = pubStrategy.Route.Hostname
		}
	} else if pubStrategy != nil && pubStrategy.Type == hyperv1.NodePort {
		data.Data["OVNDbServiceType"] = corev1.ServiceTypeNodePort
		if pubStrategy.NodePort != nil && pubStrategy.NodePort.Port > 0 {
			data.Data["OVN_SB_NODE_PORT"] = pubStrategy.NodePort.Port
		}
	}
	data.Data["OVN_NB_DB_ENDPOINT"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.OVNSbDbEndpoint
	data.Data["OVN_SB_DB_ENDPOINT"] = bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.OVNSbDbEndpoint

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
		if bootstrapResult.OVN.ExistingIPsecDaemonset != nil {
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
	} else {
		data.Data["IsSNO"] = false
	}

	manifestDirs := make([]string, 0, 2)
	manifestDirs = append(manifestDirs, filepath.Join(manifestDir, "network/ovn-kubernetes/common"))
	if bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.Enabled {
		manifestDirs = append(manifestDirs, filepath.Join(manifestDir, "network/ovn-kubernetes/managed"))
	} else {
		manifestDirs = append(manifestDirs, filepath.Join(manifestDir, "network/ovn-kubernetes/self-hosted"))
	}

	manifests, err := render.RenderDirs(manifestDirs, &data)
	if err != nil {
		return nil, progressing, errors.Wrap(err, "failed to render manifests")
	}
	objs = append(objs, manifests...)

	nodeMode := bootstrapResult.OVN.OVNKubernetesConfig.NodeMode
	if nodeMode == OVN_NODE_MODE_DPU_HOST {
		data.Data["OVN_NODE_MODE"] = nodeMode
		manifests, err = render.RenderTemplate(filepath.Join(manifestDir, "network/ovn-kubernetes/ovnkube-node.yaml"), &data)
		if err != nil {
			return nil, progressing, errors.Wrap(err, "failed to render manifests")
		}
		objs = append(objs, manifests...)
	} else if nodeMode == OVN_NODE_MODE_DPU {
		// "OVN_NODE_MODE" not set when render.RenderDir() called above,
		// so render just the error-cni.yaml with "OVN_NODE_MODE" set.
		data.Data["OVN_NODE_MODE"] = nodeMode
		manifests, err = render.RenderTemplate(filepath.Join(manifestDir, "network/ovn-kubernetes/error-cni.yaml"), &data)
		if err != nil {
			return nil, progressing, errors.Wrap(err, "failed to render manifests")
		}
		objs = append(objs, manifests...)

		// Run KubeProxy on DPU
		// DPU_DEV_PREVIEW
		// Node Mode is currently configured via a stand-alone configMap and stored
		// in bootstrapResult. Once out of DevPreview, CNO API will be expanded to
		// include Node Mode and it will be stored in conf (operv1.NetworkSpec) and
		// defaultDeployKubeProxy() will have access and this can be removed.
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
	updateNode, updateMaster := shouldUpdateOVNKonIPFamilyChange(bootstrapResult.OVN.ExistingNodeDaemonset, bootstrapResult.OVN.ExistingMasterDaemonset, ipFamilyMode)
	// annotate the daemonset and the daemonset template with the current IP family mode,
	// this triggers a daemonset restart if there are changes.
	err = setOVNDaemonsetAnnotation(objs, names.NetworkIPFamilyModeAnnotation, ipFamilyMode)
	if err != nil {
		return nil, progressing, errors.Wrapf(err, "failed to set IP family %s annotation on daemonsets", ipFamilyMode)
	}

	// don't process upgrades if we are handling a dual-stack conversion.
	if updateMaster && updateNode {
		updateNode, updateMaster = shouldUpdateOVNKonUpgrade(bootstrapResult.OVN.ExistingNodeDaemonset, bootstrapResult.OVN.ExistingMasterDaemonset, os.Getenv("RELEASE_VERSION"))
	}

	renderPrePull := false
	if updateNode {
		updateNode, renderPrePull = shouldUpdateOVNKonPrepull(bootstrapResult.OVN.ExistingNodeDaemonset, bootstrapResult.OVN.PrePullerDaemonset, os.Getenv("RELEASE_VERSION"))
	}

	// If we need to delay master or node daemonset rollout, then we'll tag that daemonset with "create-only"
	if !updateMaster {
		ds := bootstrapResult.OVN.ExistingMasterDaemonset
		k8s.UpdateObjByGroupKindName(objs, "apps", "DaemonSet", ds.Namespace, ds.Name, func(o *uns.Unstructured) {
			anno := o.GetAnnotations()
			if anno == nil {
				anno = map[string]string{}
			}
			anno[names.CreateOnlyAnnotation] = "true"
			o.SetAnnotations(anno)
		})
	}
	if !updateNode {
		ds := bootstrapResult.OVN.ExistingNodeDaemonset
		k8s.UpdateObjByGroupKindName(objs, "apps", "DaemonSet", ds.Namespace, ds.Name, func(o *uns.Unstructured) {
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
		objs = k8s.RemoveObjByGroupKindName(objs, "apps", "DaemonSet", "openshift-ovn-kubernetes", "ovnkube-upgrades-prepuller")
	}

	if bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.Enabled && bootstrapResult.OVN.OVNKubernetesConfig.HyperShiftConfig.OVNSbDbEndpoint == "" {
		k8s.UpdateObjByGroupKindName(objs, "apps", "DaemonSet", "openshift-ovn-kubernetes", "ovnkube-node", func(o *uns.Unstructured) {
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

func bootstrapOVNHyperShiftConfig(hc *HyperShiftConfig, kubeClient cnoclient.Client) (*bootstrap.OVNHyperShiftBootstrapResult, error) {
	ovnHypershiftResult := &bootstrap.OVNHyperShiftBootstrapResult{
		Enabled:   hc.Enabled,
		Namespace: hc.Namespace,
	}

	if !hc.Enabled {
		return ovnHypershiftResult, nil
	}

	hcp := &hyperv1.HostedControlPlane{ObjectMeta: metav1.ObjectMeta{Name: hc.Name}}
	err := kubeClient.ClientFor(cnoclient.ManagementClusterName).CRClient().Get(context.TODO(), types.NamespacedName{Namespace: hc.Namespace, Name: hc.Name}, hcp)
	if err != nil {
		if apierrors.IsNotFound(err) {
			klog.Infof("Did not find hosted control plane")
		} else {
			return nil, fmt.Errorf("Could not get hosted control plane: %v", err)
		}
	}

	ovnHypershiftResult.ClusterID = hcp.Spec.ClusterID
	switch hcp.Spec.ControllerAvailabilityPolicy {
	case hyperv1.HighlyAvailable:
		ovnHypershiftResult.ControlPlaneReplicas = 3
	default:
		ovnHypershiftResult.ControlPlaneReplicas = 1
	}
	for _, svc := range hcp.Spec.Services {
		// TODO: instead of the hardcoded string use ServiceType hyperv1.OVNSbDb once the API is updated
		if svc.Service == "OVNSbDb" {
			ovnHypershiftResult.ServicePublishingStrategy = &svc.ServicePublishingStrategy
		}
	}
	if ovnHypershiftResult.ServicePublishingStrategy == nil {
		klog.Warningf("service publishing strategy for OVN southbound database does not exist in hyperv1.HostedControlPlane %s/%s. Defaulting to route", hc.Name, hc.Namespace)
		ovnHypershiftResult.ServicePublishingStrategy = &hyperv1.ServicePublishingStrategy{
			Type: hyperv1.Route,
		}
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
			clusterClient := kubeClient.ClientFor(client.ManagementClusterName)
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
					ovnHypershiftResult.OVNSbDbEndpoint = fmt.Sprintf("ssl:%s:%s", route.Status.Ingress[0].Host, OVN_SB_ROUTE_PORT)
				} else if route.Spec.Host != "" {
					ovnHypershiftResult.OVNSbDbEndpoint = fmt.Sprintf("ssl:%s:%s", route.Spec.Host, OVN_SB_ROUTE_PORT)
				}
				klog.Infof("Overriding OVN configuration route to %s", ovnHypershiftResult.OVNSbDbEndpoint)
			}
		}
	case hyperv1.NodePort:
		{
			svc := &corev1.Service{}
			err = kubeClient.Default().CRClient().Get(context.TODO(), types.NamespacedName{Namespace: hc.Namespace, Name: "ovnkube-master-external"}, svc)
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
				ovnHypershiftResult.OVNSbDbEndpoint = fmt.Sprintf("ssl:%s:%d", ovnHypershiftResult.ServicePublishingStrategy.NodePort.Address, sbDbPort)
			} else {
				klog.Infof("Node port not defined for ovnkube-master service")
			}
		}
	default:
		return nil, fmt.Errorf("unsupported service publishing strategy type: %s", ovnHypershiftResult.ServicePublishingStrategy.Type)
	}
	return ovnHypershiftResult, nil
}

// bootstrapOVNConfig returns the value of mode found in the openshift-ovn-kubernetes/dpu-mode-config configMap
// if it exists, otherwise returns default configuration for OCP clusters using OVN-Kubernetes
func bootstrapOVNConfig(conf *operv1.Network, kubeClient cnoclient.Client, hc *HyperShiftConfig) (*bootstrap.OVNConfigBoostrapResult, error) {
	ovnConfigResult := &bootstrap.OVNConfigBoostrapResult{
		NodeMode: OVN_NODE_MODE_FULL,
	}
	if conf.Spec.DefaultNetwork.OVNKubernetesConfig.GatewayConfig == nil {
		bootstrapOVNGatewayConfig(conf, kubeClient.ClientFor("").CRClient())
	}

	var err error
	ovnConfigResult.HyperShiftConfig, err = bootstrapOVNHyperShiftConfig(hc, kubeClient)
	if err != nil {
		return ovnConfigResult, err
	}

	cm := &corev1.ConfigMap{}
	dmc := types.NamespacedName{Namespace: "openshift-network-operator", Name: "dpu-mode-config"}
	err = kubeClient.ClientFor("").CRClient().Get(context.TODO(), dmc, cm)

	if err != nil {
		if apierrors.IsNotFound(err) {
			klog.Infof("Did not find dpu-mode-config")
		} else {
			return nil, fmt.Errorf("Could not determine Node Mode: %w", err)
		}
	} else {
		nodeModeOverride := cm.Data["mode"]
		if nodeModeOverride != OVN_NODE_MODE_DPU_HOST && nodeModeOverride != OVN_NODE_MODE_DPU {
			klog.Warningf("dpu-mode-config does not match %q or %q, is: %q. Using OVN configuration: %+v",
				OVN_NODE_MODE_DPU_HOST, OVN_NODE_MODE_DPU, nodeModeOverride, ovnConfigResult)
			return ovnConfigResult, nil
		}
		ovnConfigResult.NodeMode = nodeModeOverride
		klog.Infof("Overriding OVN configuration to %+v", ovnConfigResult)
	}
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
	if pn.HybridOverlayConfig == nil && nn.HybridOverlayConfig != nil {
		errs = append(errs, errors.Errorf("cannot start a hybrid overlay network after install time"))
	}
	if pn.HybridOverlayConfig != nil {
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

func getMasterAddresses(kubeClient crclient.Client, controlPlaneReplicaCount int, hypershift bool) ([]string, error) {
	var heartBeat int
	masterNodeList := &corev1.NodeList{}
	ovnMasterAddresses := make([]string, 0, controlPlaneReplicaCount)

	if hypershift {
		for i := 0; i < controlPlaneReplicaCount; i++ {
			ovnMasterAddresses = append(ovnMasterAddresses, fmt.Sprintf("ovnkube-master-%d.ovnkube-master-internal.%s.svc.cluster.local", i, os.Getenv("HOSTED_CLUSTER_NAMESPACE")))
		}
	} else {
		err := wait.PollImmediate(OVN_MASTER_DISCOVERY_POLL*time.Second, time.Duration(OVN_MASTER_DISCOVERY_TIMEOUT)*time.Second, func() (bool, error) {
			matchingLabels := &crclient.MatchingLabels{"node-role.kubernetes.io/master": ""}
			if err := kubeClient.List(context.TODO(), masterNodeList, matchingLabels); err != nil {
				return false, err
			}
			if len(masterNodeList.Items) != 0 && controlPlaneReplicaCount == len(masterNodeList.Items) {
				return true, nil
			}

			heartBeat++
			if heartBeat%3 == 0 {
				klog.V(2).Infof("Waiting to complete OVN bootstrap: found (%d) master nodes out of (%d) expected: timing out in %d seconds",
					len(masterNodeList.Items), controlPlaneReplicaCount, OVN_MASTER_DISCOVERY_TIMEOUT-OVN_MASTER_DISCOVERY_POLL*heartBeat)
			}
			return false, nil
		})
		if wait.ErrWaitTimeout == err {
			klog.Warningf("Timeout exceeded while bootstraping OVN, expected amount of control plane nodes (%v) do not match found (%v): %s, continuing deployment with found replicas", controlPlaneReplicaCount, len(masterNodeList.Items))
			// On certain types of cluster this condition will never be met (assisted installer, for example)
			// As to not hold the reconciliation loop for too long on such clusters: dynamically modify the timeout
			// to a shorter and shorter value. Never reach 0 however as that will result in a `PollInfinity`.
			// Right now we'll do:
			// - First reconciliation 250 second timeout
			// - Second reconciliation 130 second timeout
			// - >= Third reconciliation 10 second timeout
			if OVN_MASTER_DISCOVERY_TIMEOUT-OVN_MASTER_DISCOVERY_BACKOFF > 0 {
				OVN_MASTER_DISCOVERY_TIMEOUT = OVN_MASTER_DISCOVERY_TIMEOUT - OVN_MASTER_DISCOVERY_BACKOFF
			}
		} else if err != nil {
			return nil, fmt.Errorf("Unable to bootstrap OVN, err: %v", err)
		}

		for _, masterNode := range masterNodeList.Items {
			var ip string
			for _, address := range masterNode.Status.Addresses {
				if address.Type == corev1.NodeInternalIP {
					ip = address.Address
					break
				}
			}
			if ip == "" {
				return nil, fmt.Errorf("No InternalIP found on master node '%s'", masterNode.Name)
			}
			ovnMasterAddresses = append(ovnMasterAddresses, ip)
		}
	}
	return ovnMasterAddresses, nil
}

func bootstrapOVN(conf *operv1.Network, kubeClient cnoclient.Client) (*bootstrap.OVNBootstrapResult, error) {
	clusterConfig := &corev1.ConfigMap{}
	clusterConfigLookup := types.NamespacedName{Name: CLUSTER_CONFIG_NAME, Namespace: CLUSTER_CONFIG_NAMESPACE}

	if err := kubeClient.ClientFor("").CRClient().Get(context.TODO(), clusterConfigLookup, clusterConfig); err != nil {
		return nil, fmt.Errorf("Unable to bootstrap OVN, unable to retrieve cluster config: %s", err)
	}

	rcD := replicaCountDecoder{}
	if err := yaml.Unmarshal([]byte(clusterConfig.Data["install-config"]), &rcD); err != nil {
		return nil, fmt.Errorf("Unable to bootstrap OVN, unable to unmarshal install-config: %s", err)
	}

	hc := NewHyperShiftConfig()
	ovnConfigResult, err := bootstrapOVNConfig(conf, kubeClient, hc)
	if err != nil {
		return nil, fmt.Errorf("Unable to bootstrap OVN config, err: %v", err)
	}

	var controlPlaneReplicaCount int
	if hc.Enabled {
		controlPlaneReplicaCount = ovnConfigResult.HyperShiftConfig.ControlPlaneReplicas
	} else {
		controlPlaneReplicaCount, _ = strconv.Atoi(rcD.ControlPlane.Replicas)
	}

	ovnMasterAddresses, err := getMasterAddresses(kubeClient.ClientFor("").CRClient(), controlPlaneReplicaCount, hc.Enabled)
	if err != nil {
		return nil, err
	}

	sort.Strings(ovnMasterAddresses)

	// clusterInitiator is used to avoid a split-brain scenario for the OVN NB/SB DBs. We want to consistently initialize
	// any OVN cluster which is bootstrapped here, to the same initiator (should it still exists), hence we annotate the
	// network.operator.openshift.io CRD with this information and always try to re-use the same member for the OVN RAFT
	// cluster initialization
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

	// Retrieve existing daemonsets - used for deciding if upgrades should happen
	masterDS := &appsv1.DaemonSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "DaemonSet",
			APIVersion: appsv1.SchemeGroupVersion.String(),
		},
	}
	nsn := types.NamespacedName{Namespace: "openshift-ovn-kubernetes", Name: "ovnkube-master"}
	if err := kubeClient.ClientFor("").CRClient().Get(context.TODO(), nsn, masterDS); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("Failed to retrieve existing master DaemonSet: %w", err)
		} else {
			masterDS = nil
		}
	}

	nodeDS := &appsv1.DaemonSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "DaemonSet",
			APIVersion: appsv1.SchemeGroupVersion.String(),
		},
	}
	nsn = types.NamespacedName{Namespace: "openshift-ovn-kubernetes", Name: "ovnkube-node"}
	if err := kubeClient.ClientFor("").CRClient().Get(context.TODO(), nsn, nodeDS); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("Failed to retrieve existing node DaemonSet: %w", err)
		} else {
			nodeDS = nil
		}
	}

	prePullerDS := &appsv1.DaemonSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "DaemonSet",
			APIVersion: appsv1.SchemeGroupVersion.String(),
		},
	}
	nsn = types.NamespacedName{Namespace: "openshift-ovn-kubernetes", Name: "ovnkube-upgrades-prepuller"}
	if err := kubeClient.ClientFor("").CRClient().Get(context.TODO(), nsn, prePullerDS); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("Failed to retrieve existing prepuller DaemonSet: %w", err)
		} else {
			prePullerDS = nil
		}
	}

	ipsecDS := &appsv1.DaemonSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "DaemonSet",
			APIVersion: appsv1.SchemeGroupVersion.String(),
		},
	}
	nsn = types.NamespacedName{Namespace: "openshift-ovn-kubernetes", Name: "ovn-ipsec"}
	if err := kubeClient.ClientFor("").CRClient().Get(context.TODO(), nsn, ipsecDS); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("Failed to retrieve existing ipsec DaemonSet: %w", err)
		} else {
			ipsecDS = nil
		}
	}

	res := bootstrap.OVNBootstrapResult{
		MasterAddresses:         ovnMasterAddresses,
		ClusterInitiator:        clusterInitiator,
		ExistingMasterDaemonset: masterDS,
		ExistingNodeDaemonset:   nodeDS,
		ExistingIPsecDaemonset:  ipsecDS,
		OVNKubernetesConfig:     ovnConfigResult,
		PrePullerDaemonset:      prePullerDS,
		FlowsConfig:             bootstrapFlowsConfig(kubeClient.ClientFor("").CRClient()),
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
func shouldUpdateOVNKonIPFamilyChange(existingNode, existingMaster *appsv1.DaemonSet, ipFamilyMode string) (updateNode, updateMaster bool) {
	// Fresh cluster - full steam ahead!
	if existingNode == nil || existingMaster == nil {
		return true, true
	}
	// check current daemonsets IP family mode
	nodeIPFamilyMode := existingNode.GetAnnotations()[names.NetworkIPFamilyModeAnnotation]
	masterIPFamilyMode := existingMaster.GetAnnotations()[names.NetworkIPFamilyModeAnnotation]
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
	if daemonSetProgressing(existingMaster, false) {
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
func shouldUpdateOVNKonPrepull(existingNode, prePuller *appsv1.DaemonSet, releaseVersion string) (updateNode, renderPrepull bool) {
	// Fresh cluster - full steam ahead! No need to wait for pre-puller.
	if existingNode == nil {
		klog.V(3).Infof("Fresh cluster, no need for prepuller")
		return true, false
	}

	// if node is already upgraded, then no need to pre-pull
	// Return true so that we reconcile any changes that somehow could have happened.
	existingNodeVersion := existingNode.GetAnnotations()["release.openshift.io/version"]
	if existingNodeVersion == releaseVersion {
		klog.V(3).Infof("OVN-Kubernetes node is already in the expected release.")
		return true, false
	}

	// at this point, we've determined we need an upgrade
	if prePuller == nil {
		klog.Infof("Rolling out the no-op prepuller daemonset...")
		return false, true
	}

	// If pre-puller just pulled a new upgrade image and then we
	// downgrade immediately, we might wanna make prepuller pull the downgrade image.
	existingPrePullerVersion := prePuller.GetAnnotations()["release.openshift.io/version"]
	if existingPrePullerVersion != releaseVersion {
		klog.Infof("Rendering prepuller daemonset to update its image...")
		return false, true
	}

	if daemonSetProgressing(prePuller, true) {
		klog.Infof("Waiting for ovnkube-upgrades-prepuller daemonset to finish pulling the image before updating node")
		return false, true
	}

	klog.Infof("OVN-Kube upgrades-prepuller daemonset rollout complete, now starting node rollouts")
	return true, false
}

// shouldUpdateOVNKonUpgrade determines if we should roll out changes to
// the master and node daemonsets on upgrades. We roll out nodes first,
// then masters. Downgrades, we do the opposite.
func shouldUpdateOVNKonUpgrade(existingNode, existingMaster *appsv1.DaemonSet, releaseVersion string) (updateNode, updateMaster bool) {
	// Fresh cluster - full steam ahead!
	if existingNode == nil || existingMaster == nil {
		return true, true
	}

	nodeVersion := existingNode.GetAnnotations()["release.openshift.io/version"]
	masterVersion := existingMaster.GetAnnotations()["release.openshift.io/version"]

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
		if daemonSetProgressing(existingNode, true) {
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
		if daemonSetProgressing(existingMaster, false) {
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

// setOVNDaemonsetAnnotation annotates the OVNkube master and node daemonset
// it also annotated the template with the provided key and value to force the rollout
func setOVNDaemonsetAnnotation(objs []*uns.Unstructured, key, value string) error {
	for _, obj := range objs {
		if obj.GetAPIVersion() == "apps/v1" && obj.GetKind() == "DaemonSet" &&
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
