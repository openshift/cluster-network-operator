package network

import (
	"testing"

	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	. "github.com/onsi/gomega"
)

var config = operv1.NetworkSpec{
	ClusterNetwork: []operv1.ClusterNetworkEntry{
		{
			CIDR:       "10.128.0.0/14",
			HostPrefix: 23,
		},
	},
	KubeProxyConfig: &operv1.ProxyConfig{
		BindAddress:        "0.0.0.0",
		IptablesSyncPeriod: "1m",
		ProxyArguments: map[string]operv1.ProxyArgumentList{
			// string
			"proxy-mode": {"blah"},

			// duration
			"iptables-min-sync-period": {"2m"},

			// optional int
			"iptables-masquerade-bit": {"14"},

			// optional duration
			"conntrack-tcp-timeout-close-wait": {"10m"},

			// This will be overridden
			"conntrack-max-per-core": {"5"},
		},
	},
}

var configIPv6 = operv1.NetworkSpec{
	ClusterNetwork: []operv1.ClusterNetworkEntry{
		{
			CIDR:       "fd00:1234::/48",
			HostPrefix: 64,
		},
	},
	KubeProxyConfig: &operv1.ProxyConfig{
		BindAddress:        "::",
		IptablesSyncPeriod: "1m",
		ProxyArguments: map[string]operv1.ProxyArgumentList{
			// string
			"proxy-mode": {"blah"},

			// duration
			"iptables-min-sync-period": {"2m"},

			// optional int
			"iptables-masquerade-bit": {"14"},

			// optional duration
			"conntrack-tcp-timeout-close-wait": {"10m"},

			// This will be overridden
			"conntrack-max-per-core": {"5"},
		},
	},
}

func TestKubeProxyConfig(t *testing.T) {
	g := NewGomegaWithT(t)

	errs := validateKubeProxy(&config)
	g.Expect(errs).To(HaveLen(0))

	cfg, err := kubeProxyConfiguration(map[string]operv1.ProxyArgumentList{
		// special address+port combo
		"metrics-bind-address":   {"1.2.3.4"},
		"metrics-port":           {"999"},
		"conntrack-max-per-core": {"10"},
	},
		&config,
		map[string]operv1.ProxyArgumentList{
			"conntrack-max-per-core": {"15"},
		})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(cfg).To(MatchYAML(`apiVersion: kubeproxy.config.k8s.io/v1alpha1
bindAddress: "0.0.0.0"
bindAddressHardFail: false
clientConnection:
  acceptContentTypes: ""
  burst: 0
  contentType: ""
  kubeconfig: ""
  qps: 0
clusterCIDR: 10.128.0.0/14
configSyncPeriod: 0s
conntrack:
  maxPerCore: 15
  min: null
  tcpCloseWaitTimeout: 10m0s
  tcpEstablishedTimeout: null
detectLocal:
  bridgeInterface: ""
  interfaceNamePrefix: ""
detectLocalMode: ""
enableProfiling: false
healthzBindAddress: ""
hostnameOverride: ""
iptables:
  masqueradeAll: false
  masqueradeBit: 14
  minSyncPeriod: 2m0s
  syncPeriod: 1m0s
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
metricsBindAddress: 1.2.3.4:999
mode: blah
nodePortAddresses: null
oomScoreAdj: null
portRange: ""
showHiddenMetricsForVersion: ""
udpIdleTimeout: 0s
winkernel:
  enableDSR: false
  forwardHealthCheckVip: false
  networkName: ""
  rootHnsEndpointName: ""
  sourceVip: ""
`))
}

