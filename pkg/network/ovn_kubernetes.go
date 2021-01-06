package network

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	yaml "github.com/ghodss/yaml"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"github.com/openshift/cluster-network-operator/pkg/render"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
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

const OVN_MASTER_DISCOVERY_TIMEOUT = 280
const OVN_MASTER_DISCOVERY_POLL = 5

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
	data.Data["OVN_NB_PORT"] = OVN_NB_PORT
	data.Data["OVN_SB_PORT"] = OVN_SB_PORT
	data.Data["OVN_NB_RAFT_PORT"] = OVN_NB_RAFT_PORT
	data.Data["OVN_SB_RAFT_PORT"] = OVN_SB_RAFT_PORT
	data.Data["OVN_NB_RAFT_ELECTION_TIMER"] = os.Getenv("OVN_NB_RAFT_ELECTION_TIMER")
	data.Data["OVN_SB_RAFT_ELECTION_TIMER"] = os.Getenv("OVN_SB_RAFT_ELECTION_TIMER")
	data.Data["OVN_CONTROLLER_INACTIVITY_PROBE"] = os.Getenv("OVN_CONTROLLER_INACTIVITY_PROBE")
	data.Data["OVN_NB_DB_LIST"] = dbList(bootstrapResult.OVN.MasterIPs, OVN_NB_PORT)
	data.Data["OVN_SB_DB_LIST"] = dbList(bootstrapResult.OVN.MasterIPs, OVN_SB_PORT)
	data.Data["OVN_MASTER_IP"] = bootstrapResult.OVN.MasterIPs[0]
	data.Data["OVN_MIN_AVAILABLE"] = len(bootstrapResult.OVN.MasterIPs)/2 + 1
	data.Data["LISTEN_DUAL_STACK"] = listenDualStack(bootstrapResult.OVN.MasterIPs[0])
	data.Data["OVN_CERT_CN"] = OVN_CERT_CN
	data.Data["OVN_NORTHD_PROBE_INTERVAL"] = os.Getenv("OVN_NORTHD_PROBE_INTERVAL")
	data.Data["ACLLOGGING"] = c.AclLogging
	data.Data["ACLLOGGINGRATELIMIT"] = c.AclLoggingRateLimit

	var ippools string
	for _, net := range conf.ClusterNetwork {
		if len(ippools) != 0 {
			ippools += ","
		}
		ippools += fmt.Sprintf("%s/%d", net.CIDR, net.HostPrefix)
	}
	data.Data["OVN_cidr"] = ippools

	var svcpools string
	for _, net := range conf.ServiceNetwork {
		if len(svcpools) != 0 {
			svcpools += ","
		}
		svcpools += fmt.Sprintf("%s", net)
	}
	data.Data["OVN_service_cidr"] = svcpools

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

	manifests, err := render.RenderDir(filepath.Join(manifestDir, "network/ovn-kubernetes"), &data)

	if err != nil {
		return nil, errors.Wrap(err, "failed to render manifests")
	}

	objs = append(objs, manifests...)

	if c.IPsecConfig != nil {
		// Only render ipsec manifest if ipsec has been enabled at cluster
		// installation time. We will never have to delete the ipsec pod
		// because it cannot be disabled at runtime
		//
		// We must render these manifests after ovn-kubernetes manifests
		// as they create the openshift-ovn-kubernetes namespace
		ipsecManifests, err := render.RenderDir(filepath.Join(manifestDir, "network/ovn-kubernetes-ipsec"), &data)
		if err != nil {
			return nil, errors.Wrap(err, "failed to render ipsec manifest")
		}
		objs = append(objs, ipsecManifests...)
	}

	return objs, nil
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

// isOVNKubernetesChangeSafe currently returns an error if any changes are made.
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
	// This might change 
	if !reflect.DeepEqual(pn.AclLogging, nn.AclLogging) { 
		errs = append(errs, errors.Errorf("cannot toggle ACL logging after install time"))
	}

	return errs
}

func fillOVNKubernetesDefaults(conf, previous *operv1.NetworkSpec, hostMTU int) {

	if conf.DefaultNetwork.OVNKubernetesConfig == nil {
		conf.DefaultNetwork.OVNKubernetesConfig = &operv1.OVNKubernetesConfig{}
	}

	sc := conf.DefaultNetwork.OVNKubernetesConfig
	// MTU  is currently the only field we pull from previous.
	// If MTU is not supplied, we infer it from the host on which CNO is running
	// (which may not be a node in the cluster).
	// However, this can never change, so we always prefer previous.

	// TODO - Need to check as IPsec will additional headers
	if sc.MTU == nil {
		var mtu uint32 = uint32(hostMTU) - 100 // 100 byte geneve header
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
}

func networkPluginName() string {
	return "ovn-kubernetes"
}

type replicaCountDecoder struct {
	ControlPlane struct {
		Replicas string `json:"replicas"`
	} `json:"controlPlane"`
}

func bootstrapOVN(kubeClient client.Client) (*bootstrap.BootstrapResult, error) {
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

	controlPlaneReplicaCount, _ := strconv.Atoi(rcD.ControlPlane.Replicas)

	var heartBeat int

	err := wait.PollImmediate(OVN_MASTER_DISCOVERY_POLL*time.Second, OVN_MASTER_DISCOVERY_TIMEOUT*time.Second, func() (bool, error) {
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
	if err != nil {
		return nil, fmt.Errorf("Unable to bootstrap OVN, expected amount of control plane nodes (%v) do not match found (%v): %s", controlPlaneReplicaCount, len(masterNodeList.Items), err)
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

	res := bootstrap.BootstrapResult{
		OVN: bootstrap.OVNBootstrapResult{
			MasterIPs: ovnMasterIPs,
		},
	}
	return &res, nil
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
