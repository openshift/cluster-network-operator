package network

import (
	"testing"

	operv1 "github.com/openshift/api/operator/v1"

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

			// special address+port combo
			"metrics-bind-address": {"1.2.3.4"},
			"metrics-port":         {"999"},

			// optional int
			"iptables-masquerade-bit": {"14"},

			// optional duration
			"conntrack-tcp-timeout-close-wait": {"10m"},
		},
	},
}

func TestRenderKubeProxy(t *testing.T) {
	g := NewGomegaWithT(t)

	errs := validateKubeProxy(&config)
	g.Expect(errs).To(HaveLen(0))

	cfg, err := kubeProxyConfiguration(&config, nil)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(cfg).To(Equal(`apiVersion: kubeproxy.config.k8s.io/v1alpha1
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
