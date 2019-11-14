package network

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/pkg/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"github.com/openshift/cluster-network-operator/pkg/render"
	corev1 "k8s.io/api/core/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const OVN_NB_PORT = "9641"
const OVN_SB_PORT = "9642"
const OVN_NB_RAFT_PORT = "9643"
const OVN_SB_RAFT_PORT = "9644"

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
	data.Data["KUBERNETES_SERVICE_HOST"] = os.Getenv("KUBERNETES_SERVICE_HOST")
	data.Data["KUBERNETES_SERVICE_PORT"] = os.Getenv("KUBERNETES_SERVICE_PORT")
	data.Data["K8S_APISERVER"] = fmt.Sprintf("https://%s:%s", os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT"))
	data.Data["MTU"] = c.MTU
	data.Data["CNIConfDir"] = pluginCNIConfDir(conf)
	data.Data["CNIBinDir"] = CNIBinDir
	data.Data["OVN_NB_PORT"] = OVN_NB_PORT
	data.Data["OVN_SB_PORT"] = OVN_SB_PORT
	data.Data["OVN_NB_RAFT_PORT"] = OVN_NB_RAFT_PORT
	data.Data["OVN_SB_RAFT_PORT"] = OVN_SB_RAFT_PORT
	data.Data["OVN_NODES"] = strings.Join(bootstrapResult.OVN.OVNMasterNodes, " ")
	data.Data["OVN_NODE_IPS"] = strings.Join(bootstrapResult.OVN.OVNMasterNodeIPs, " ")

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
		data.Data["OVNHybridOverlayNetCIDR"] = c.HybridOverlayConfig.HybridClusterNetwork[0].CIDR
		data.Data["OVNHybridOverlayEnable"] = "true"
	} else {
		data.Data["OVNHybridOverlayNetCIDR"] = ""
		data.Data["OVNHybridOverlayEnable"] = ""
	}

	manifests, err := render.RenderDir(filepath.Join(manifestDir, "network/ovn-kubernetes"), &data)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render manifests")
	}

	objs = append(objs, manifests...)
	return objs, nil
}

// validateOVNKubernetes checks that the ovn-kubernetes specific configuration
// is basically sane.
func validateOVNKubernetes(conf *operv1.NetworkSpec) []error {
	out := []error{}

	if len(conf.ClusterNetwork) == 0 {
		out = append(out, errors.Errorf("ClusterNetworks cannot be empty"))
	}
	if len(conf.ServiceNetwork) != 1 {
		out = append(out, errors.Errorf("ServiceNetwork must have exactly 1 entry"))
	}

	oc := conf.DefaultNetwork.OVNKubernetesConfig
	if oc != nil {
		if oc.MTU != nil && (*oc.MTU < 576 || *oc.MTU > 65536) {
			out = append(out, errors.Errorf("invalid MTU %d", *oc.MTU))
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
	if pn.HybridOverlayConfig != nil {
		if !reflect.DeepEqual(pn.HybridOverlayConfig, nn.HybridOverlayConfig) {
			errs = append(errs, errors.Errorf("once set cannot change ovn-kubernetes Hybrid Overlay Config"))
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
	// If it's not supplied, we infer it from  the node on which we're running.
	// However, this can never change, so we always prefer previous.
	if sc.MTU == nil {
		var mtu uint32 = uint32(hostMTU) - 100 // 100 byte geneve header
		if previous != nil && previous.DefaultNetwork.OVNKubernetesConfig != nil {
			mtu = *previous.DefaultNetwork.OVNKubernetesConfig.MTU
		}
		sc.MTU = &mtu
	}
}

func networkPluginName() string {
	return "ovn-kubernetes"
}

type ovnMaster struct {
	Name string
	IP   string
}

// Implements sort.Interface
type ovnMasterSlice []*ovnMaster

func (p ovnMasterSlice) Len() int           { return len(p) }
func (p ovnMasterSlice) Less(i, j int) bool { return p[i].Name < p[j].Name }
func (p ovnMasterSlice) Swap(i, j int)      { p[i], p[j] = p[j], p[i] }

func boostrapOVN(kubeClient client.Client) (*bootstrap.BootstrapResult, error) {
	masterNodeList := &corev1.NodeList{}
	matchingLabels := &client.MatchingLabels{"node-role.kubernetes.io/master": ""}
	if err := kubeClient.List(context.TODO(), masterNodeList, matchingLabels); err != nil {
		return nil, err
	}

	if len(masterNodeList.Items) == 0 {
		return nil, fmt.Errorf("unable to bootstrap OVN, no master nodes found")
	}

	var ovnMasters ovnMasterSlice

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
		parsedIP := net.ParseIP(ip)
		if parsedIP == nil {
			return nil, fmt.Errorf("Failed to parse InternalIP '%s' found on master node '%s'", ip, masterNode.Name)
		}
		if parsedIP.To4() == nil {
			// IPv6, wrap address in brackets
			ip = fmt.Sprintf("[%s]", ip)
		}
		ovnMasters = append(ovnMasters, &ovnMaster{Name: masterNode.Name, IP: ip})
	}

	sort.Sort(ovnMasters)
	ovnMasterNodes := []string{}
	ovnMasterNodeIPs := []string{}
	for _, m := range ovnMasters {
		ovnMasterNodes = append(ovnMasterNodes, m.Name)
		ovnMasterNodeIPs = append(ovnMasterNodeIPs, m.IP)
	}

	res := bootstrap.BootstrapResult{
		OVN: bootstrap.OVNBootstrapResult{
			OVNMasterNodes:   ovnMasterNodes,
			OVNMasterNodeIPs: ovnMasterNodeIPs,
		},
	}
	return &res, nil
}
