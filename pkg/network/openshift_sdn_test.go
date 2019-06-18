package network

import (
	"testing"

	yaml "github.com/ghodss/yaml"

	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/apply"

	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	. "github.com/onsi/gomega"
)

var OpenShiftSDNConfig = operv1.Network{
	Spec: operv1.NetworkSpec{
		ServiceNetwork: []string{"172.30.0.0/16"},
		ClusterNetwork: []operv1.ClusterNetworkEntry{
			{
				CIDR:       "10.128.0.0/15",
				HostPrefix: 23,
			},
			{
				CIDR:       "10.0.0.0/14",
				HostPrefix: 24,
			},
		},
		DefaultNetwork: operv1.DefaultNetworkDefinition{
			Type: operv1.NetworkTypeOpenShiftSDN,
			OpenShiftSDNConfig: &operv1.OpenShiftSDNConfig{
				Mode: operv1.SDNModeNetworkPolicy,
			},
		},
	},
}

var manifestDir = "../../bindata"

// TestRenderOpenShiftSDN has some simple rendering tests
func TestRenderOpenShiftSDN(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := OpenShiftSDNConfig.DeepCopy()
	config := &crd.Spec
	sdnConfig := config.DefaultNetwork.OpenShiftSDNConfig

	errs := validateOpenShiftSDN(config)
	g.Expect(errs).To(HaveLen(0))
	FillDefaults(config, nil)

	// Make sure the OVS daemonset isn't created
	truth := true
	sdnConfig.UseExternalOpenvswitch = &truth
	objs, err := renderOpenShiftSDN(config, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).NotTo(ContainElement(HaveKubernetesID("DaemonSet", "openshift-sdn", "ovs")))

	// enable openvswitch
	sdnConfig.UseExternalOpenvswitch = nil
	objs, err = renderOpenShiftSDN(config, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-sdn", "ovs")))

	// It's important that the namespace is first
	g.Expect(objs[0]).To(HaveKubernetesID("Namespace", "", "openshift-sdn"))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ClusterRole", "", "openshift-sdn")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ClusterRole", "", "openshift-sdn-controller")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ServiceAccount", "openshift-sdn", "sdn")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ServiceAccount", "openshift-sdn", "sdn-controller")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ClusterRoleBinding", "", "openshift-sdn")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-sdn", "sdn")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-sdn", "sdn-controller")))

	// No netnamespaces by default
	g.Expect(objs).NotTo(ContainElement(HaveKubernetesID("NetNamespace", "", "openshift-ingress")))

	// make sure all deployments are in the master
	for _, obj := range objs {
		if obj.GetKind() != "Deployment" {
			continue
		}

		sel, found, err := uns.NestedStringMap(obj.Object, "spec", "template", "spec", "nodeSelector")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(found).To(BeTrue())

		_, ok := sel["node-role.kubernetes.io/master"]
		g.Expect(ok).To(BeTrue())
	}

	// Make sure every obj is reasonable:
	// - it is supported
	// - it reconciles to itself (steady state)
	for _, obj := range objs {
		g.Expect(apply.IsObjectSupported(obj)).NotTo(HaveOccurred())
		cur := obj.DeepCopy()
		upd := obj.DeepCopy()

		err = apply.MergeObjectForUpdate(cur, upd)
		g.Expect(err).NotTo(HaveOccurred())

		tweakMetaForCompare(cur)
		g.Expect(cur).To(Equal(upd))
	}
}

func TestFillOpenShiftSDNDefaults(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := OpenShiftSDNConfig.DeepCopy()
	conf := &crd.Spec
	conf.DefaultNetwork.OpenShiftSDNConfig = nil

	// vars
	f := false
	p := uint32(4789)
	m := uint32(8950)

	expected := operv1.NetworkSpec{
		ServiceNetwork: []string{"172.30.0.0/16"},
		ClusterNetwork: []operv1.ClusterNetworkEntry{
			{
				CIDR:       "10.128.0.0/15",
				HostPrefix: 23,
			},
			{
				CIDR:       "10.0.0.0/14",
				HostPrefix: 24,
			},
		},
		DefaultNetwork: operv1.DefaultNetworkDefinition{
			Type: operv1.NetworkTypeOpenShiftSDN,
			OpenShiftSDNConfig: &operv1.OpenShiftSDNConfig{
				Mode:      operv1.SDNModeNetworkPolicy,
				VXLANPort: &p,
				MTU:       &m,
			},
		},
		DeployKubeProxy: &f,
		KubeProxyConfig: &operv1.ProxyConfig{
			BindAddress: "0.0.0.0",
			ProxyArguments: map[string][]string{
				"metrics-bind-address": {"0.0.0.0"},
				"metrics-port":         {"9101"},
			},
		},
	}

	fillOpenShiftSDNDefaults(conf, nil, 9000)

	g.Expect(conf).To(Equal(&expected))

}

