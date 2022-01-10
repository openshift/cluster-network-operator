package network

import (
	"fmt"
	"testing"

	yaml "github.com/ghodss/yaml"

	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/apply"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"

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

	bootstrapResult := &bootstrap.BootstrapResult{
		Infra: bootstrap.InfraBootstrapResult{},
	}

	errs := validateOpenShiftSDN(config)
	g.Expect(errs).To(HaveLen(0))
	FillDefaults(config, nil)

	objs, err := renderOpenShiftSDN(config, bootstrapResult, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())

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
	truth := true

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
				Mode:           operv1.SDNModeNetworkPolicy,
				VXLANPort:      &p,
				MTU:            &m,
				EnableUnidling: &truth,
			},
		},
		DeployKubeProxy: &f,
		KubeProxyConfig: &operv1.ProxyConfig{
			BindAddress:    "0.0.0.0",
			ProxyArguments: map[string]operv1.ProxyArgumentList{},
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

	config.KubeProxyConfig = &operv1.ProxyConfig{
		ProxyArguments: map[string]operv1.ProxyArgumentList{
			"proxy-mode": {"userspace"},
		},
	}
	errExpect("invalid proxy-mode - when unidling is enabled")
}

func TestProxyArgs(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := OpenShiftSDNConfig.DeepCopy()
	config := &crd.Spec
	FillDefaults(config, nil)

	bootstrapResult := &bootstrap.BootstrapResult{
		Infra: bootstrap.InfraBootstrapResult{},
	}

	// iter through all objects, finding the kube-proxy config map
	getProxyConfigFile := func(objs []*uns.Unstructured) *uns.Unstructured {
		for _, obj := range objs {
			if obj.GetKind() == "ConfigMap" && obj.GetName() == "sdn-config" {
				val, ok, err := uns.NestedString(obj.Object, "data", "kube-proxy-config.yaml")
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
	objs, err := renderOpenShiftSDN(config, bootstrapResult, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	cfg := getProxyConfigFile(objs)

	val, _, _ := uns.NestedString(cfg.Object, "bindAddress")
	g.Expect(val).To(Equal("0.0.0.0"))
	val, _, _ = uns.NestedString(cfg.Object, "iptables", "syncPeriod")
	g.Expect(val).To(Equal("0s"))

	// set sync period
	config.KubeProxyConfig = &operv1.ProxyConfig{
		IptablesSyncPeriod: "10s",
		BindAddress:        "1.2.3.4",
		ProxyArguments:     map[string]operv1.ProxyArgumentList{},
	}
	objs, err = renderOpenShiftSDN(config, bootstrapResult, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	cfg = getProxyConfigFile(objs)
	val, _, _ = uns.NestedString(cfg.Object, "iptables", "syncPeriod")
	g.Expect(val).To(Equal("10s"))
	val, _, _ = uns.NestedString(cfg.Object, "bindAddress")
	g.Expect(val).To(Equal("1.2.3.4"))

	// set proxy args
	config.KubeProxyConfig.ProxyArguments = map[string]operv1.ProxyArgumentList{
		"cluster-cidr":       {"1.2.3.4/5"},
		"config-sync-period": {"1s", "2s"},
	}
	objs, err = renderOpenShiftSDN(config, bootstrapResult, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	cfg = getProxyConfigFile(objs)

	arg, _, _ := uns.NestedString(cfg.Object, "clusterCIDR")
	g.Expect(arg).To(Equal("1.2.3.4/5"))

	arg, _, _ = uns.NestedString(cfg.Object, "configSyncPeriod")
	g.Expect(arg).To(Equal("2s"))

	// Setting the proxy mode explicitly still gets unidling
	config.KubeProxyConfig.ProxyArguments = map[string]operv1.ProxyArgumentList{
		"proxy-mode": {"iptables"},
	}
	objs, err = renderOpenShiftSDN(config, bootstrapResult, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	cfg = getProxyConfigFile(objs)

	arg, _, _ = uns.NestedString(cfg.Object, "mode")
	g.Expect(arg).To(Equal("unidling+iptables"))

	// Disabling unidling doesn't add the fixup
	f := false
	config.DefaultNetwork.OpenShiftSDNConfig.EnableUnidling = &f
	objs, err = renderOpenShiftSDN(config, bootstrapResult, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	cfg = getProxyConfigFile(objs)

	arg, _, _ = uns.NestedString(cfg.Object, "mode")
	g.Expect(arg).To(Equal("iptables"))

	// Explicitly setting the metrics port to "9101" is allowed but does not affect
	// the actual configuration, which uses "29101". Other port values are not
	// allowed. (Even 29101!)
	config.KubeProxyConfig.ProxyArguments = map[string]operv1.ProxyArgumentList{
		"metrics-port": {"29101"},
	}
	errs := validateKubeProxy(config)
	g.Expect(errs).To(HaveLen(1))
	config.KubeProxyConfig.ProxyArguments = map[string]operv1.ProxyArgumentList{
		"metrics-port": {"9101"},
	}
	errs = validateKubeProxy(config)
	g.Expect(errs).To(HaveLen(0))
	// Validate that we don't allow the feature-gates to be set via user config
	config.KubeProxyConfig.ProxyArguments = map[string]operv1.ProxyArgumentList{
		"feature-gates": {"FGBar=true"},
	}
	errs = validateKubeProxy(config)
	g.Expect(errs).To(HaveLen(1))

	objs, err = renderOpenShiftSDN(config, bootstrapResult, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	cfg = getProxyConfigFile(objs)

	arg, _, _ = uns.NestedString(cfg.Object, "metricsBindAddress")
	g.Expect(arg).To(Equal("127.0.0.1:29101"))
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
	next.DefaultNetwork.OpenShiftSDNConfig.Mode = operv1.SDNModeMultitenant
	mtu := uint32(4000)
	next.DefaultNetwork.OpenShiftSDNConfig.MTU = &mtu
	f := false
	next.DefaultNetwork.OpenShiftSDNConfig.EnableUnidling = &f

	errs = isOpenShiftSDNChangeSafe(prev, next)
	g.Expect(errs).To(HaveLen(3))
	g.Expect(errs[0]).To(MatchError("cannot change openshift-sdn mode"))
	g.Expect(errs[1]).To(MatchError("cannot change openshift-sdn vxlanPort"))
	g.Expect(errs[2]).To(MatchError("cannot change openshift-sdn mtu without migration"))

	next.DefaultNetwork.OpenShiftSDNConfig.VXLANPort = prev.DefaultNetwork.OpenShiftSDNConfig.VXLANPort
	next.DefaultNetwork.OpenShiftSDNConfig.Mode = prev.DefaultNetwork.OpenShiftSDNConfig.Mode
	next.DefaultNetwork.OpenShiftSDNConfig.MTU = prev.DefaultNetwork.OpenShiftSDNConfig.MTU
	// mtu migration

	// valid mtu migration
	next.Migration = &operv1.NetworkMigration{
		MTU: &operv1.MTUMigration{
			Network: &operv1.MTUMigrationValues{
				From: prev.DefaultNetwork.OpenShiftSDNConfig.MTU,
				To:   ptrToUint32(1300),
			},
			Machine: &operv1.MTUMigrationValues{
				To: ptrToUint32(1500),
			},
		},
	}
	errs = isOpenShiftSDNChangeSafe(prev, next)
	g.Expect(errs).To(BeEmpty())

	// missing fields
	next.Migration.MTU.Network.From = nil
	errs = isOpenShiftSDNChangeSafe(prev, next)
	g.Expect(errs).To(HaveLen(1))
	g.Expect(errs[0]).To(MatchError("invalid Migration.MTU, at least one of the required fields is missing"))

	// invalid Migration.MTU.Network.From, not equal to previously applied MTU
	next.Migration.MTU.Network.From = ptrToUint32(*prev.DefaultNetwork.OpenShiftSDNConfig.MTU + 100)
	errs = isOpenShiftSDNChangeSafe(prev, next)
	g.Expect(errs).To(HaveLen(1))
	g.Expect(errs[0]).To(MatchError(fmt.Sprintf("invalid Migration.MTU.Network.From(%d) not equal to the currently applied MTU(%d)", *next.Migration.MTU.Network.From, *prev.DefaultNetwork.OpenShiftSDNConfig.MTU)))

	next.Migration.MTU.Network.From = prev.DefaultNetwork.OpenShiftSDNConfig.MTU

	// invalid Migration.MTU.Host.To, not big enough to accommodate next.Migration.MTU.Network.To with encap overhead
	next.Migration.MTU.Network.To = ptrToUint32(1500)
	errs = isOpenShiftSDNChangeSafe(prev, next)
	g.Expect(errs).To(HaveLen(1))
	g.Expect(errs[0]).To(MatchError(fmt.Sprintf("invalid Migration.MTU.Machine.To(%d), has to be at least %d", *next.Migration.MTU.Machine.To, *next.Migration.MTU.Network.To+50)))
}

func TestOpenShiftSDNMultitenant(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := OpenShiftSDNConfig.DeepCopy()
	config := &crd.Spec
	FillDefaults(config, nil)
	config.DefaultNetwork.OpenShiftSDNConfig.Mode = "Multitenant"

	bootstrapResult := &bootstrap.BootstrapResult{
		Infra: bootstrap.InfraBootstrapResult{},
	}

	objs, err := renderOpenShiftSDN(config, bootstrapResult, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())

	// the full list of namespaces with a netns
	netNS := []string{
		"openshift-dns",
		"openshift-ingress",
		"openshift-monitoring",
		"openshift-kube-apiserver",
		"openshift-kube-apiserver-operator",
		"openshift-apiserver",
		"kube-system",
		"openshift-operator-lifecycle-manager",
		"openshift-image-registry",
		"openshift-user-workload-monitoring",
		"openshift-etcd",
		"openshift-etcd-operator",
		"openshift-service-catalog-apiserver",
		"openshift-service-catalog-controller-manager",
		"openshift-template-service-broker",
		"openshift-ansible-service-broker",
		"openshift-authentication",
		"openshift-authentication-operator",
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

func TestOpenshiftSDNProxyConfig(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := OpenShiftSDNConfig.DeepCopy()
	config := &crd.Spec
	FillDefaults(config, nil)
	// hard-code the mtu in case we run on other kinds of nodes
	mtu := uint32(1450)
	config.DefaultNetwork.OpenShiftSDNConfig.MTU = &mtu

	// iter through all objects, finding the sdn config map
	getProxyConfig := func(objs []*uns.Unstructured) string {
		for _, obj := range objs {
			if obj.GetKind() == "ConfigMap" && obj.GetName() == "sdn-config" {
				val, ok, err := uns.NestedString(obj.Object, "data", "kube-proxy-config.yaml")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(ok).To(BeTrue())
				return val
			}
		}
		t.Fatal("failed to find sdn-config")
		return "" //unreachable
	}

	bootstrapResult := &bootstrap.BootstrapResult{
		Infra: bootstrap.InfraBootstrapResult{},
	}

	// test default rendering
	objs, err := renderOpenShiftSDN(config, bootstrapResult, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(getProxyConfig(objs)).To(MatchYAML(`
apiVersion: kubeproxy.config.k8s.io/v1alpha1
bindAddress: 0.0.0.0
bindAddressHardFail: false
clientConnection:
  acceptContentTypes: ""
  burst: 0
  contentType: ""
  kubeconfig: ""
  qps: 0
clusterCIDR: ""
configSyncPeriod: 0s
conntrack:
  maxPerCore: null
  min: null
  tcpCloseWaitTimeout: null
  tcpEstablishedTimeout: null
detectLocalMode: ""
enableProfiling: true
healthzBindAddress: 0.0.0.0:10256
hostnameOverride: ""
iptables:
  masqueradeAll: false
  masqueradeBit: 0
  minSyncPeriod: 0s
  syncPeriod: 0s
ipvs:
  excludeCIDRs: null
  minSyncPeriod: 0s
  scheduler: ""
  strictARP: false
  syncPeriod: 0s
  tcpFinTimeout: 0s
  tcpTimeout: 0s
  udpTimeout: 0s
kind: KubeProxyConfiguration
metricsBindAddress: 127.0.0.1:29101
mode: unidling+iptables
nodePortAddresses: null
oomScoreAdj: null
portRange: ""
showHiddenMetricsForVersion: ""
udpIdleTimeout: 0s
winkernel:
  enableDSR: false
  networkName: ""
  sourceVip: ""
`))

	// Disable unidling
	f := false
	config.DefaultNetwork.OpenShiftSDNConfig.EnableUnidling = &f
	objs, err = renderOpenShiftSDN(config, bootstrapResult, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(getProxyConfig(objs)).To(MatchYAML(`
apiVersion: kubeproxy.config.k8s.io/v1alpha1
bindAddress: 0.0.0.0
bindAddressHardFail: false
clientConnection:
  acceptContentTypes: ""
  burst: 0
  contentType: ""
  kubeconfig: ""
  qps: 0
clusterCIDR: ""
configSyncPeriod: 0s
conntrack:
  maxPerCore: null
  min: null
  tcpCloseWaitTimeout: null
  tcpEstablishedTimeout: null
detectLocalMode: ""
enableProfiling: true
healthzBindAddress: 0.0.0.0:10256
hostnameOverride: ""
iptables:
  masqueradeAll: false
  masqueradeBit: 0
  minSyncPeriod: 0s
  syncPeriod: 0s
ipvs:
  excludeCIDRs: null
  minSyncPeriod: 0s
  scheduler: ""
  strictARP: false
  syncPeriod: 0s
  tcpFinTimeout: 0s
  tcpTimeout: 0s
  udpTimeout: 0s
kind: KubeProxyConfiguration
metricsBindAddress: 127.0.0.1:29101
mode: iptables
nodePortAddresses: null
oomScoreAdj: null
portRange: ""
showHiddenMetricsForVersion: ""
udpIdleTimeout: 0s
winkernel:
  enableDSR: false
  networkName: ""
  sourceVip: ""
`))

}
