package network

import (
	"testing"

	operv1 "github.com/openshift/api/operator/v1"
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
		ProxyArguments: map[string][]string{
			// string
			"proxy-mode": {"blah"},

			// duration
			"iptables-min-sync-period": {"2m"},

			// optional int
			"iptables-masquerade-bit": {"14"},

			// optional duration
			"conntrack-tcp-timeout-close-wait": {"10m"},
		},
	},
}

func TestKubeProxyConfig(t *testing.T) {
	g := NewGomegaWithT(t)

	errs := validateKubeProxy(&config)
	g.Expect(errs).To(HaveLen(0))

	cfg, err := kubeProxyConfiguration(&config, map[string][]string{
		// special address+port combo
		"metrics-bind-address": {"1.2.3.4"},
		"metrics-port":         {"999"},
	})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(cfg).To(MatchYAML(`apiVersion: kubeproxy.config.k8s.io/v1alpha1
bindAddress: 0.0.0.0
clientConnection:
  acceptContentTypes: ""
  burst: 0
  contentType: ""
  kubeconfig: ""
  qps: 0
clusterCIDR: 10.128.0.0/14
configSyncPeriod: 0s
conntrack:
  max: null
  maxPerCore: null
  min: null
  tcpCloseWaitTimeout: 10m0s
  tcpEstablishedTimeout: null
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
  syncPeriod: 0s
kind: KubeProxyConfiguration
metricsBindAddress: 1.2.3.4:999
mode: blah
nodePortAddresses: null
oomScoreAdj: null
portRange: ""
resourceContainer: ""
udpIdleTimeout: 0s
`))
}

func TestShouldDeployKubeProxy(t *testing.T) {
	g := NewGomegaWithT(t)

	c := &operv1.NetworkSpec{
		DefaultNetwork: operv1.DefaultNetworkDefinition{
			Type: operv1.NetworkTypeOpenShiftSDN,
		},
	}

	g.Expect(ShouldDeployKubeProxy(c)).To(BeFalse())

	c.DefaultNetwork.Type = operv1.NetworkTypeOVNKubernetes
	g.Expect(ShouldDeployKubeProxy(c)).To(BeFalse())

	c.DefaultNetwork.Type = operv1.NetworkTypeKuryr
	g.Expect(ShouldDeployKubeProxy(c)).To(BeFalse())

	c.DefaultNetwork.Type = "Flannel"
	g.Expect(ShouldDeployKubeProxy(c)).To(BeTrue())
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
			ProxyArguments: map[string][]string{
				"foo": {"bar"},
			},
		},
	}
	g.Expect(validateKubeProxy(c)).To(BeEmpty())

	// Break something
	c.KubeProxyConfig.BindAddress = "invalid"
	c.KubeProxyConfig.IptablesSyncPeriod = "asdf"
	c.KubeProxyConfig.ProxyArguments["healthz-port"] = []string{"9101"}
	c.KubeProxyConfig.ProxyArguments["metrics-port"] = []string{"10256"}
	g.Expect(validateKubeProxy(c)).To(HaveLen(4))
}

func TestFillKubeProxyDefaults(t *testing.T) {
	g := NewGomegaWithT(t)

	c := &operv1.NetworkSpec{
		ClusterNetwork: []operv1.ClusterNetworkEntry{
			{
				CIDR:       "192.168.0.0/14",
				HostPrefix: 23,
			},
		},
	}

	FillKubeProxyDefaults(c, nil)
	tt := true
	g.Expect(c).To(Equal(&operv1.NetworkSpec{
		ClusterNetwork: []operv1.ClusterNetworkEntry{
			{
				CIDR:       "192.168.0.0/14",
				HostPrefix: 23,
			},
		},
		DeployKubeProxy: &tt,
		KubeProxyConfig: &operv1.ProxyConfig{
			BindAddress: "0.0.0.0",
		},
	}))
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

	FillKubeProxyDefaults(c, nil)

	objs, err := RenderStandaloneKubeProxy(c, manifestDir)
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
clientConnection:
  acceptContentTypes: ""
  burst: 0
  contentType: ""
  kubeconfig: ""
  qps: 0
clusterCIDR: 192.168.0.0/14
configSyncPeriod: 0s
conntrack:
  max: null
  maxPerCore: null
  min: null
  tcpCloseWaitTimeout: null
  tcpEstablishedTimeout: null
enableProfiling: false
healthzBindAddress: 0.0.0.0:10256
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
  syncPeriod: 0s
kind: KubeProxyConfiguration
metricsBindAddress: 0.0.0.0:9101
mode: iptables
nodePortAddresses: null
oomScoreAdj: null
portRange: ""
resourceContainer: ""
udpIdleTimeout: 0s`))
		}
	}
	g.Expect(found).To(BeTrue())
}
