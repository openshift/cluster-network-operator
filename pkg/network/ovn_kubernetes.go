package network

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/ghodss/yaml"
	"github.com/pkg/errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	legacyconfigv1 "github.com/openshift/api/legacyconfig/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/render"
)

// renderOVNKubernetes returns the manifests for the ovn-kubernetes.
// This creates
// - the ClusterNetwork object
// - the ovn-kubernetes namespace
// - the ovn-kubeernetes setup
// - the ovnkube-node daemonset
// - the ovnkube-master deployment
// and some other small things.
func renderOVNKubernetes(conf *operv1.NetworkSpec, manifestDir string) ([]*uns.Unstructured, error) {
	c := conf.DefaultNetwork.OVNKubernetesConfig

	objs := []*uns.Unstructured{}

	// render the manifests on disk
	data := render.MakeRenderData()
	data.Data["OvnImage"] = os.Getenv("OVN_IMAGE")
	data.Data["HypershiftImage"] = os.Getenv("HYPERSHIFT_IMAGE")
	data.Data["K8S_APISERVER"] = fmt.Sprintf("https://%s:%s", os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT"))
	data.Data["MTU"] = c.MTU

	var ippools string
	for _, net := range conf.ClusterNetwork {
		if len(ippools) != 0 {
			ippools += ","
		}
		ippools += fmt.Sprintf("%s/%d", net.CIDR, net.HostPrefix)
	}
	data.Data["OVN_cidr"] = ippools
	data.Data["OVN_service_cidr"] = conf.ServiceNetwork

	nodeCfg, err := nodeOVNConfig(conf)
	if err != nil {
		return nil, errors.Wrap(err, "failed to build node config")
	}
	data.Data["NodeConfig"] = nodeCfg

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

	if reflect.DeepEqual(pn, nn) {
		return []error{}
	}
	return []error{errors.Errorf("cannot change ovn-kubernetes configuration")}
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

// nodeOVNConfig builds the (yaml text of) the NodeConfig object
// consumed by the sdn node process
func nodeOVNConfig(conf *operv1.NetworkSpec) (string, error) {
	c := conf.DefaultNetwork.OVNKubernetesConfig

	result := legacyconfigv1.NodeConfig{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "NodeConfig",
		},
		NetworkConfig: legacyconfigv1.NodeNetworkConfig{
			NetworkPluginName: networkPluginName(),
			MTU:               *c.MTU,
		},

		// Openshift-sdn calls the CRI endpoint directly; point it to crio
		KubeletArguments: legacyconfigv1.ExtendedArguments{
			"container-runtime":          {"remote"},
			"container-runtime-endpoint": {"/var/run/crio/crio.sock"},
		},
	}

	buf, err := yaml.Marshal(result)
	return string(buf), err
}