func TestValidateOpenShiftSDN(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := OpenShiftSDNConfig.DeepCopy()
	config := &crd.Spec
	sdnConfig := config.DefaultNetwork.OpenShiftSDNConfig

	err := validateOpenShiftSDN(config)
	g.Expect(err).To(BeEmpty())
	FillDefaults(config, nil)

	errExpect := func(substr string) {
		t.Helper()
		g.Expect(validateOpenShiftSDN(config)).To(
			ContainElement(MatchError(
				ContainSubstring(substr))))
	}

	// set mtu to insanity
	mtu := uint32(70000)
	sdnConfig.MTU = &mtu
	errExpect("invalid MTU 70000")

	sdnConfig.Mode = "broken"
	errExpect("invalid openshift-sdn mode \"broken\"")

	port := uint32(66666)
	sdnConfig.VXLANPort = &port
	errExpect("invalid VXLANPort 66666")

	config.ClusterNetwork = nil
	errExpect("ClusterNetwork cannot be empty")
}

func TestProxyArgs(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := OpenShiftSDNConfig.DeepCopy()
	config := &crd.Spec
	FillDefaults(config, nil)

	// iter through all objects, finding the sdn config map
	getSdnConfigFile := func(objs []*uns.Unstructured) *uns.Unstructured {
		for _, obj := range objs {
			if obj.GetKind() == "ConfigMap" && obj.GetName() == "sdn-config" {
				val, ok, err := uns.NestedString(obj.Object, "data", "sdn-config.yaml")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(ok).To(BeTrue())

				out := uns.Unstructured{}
				err = yaml.Unmarshal([]byte(val), &out)
				g.Expect(err).NotTo(HaveOccurred())
				t.Logf("%+v", out)

				return &out
			}
		}
		t.Fatal("failed to find sdn-config")
		return nil //unreachable
	}

	// test default rendering
	objs, err := renderOpenShiftSDN(config, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	cfg := getSdnConfigFile(objs)

	val, _, _ := uns.NestedString(cfg.Object, "servingInfo", "bindAddress")
	g.Expect(val).To(Equal("0.0.0.0:10251"))
	val, _, _ = uns.NestedString(cfg.Object, "iptablesSyncPeriod")
	g.Expect(val).To(Equal(""))

	// set sync period
	config.KubeProxyConfig = &operv1.ProxyConfig{
		IptablesSyncPeriod: "10s",
		BindAddress:        "1.2.3.4",
	}
	objs, err = renderOpenShiftSDN(config, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	cfg = getSdnConfigFile(objs)
	g.Expect(cfg.Object).To(HaveKeyWithValue("iptablesSyncPeriod", "10s"))
	val, _, _ = uns.NestedString(cfg.Object, "servingInfo", "bindAddress")
	g.Expect(val).To(Equal("1.2.3.4:10251"))

	//set proxy args
	config.KubeProxyConfig.ProxyArguments = map[string][]string{
		"cluster-cidr":       {"1.2.3.4/5"},
		"config-sync-period": {"1s", "2s"},
	}
	objs, err = renderOpenShiftSDN(config, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	cfg = getSdnConfigFile(objs)

	arg, _, _ := uns.NestedStringSlice(cfg.Object, "proxyArguments", "cluster-cidr")
	g.Expect(arg).To(Equal([]string{"1.2.3.4/5"}))

	arg, _, _ = uns.NestedStringSlice(cfg.Object, "proxyArguments", "config-sync-period")
	g.Expect(arg).To(Equal([]string{"1s", "2s"}))

}

func TestOpenShiftSDNIsSafe(t *testing.T) {
	g := NewGomegaWithT(t)

	prev := OpenShiftSDNConfig.Spec.DeepCopy()
	FillDefaults(prev, nil)
	next := OpenShiftSDNConfig.Spec.DeepCopy()
	FillDefaults(next, nil)

	errs := isOpenShiftSDNChangeSafe(prev, next)
	g.Expect(errs).To(BeEmpty())

	// change the vxlan port
	p := uint32(99)
	next.DefaultNetwork.OpenShiftSDNConfig.VXLANPort = &p

	errs = isOpenShiftSDNChangeSafe(prev, next)
	g.Expect(errs).To(HaveLen(1))
	g.Expect(errs[0]).To(MatchError("cannot change openshift-sdn configuration"))
}

func TestOpenShiftSDNMultitenant(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := OpenShiftSDNConfig.DeepCopy()
	config := &crd.Spec
	FillDefaults(config, nil)
	config.DefaultNetwork.OpenShiftSDNConfig.Mode = "Multitenant"

	objs, err := renderOpenShiftSDN(config, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())

	// the full list of namespaces with a netns
	netNS := []string{
		"openshift-dns",
		"openshift-ingress",
		"openshift-monitoring",
		"openshift-kube-apiserver",
		"openshift-apiserver",
		"kube-system",
		"openshift-operator-lifecycle-manager",
		"openshift-image-registry",
	}

	for _, ns := range netNS {
		g.Expect(objs).To(ContainElement(HaveKubernetesID("NetNamespace", "", ns)))
	}
}

func TestClusterNetwork(t *testing.T) {
	g := NewGomegaWithT(t)

	copy := OpenShiftSDNConfig.DeepCopy()
	config := &copy.Spec
	FillDefaults(config, nil)
	// hard-code the mtu in case we run on other kinds of nodes
	mtu := uint32(1450)
	config.DefaultNetwork.OpenShiftSDNConfig.MTU = &mtu

	cn, err := clusterNetwork(config)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(cn).To(Equal(`apiVersion: network.openshift.io/v1
clusterNetworks:
- CIDR: 10.128.0.0/15
  hostSubnetLength: 9
- CIDR: 10.0.0.0/14
  hostSubnetLength: 8
hostsubnetlength: 9
kind: ClusterNetwork
metadata:
  creationTimestamp: null
  name: default
mtu: 1450
network: 10.128.0.0/15
pluginName: redhat/openshift-ovs-networkpolicy
serviceNetwork: 172.30.0.0/16
vxlanPort: 4789
`))
}

func TestOpenshiftNodeConfig(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := OpenShiftSDNConfig.DeepCopy()
	config := &crd.Spec
	FillDefaults(config, nil)
	// hard-code the mtu in case we run on other kinds of nodes
	mtu := uint32(1450)
	config.DefaultNetwork.OpenShiftSDNConfig.MTU = &mtu

	cfg, err := nodeConfig(config)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(cfg).To(Equal(`allowDisabledDocker: false
apiVersion: v1
authConfig:
  authenticationCacheSize: 0
  authenticationCacheTTL: ""
  authorizationCacheSize: 0
  authorizationCacheTTL: ""
dnsBindAddress: ""
dnsDomain: ""
dnsIP: ""
dnsNameservers: null
dnsRecursiveResolvConf: ""
dockerConfig:
  dockerShimRootDirectory: ""
  dockerShimSocket: ""
  execHandlerName: ""
enableUnidling: null
imageConfig:
  format: ""
  latest: false
iptablesSyncPeriod: ""
kind: NodeConfig
kubeletArguments:
  container-runtime:
  - remote
  container-runtime-endpoint:
  - unix:///var/run/crio/crio.sock
masterClientConnectionOverrides: null
masterKubeConfig: ""
networkConfig:
  mtu: 1450
  networkPluginName: redhat/openshift-ovs-networkpolicy
nodeIP: ""
nodeName: ""
podManifestConfig: null
proxyArguments:
  metrics-bind-address:
  - 0.0.0.0
  metrics-port:
  - "9101"
servingInfo:
  bindAddress: 0.0.0.0:10251
  bindNetwork: ""
  certFile: ""
  clientCA: /var/run/secrets/kubernetes.io/serviceaccount/ca.crt
  keyFile: ""
  namedCertificates: null
volumeConfig:
  localQuota:
    perFSGroup: null
volumeDirectory: ""
`))
}