func TestKubeProxyIPv6Config(t *testing.T) {
	g := NewGomegaWithT(t)

	errs := validateKubeProxy(&configIPv6)
	g.Expect(errs).To(HaveLen(0))

	cfg, err := kubeProxyConfiguration(
		map[string]operv1.ProxyArgumentList{
			// special address+port combo
			"metrics-bind-address":   {"fd00:1234::4"},
			"metrics-port":           {"51999"},
			"conntrack-max-per-core": {"10"},
		},
		&configIPv6,
		map[string]operv1.ProxyArgumentList{
			"conntrack-max-per-core": {"15"},
		})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(cfg).To(MatchYAML(`apiVersion: kubeproxy.config.k8s.io/v1alpha1
bindAddress: "::"
bindAddressHardFail: false
clientConnection:
  acceptContentTypes: ""
  burst: 0
  contentType: ""
  kubeconfig: ""
  qps: 0
clusterCIDR: fd00:1234::/48
configSyncPeriod: 0s
conntrack:
  maxPerCore: 15
  min: null
  tcpCloseWaitTimeout: 10m0s
  tcpEstablishedTimeout: null
detectLocal:
  bridgeInterface: ""
  interfaceNamePrefix: ""
detectLocalMode: ""
enableProfiling: false
healthzBindAddress: ""
hostnameOverride: ""
iptables:
  masqueradeAll: false
  masqueradeBit: 14
  minSyncPeriod: 2m0s
  syncPeriod: 1m0s
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
metricsBindAddress: '[fd00:1234::4]:51999'
mode: blah
nodePortAddresses: null
oomScoreAdj: null
portRange: ""
showHiddenMetricsForVersion: ""
udpIdleTimeout: 0s
winkernel:
  enableDSR: false
  forwardHealthCheckVip: false
  networkName: ""
  rootHnsEndpointName: ""
  sourceVip: ""
`))
}

func TestShouldDeployKubeProxy(t *testing.T) {
	g := NewGomegaWithT(t)

	c := &operv1.NetworkSpec{
		DefaultNetwork: operv1.DefaultNetworkDefinition{
			Type: operv1.NetworkTypeOpenShiftSDN,
		},
	}

	g.Expect(acceptsKubeProxyConfig(c)).To(BeTrue())
	g.Expect(defaultDeployKubeProxy(c)).To(BeFalse())

	c.DefaultNetwork.Type = operv1.NetworkTypeOVNKubernetes
	g.Expect(acceptsKubeProxyConfig(c)).To(BeFalse())
	g.Expect(defaultDeployKubeProxy(c)).To(BeFalse())

	c.DefaultNetwork.Type = operv1.NetworkTypeKuryr
	g.Expect(acceptsKubeProxyConfig(c)).To(BeFalse())
	g.Expect(defaultDeployKubeProxy(c)).To(BeFalse())

	c.DefaultNetwork.Type = "Flannel"
	g.Expect(acceptsKubeProxyConfig(c)).To(BeTrue())
	g.Expect(defaultDeployKubeProxy(c)).To(BeTrue())
}

func TestValidateKubeProxy(t *testing.T) {
	g := NewGomegaWithT(t)

	// Check that the empty case validates
	c := &operv1.NetworkSpec{}
	g.Expect(validateKubeProxy(c)).To(BeEmpty())

	// Check that the default openshift-sdn config validates
	g.Expect(validateKubeProxy(&OpenShiftSDNConfig.Spec)).To(BeEmpty())

	// Check that some reasonable values validate
	c = &operv1.NetworkSpec{
		KubeProxyConfig: &operv1.ProxyConfig{
			BindAddress:        "1.2.3.4",
			IptablesSyncPeriod: "30s",
			ProxyArguments: map[string]operv1.ProxyArgumentList{
				"foo": {"bar"},
			},
		},
	}
	g.Expect(validateKubeProxy(c)).To(BeEmpty())

	// Break something
	c.KubeProxyConfig.BindAddress = "invalid"
	c.KubeProxyConfig.IptablesSyncPeriod = "asdf"
	c.KubeProxyConfig.ProxyArguments["healthz-port"] = []string{"9102"}
	c.KubeProxyConfig.ProxyArguments["metrics-port"] = []string{"10255"}
	c.KubeProxyConfig.ProxyArguments["feature-gates"] = []string{"FGFoo=bar,FGBaz=bah"}
	g.Expect(validateKubeProxy(c)).To(HaveLen(5))
}

