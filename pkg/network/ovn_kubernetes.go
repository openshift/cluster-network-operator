package network

import (
	"context"
	"fmt"
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
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/render"
	"github.com/openshift/cluster-network-operator/pkg/util/k8s"
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	types "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	utilnet "k8s.io/utils/net"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

var OVN_MASTER_DISCOVERY_TIMEOUT = 250

// renderOVNKubernetes returns the manifests for the ovn-kubernetes.
// This creates
// - the openshift-ovn-kubernetes namespace
// - the ovn-config ConfigMap
// - the ovnkube-node daemonset
// - the ovnkube-master deployment
// and some other small things.
func renderOVNKubernetes(conf *operv1.NetworkSpec, bootstrapResult *bootstrap.BootstrapResult, manifestDir string) ([]*uns.Unstructured, error) {
	c := conf.DefaultNetwork.OVNKubernetesConfig

	objs := []*uns.Unstructured{}

	// render the manifests on disk
	data := render.MakeRenderData()
	data.Data["ReleaseVersion"] = os.Getenv("RELEASE_VERSION")
	data.Data["OvnImage"] = os.Getenv("OVN_IMAGE")
	data.Data["KubeRBACProxyImage"] = os.Getenv("KUBE_RBAC_PROXY_IMAGE")
	data.Data["KUBERNETES_SERVICE_HOST"] = os.Getenv("KUBERNETES_SERVICE_HOST")
	data.Data["KUBERNETES_SERVICE_PORT"] = os.Getenv("KUBERNETES_SERVICE_PORT")
	data.Data["K8S_APISERVER"] = fmt.Sprintf("https://%s:%s", os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT"))
	data.Data["MTU"] = c.MTU
	data.Data["GenevePort"] = c.GenevePort
	data.Data["CNIConfDir"] = pluginCNIConfDir(conf)
	data.Data["CNIBinDir"] = CNIBinDir
	data.Data["OVN_GATEWAY_MODE"] = bootstrapResult.OVN.GatewayMode
	data.Data["OVN_NB_PORT"] = OVN_NB_PORT
	data.Data["OVN_SB_PORT"] = OVN_SB_PORT
	data.Data["OVN_NB_RAFT_PORT"] = OVN_NB_RAFT_PORT
	data.Data["OVN_SB_RAFT_PORT"] = OVN_SB_RAFT_PORT
	data.Data["OVN_NB_RAFT_ELECTION_TIMER"] = os.Getenv("OVN_NB_RAFT_ELECTION_TIMER")
	data.Data["OVN_SB_RAFT_ELECTION_TIMER"] = os.Getenv("OVN_SB_RAFT_ELECTION_TIMER")
	data.Data["OVN_CONTROLLER_INACTIVITY_PROBE"] = os.Getenv("OVN_CONTROLLER_INACTIVITY_PROBE")
	data.Data["OVN_NB_DB_LIST"] = dbList(bootstrapResult.OVN.MasterIPs, OVN_NB_PORT)
	data.Data["OVN_SB_DB_LIST"] = dbList(bootstrapResult.OVN.MasterIPs, OVN_SB_PORT)
	data.Data["OVN_DB_CLUSTER_INITIATOR"] = bootstrapResult.OVN.ClusterInitiator
	data.Data["OVN_MIN_AVAILABLE"] = len(bootstrapResult.OVN.MasterIPs)/2 + 1
	data.Data["LISTEN_DUAL_STACK"] = listenDualStack(bootstrapResult.OVN.MasterIPs[0])
	data.Data["OVN_CERT_CN"] = OVN_CERT_CN
	data.Data["OVN_NORTHD_PROBE_INTERVAL"] = os.Getenv("OVN_NORTHD_PROBE_INTERVAL")
	data.Data["NetFlowCollectors"] = ""
	data.Data["SFlowCollectors"] = ""
	data.Data["IPFIXCollectors"] = ""
	data.Data["OVNPolicyAuditRateLimit"] = c.PolicyAuditConfig.RateLimit
	data.Data["OVNPolicyAuditMaxFileSize"] = c.PolicyAuditConfig.MaxFileSize
	data.Data["OVNPolicyAuditDestination"] = c.PolicyAuditConfig.Destination
	data.Data["OVNPolicyAuditSyslogFacility"] = c.PolicyAuditConfig.SyslogFacility
	if bootstrapResult.OVN.Platform == configv1.AzurePlatformType {
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

	if c.IPsecConfig != nil {
		data.Data["EnableIPsec"] = true
	} else {
		data.Data["EnableIPsec"] = false
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

	manifests, err := render.RenderDir(filepath.Join(manifestDir, "network/ovn-kubernetes"), &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render manifests")
	}
	objs = append(objs, manifests...)

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
		return nil, errors.Wrapf(err, "failed to set IP family %s annotation on daemonsets", ipFamilyMode)
	}

	// don't process upgrades if we are handling a dual-stack conversion.
	if updateMaster && updateNode {
		updateNode, updateMaster = shouldUpdateOVNKonUpgrade(bootstrapResult.OVN.ExistingNodeDaemonset, bootstrapResult.OVN.ExistingMasterDaemonset, os.Getenv("RELEASE_VERSION"))
	}

	// If we need to delay master or node daemonset rollout, then we'll replace the new one with the existing one
	if !updateMaster {
		us, err := k8s.ToUnstructured(bootstrapResult.OVN.ExistingMasterDaemonset)
		if err != nil {
			return nil, errors.Wrap(err, "failed to transmute existing master daemonset")
		}
		objs = k8s.ReplaceObj(objs, us)
	}
	if !updateNode {
		us, err := k8s.ToUnstructured(bootstrapResult.OVN.ExistingNodeDaemonset)
		if err != nil {
			return nil, errors.Wrap(err, "failed to transmute existing node daemonset")
		}
		objs = k8s.ReplaceObj(objs, us)
	}

	return objs, nil
}

// returns the value of mode found in the openshift-ovn-kubernetes/gateway-mode-config configMap
// if it exists, otherwise returns whatever the global OVN_GATEWAY_MODE is set to (shared)
func GetGatewayMode(kubeClient client.Client) (string, error) {
	defaultGatewayMode := "shared"
	cm := &corev1.ConfigMap{}
	nsn := types.NamespacedName{Namespace: "openshift-network-operator", Name: "gateway-mode-config"}
	err := kubeClient.Get(context.TODO(), nsn, cm)

	if err != nil {
		if apierrors.IsNotFound(err) {
			klog.Warningf("Did not find gateway-mode-config. Using default mode: \"%s\"", defaultGatewayMode)
			return defaultGatewayMode, nil
		} else {
			return "", fmt.Errorf("Could not determine gateway mode: %w", err)
		}
	}
	if cm.Data["mode"] != "shared" && cm.Data["mode"] != "local" {
		klog.Warningf("Ignoring gateway-mode-config %s. Does not match \"shared\" or \"local\"", cm.Data["mode"])
		return defaultGatewayMode, nil
	}
	klog.Infof("Overriding OVN gateway mode to %s", cm.Data["mode"])
	return cm.Data["mode"], nil
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
		if oc.MTU != nil && (*oc.MTU < 576 || *oc.MTU > 65536) {
			out = append(out, errors.Errorf("invalid MTU %d", *oc.MTU))
		}
		if oc.GenevePort != nil && (*oc.GenevePort < 1 || *oc.GenevePort > 65535) {
			out = append(out, errors.Errorf("invalid GenevePort %d", *oc.GenevePort))
		}
	}

	return out
}

// isOVNKubernetesChangeSafe currently returns an error if any changes to immutable
// fields are made.
// In the future, we may support rolling out MTU or other alterations.
func isOVNKubernetesChangeSafe(prev, next *operv1.NetworkSpec) []error {
	pn := prev.DefaultNetwork.OVNKubernetesConfig
	nn := next.DefaultNetwork.OVNKubernetesConfig
	errs := []error{}

	if !reflect.DeepEqual(pn.MTU, nn.MTU) {
		errs = append(errs, errors.Errorf("cannot change ovn-kubernetes MTU"))
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
	if pn.IPsecConfig == nil && nn.IPsecConfig != nil {
		errs = append(errs, errors.Errorf("cannot enable IPsec after install time"))
	}
	if pn.IPsecConfig != nil {
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

	const ipsecOverhead = 46 // Transport mode, AES-GCM
	const geneveOverhead = 100

	var encapOverhead uint32 = geneveOverhead
	if conf.DefaultNetwork.OVNKubernetesConfig.IPsecConfig != nil {
		encapOverhead += ipsecOverhead
	}

	sc := conf.DefaultNetwork.OVNKubernetesConfig
	// MTU  is currently the only field we pull from previous.
	// If MTU is not supplied, we infer it from the host on which CNO is running
	// (which may not be a node in the cluster).
	// However, this can never change, so we always prefer previous.

	// TODO - Need to check as IPsec will additional headers
	if sc.MTU == nil {
		var mtu uint32 = uint32(hostMTU) - encapOverhead
		if previous != nil && previous.DefaultNetwork.OVNKubernetesConfig != nil &&
			previous.DefaultNetwork.OVNKubernetesConfig.MTU != nil {
			mtu = *previous.DefaultNetwork.OVNKubernetesConfig.MTU
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

func bootstrapOVN(conf *operv1.Network, kubeClient client.Client) (*bootstrap.BootstrapResult, error) {
	clusterConfig := &corev1.ConfigMap{}
	clusterConfigLookup := types.NamespacedName{Name: CLUSTER_CONFIG_NAME, Namespace: CLUSTER_CONFIG_NAMESPACE}
	masterNodeList := &corev1.NodeList{}

	if err := kubeClient.Get(context.TODO(), clusterConfigLookup, clusterConfig); err != nil {
		return nil, fmt.Errorf("Unable to bootstrap OVN, unable to retrieve cluster config: %s", err)
	}

	rcD := replicaCountDecoder{}
	if err := yaml.Unmarshal([]byte(clusterConfig.Data["install-config"]), &rcD); err != nil {
		return nil, fmt.Errorf("Unable to bootstrap OVN, unable to unmarshal install-config: %s", err)
	}

	gatewayMode, err := GetGatewayMode(kubeClient)
	if err != nil {
		return nil, fmt.Errorf("Unable to bootstrap OVN, undetermined gateway-mode: '%s'", err)
	}

	controlPlaneReplicaCount, _ := strconv.Atoi(rcD.ControlPlane.Replicas)

	var heartBeat int

	err = wait.PollImmediate(OVN_MASTER_DISCOVERY_POLL*time.Second, time.Duration(OVN_MASTER_DISCOVERY_TIMEOUT)*time.Second, func() (bool, error) {
		matchingLabels := &client.MatchingLabels{"node-role.kubernetes.io/master": ""}
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

	ovnMasterIPs := make([]string, len(masterNodeList.Items))
	for i, masterNode := range masterNodeList.Items {
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
		ovnMasterIPs[i] = ip
	}

	sort.Strings(ovnMasterIPs)

	// clusterInitiator is used to avoid a split-brain scenario for the OVN NB/SB DBs. We want to consistently initialize
	// any OVN cluster which is bootstrapped here, to the same initiator (should it still exists), hence we annotate the
	// network.operator.openshift.io CRD with this information and always try to re-use the same member for the OVN RAFT
	// cluster initialization
	var clusterInitiator string
	currentAnnotation := conf.GetAnnotations()
	if cInitiator, ok := currentAnnotation[names.OVNRaftClusterInitiator]; ok && currentInitiatorExists(ovnMasterIPs, cInitiator) {
		clusterInitiator = cInitiator
	} else {
		clusterInitiator = ovnMasterIPs[0]
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
	masterDS := &appsv1.DaemonSet{}
	nsn := types.NamespacedName{Namespace: "openshift-ovn-kubernetes", Name: "ovnkube-master"}
	if err := kubeClient.Get(context.TODO(), nsn, masterDS); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("Failed to retrieve existing master DaemonSet: %w", err)
		} else {
			masterDS = nil
		}
	}

	nodeDS := &appsv1.DaemonSet{}
	nsn = types.NamespacedName{Namespace: "openshift-ovn-kubernetes", Name: "ovnkube-node"}
	if err := kubeClient.Get(context.TODO(), nsn, nodeDS); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("Failed to retrieve existing node DaemonSet: %w", err)
		} else {
			nodeDS = nil
		}
	}

	var platformType configv1.PlatformType

	infraConfig := &configv1.Infrastructure{}
	if err := kubeClient.Get(context.TODO(), types.NamespacedName{Name: "cluster"}, infraConfig); err != nil {
		return nil, fmt.Errorf("failed to get infrastructure 'config': %v", err)
	}

	if infraConfig.Status.PlatformStatus != nil {
		platformType = infraConfig.Status.PlatformStatus.Type
	}
	klog.V(2).Infof("Openshift-OVN: Bootstrap OVNN infraConfig Platform: %q", platformType)

	res := bootstrap.BootstrapResult{
		OVN: bootstrap.OVNBootstrapResult{
			MasterIPs:               ovnMasterIPs,
			ClusterInitiator:        clusterInitiator,
			ExistingMasterDaemonset: masterDS,
			ExistingNodeDaemonset:   nodeDS,
			GatewayMode:             gatewayMode,
			Platform:                platformType,
		},
	}
	return &res, nil
}

func currentInitiatorExists(ovnMasterIPs []string, configInitiator string) bool {
	for _, masterIP := range ovnMasterIPs {
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