func TestFillKubeProxyDefaults(t *testing.T) {
	g := NewGomegaWithT(t)

	trueVar := true
	falseVar := false

	testcases := []struct {
		in  *operv1.NetworkSpec
		out *operv1.NetworkSpec
	}{
		// no bind address and cluster CIDR is IPv4
		{
			in: &operv1.NetworkSpec{
				ClusterNetwork: []operv1.ClusterNetworkEntry{
					{
						CIDR:       "192.168.0.0/14",
						HostPrefix: 23,
					},
				},
				DeployKubeProxy: &trueVar,
			},
			out: &operv1.NetworkSpec{
				ClusterNetwork: []operv1.ClusterNetworkEntry{
					{
						CIDR:       "192.168.0.0/14",
						HostPrefix: 23,
					},
				},
				DeployKubeProxy: &trueVar,
				KubeProxyConfig: &operv1.ProxyConfig{
					BindAddress: "0.0.0.0",
				},
			},
		},
		// no bind address and cluster CIDR is IPv6
		{
			in: &operv1.NetworkSpec{
				ClusterNetwork: []operv1.ClusterNetworkEntry{
					{
						CIDR:       "fd00:1234::/64",
						HostPrefix: 23,
					},
				},
				DeployKubeProxy: &trueVar,
			},
			out: &operv1.NetworkSpec{
				ClusterNetwork: []operv1.ClusterNetworkEntry{
					{
						CIDR:       "fd00:1234::/64",
						HostPrefix: 23,
					},
				},
				DeployKubeProxy: &trueVar,
				KubeProxyConfig: &operv1.ProxyConfig{
					BindAddress: "::",
				},
			},
		},
		// no bind address and no deploy kube-proxy
		{
			in: &operv1.NetworkSpec{
				ClusterNetwork: []operv1.ClusterNetworkEntry{
					{
						CIDR:       "fd00:1234::/64",
						HostPrefix: 23,
					},
				},
				DeployKubeProxy: &falseVar,
			},
			out: &operv1.NetworkSpec{
				ClusterNetwork: []operv1.ClusterNetworkEntry{
					{
						CIDR:       "fd00:1234::/64",
						HostPrefix: 23,
					},
				},
				DeployKubeProxy: &falseVar,
			},
		},
	}
	for _, tc := range testcases {
		fillKubeProxyDefaults(tc.in, nil)
		g.Expect(tc.in).To(Equal(tc.out))
	}
}

var FakeKubeProxyBootstrapResult = bootstrap.BootstrapResult{
	OVN: bootstrap.OVNBootstrapResult{
		OVNKubernetesConfig: &bootstrap.OVNConfigBoostrapResult{
			NodeMode: "full",
		},
	},
}

func TestRenderKubeProxy(t *testing.T) {
	g := NewGomegaWithT(t)

	c := &operv1.NetworkSpec{
		ClusterNetwork: []operv1.ClusterNetworkEntry{
			{
				CIDR:       "192.168.0.0/14",
				HostPrefix: 23,
			},
		},
		DefaultNetwork: operv1.DefaultNetworkDefinition{Type: "Flannel"},
		KubeProxyConfig: &operv1.ProxyConfig{
			IptablesSyncPeriod: "42s",
		},
	}

	fillKubeProxyDefaults(c, nil)

	objs, err := renderStandaloneKubeProxy(c, &FakeKubeProxyBootstrapResult, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(objs).To(HaveLen(10))

	// Make sure the arguments to kube-proxy are reasonable
	found := false
	for _, obj := range objs {
		if obj.GetAPIVersion() == "v1" && obj.GetKind() == "ConfigMap" && obj.GetName() == "proxy-config" {
			if found == true {
				t.Fatal("Two kube-proxy configmaps!?")
			}
			found = true

			val, ok, err := uns.NestedString(obj.Object, "data", "kube-proxy-config.yaml")
			g.Expect(ok).To(BeTrue())
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(val).To(MatchYAML(`
apiVersion: kubeproxy.config.k8s.io/v1alpha1
bindAddress: 0.0.0.0
bindAddressHardFail: false
clientConnection:
  acceptContentTypes: ""
  burst: 0
  contentType: ""
  kubeconfig: ""
  qps: 0
clusterCIDR: 192.168.0.0/14
configSyncPeriod: 0s
conntrack:
  maxPerCore: null
  min: null
  tcpCloseWaitTimeout: null
  tcpEstablishedTimeout: null
detectLocal:
  bridgeInterface: ""
  interfaceNamePrefix: ""
detectLocalMode: ""
enableProfiling: false
healthzBindAddress: 0.0.0.0:10255
hostnameOverride: ""
iptables:
  masqueradeAll: false
  masqueradeBit: null
  minSyncPeriod: 0s
  syncPeriod: 42s
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
metricsBindAddress: 0.0.0.0:29102
mode: iptables
nodePortAddresses: null
oomScoreAdj: null
portRange: ""
showHiddenMetricsForVersion: ""
udpIdleTimeout: 0s
winkernel:
  enableDSR: false
  forwardHealthCheckVip: false
  networkName: ""
  rootHnsEndpointName: ""
  sourceVip: ""
`))
		}
	}
	g.Expect(found).To(BeTrue())
}
