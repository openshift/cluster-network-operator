package k8s

import (
	"strings"
	"testing"

	operv1 "github.com/openshift/api/operator/v1"
)

func TestGenerateKubeProxyConfiguration(t *testing.T) {
	defaults := map[string]operv1.ProxyArgumentList{
		"bind-address":            {"0.0.0.0"},
		"metrics-bind-address":    {"0.0.0.0"},
		"metrics-port":            {"9102"},
		"proxy-mode":              {"iptables"},
		"iptables-masquerade-bit": {"0"},
	}

	tests := []struct {
		description string
		overrides   map[string]operv1.ProxyArgumentList
		output      string
		err         string
	}{
		{
			description: "no overrides",
			overrides:   nil,
			output: `
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
detectLocal:
  bridgeInterface: ""
  interfaceNamePrefix: ""
detectLocalMode: ""
enableProfiling: false
healthzBindAddress: ""
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
metricsBindAddress: 0.0.0.0:9102
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
`,
		},
		{
			description: "full overrides",
			overrides: map[string]operv1.ProxyArgumentList{
				"bind-address":            {"1.2.3.4"},
				"metrics-bind-address":    {"5.6.7.8"},
				"metrics-port":            {"9999"},
				"proxy-mode":              {"userspace"},
				"iptables-masquerade-bit": {"14"},
			},
			output: `
apiVersion: kubeproxy.config.k8s.io/v1alpha1
bindAddress: 1.2.3.4
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
metricsBindAddress: 5.6.7.8:9999
mode: userspace
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
`,
		},
		{
			description: "metrics-bind-address only",
			overrides: map[string]operv1.ProxyArgumentList{
				"metrics-bind-address": {"5.6.7.8"},
			},
			output: `
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
detectLocal:
  bridgeInterface: ""
  interfaceNamePrefix: ""
detectLocalMode: ""
enableProfiling: false
healthzBindAddress: ""
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
metricsBindAddress: 5.6.7.8:9102
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
`,
		},
		{
			description: "metrics-port only",
			overrides: map[string]operv1.ProxyArgumentList{
				"metrics-port": {"9999"},
			},
			output: `
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
detectLocal:
  bridgeInterface: ""
  interfaceNamePrefix: ""
detectLocalMode: ""
enableProfiling: false
healthzBindAddress: ""
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
metricsBindAddress: 0.0.0.0:9999
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
`,
		},
		{
			description: "misc",
			overrides: map[string]operv1.ProxyArgumentList{
				"masquerade-all":           {"true"},
				"iptables-min-sync-period": {"10s"},
				"ipvs-exclude-cidrs":       {"1.2.3.4/8,5.6.7.8/16"},
				"proxy-port-range":         {"1000+10"},
			},
			output: `
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
detectLocal:
  bridgeInterface: ""
  interfaceNamePrefix: ""
detectLocalMode: ""
enableProfiling: false
healthzBindAddress: ""
hostnameOverride: ""
iptables:
  masqueradeAll: true
  masqueradeBit: 0
  minSyncPeriod: 10s
  syncPeriod: 0s
ipvs:
  excludeCIDRs:
  - 1.2.3.4/8
  - 5.6.7.8/16
  minSyncPeriod: 0s
  scheduler: ""
  strictARP: false
  syncPeriod: 0s
  tcpFinTimeout: 0s
  tcpTimeout: 0s
  udpTimeout: 0s
kind: KubeProxyConfiguration
metricsBindAddress: 0.0.0.0:9102
mode: iptables
nodePortAddresses: null
oomScoreAdj: null
portRange: 1000+10
showHiddenMetricsForVersion: ""
udpIdleTimeout: 0s
winkernel:
  enableDSR: false
  forwardHealthCheckVip: false
  networkName: ""
  rootHnsEndpointName: ""
  sourceVip: ""
`,
		},
		{
			description: "bad address",
			overrides: map[string]operv1.ProxyArgumentList{
				"bind-address": {"foo"},
			},
			err: `invalid bind-address "foo"`,
		},
		{
			description: "bad port",
			overrides: map[string]operv1.ProxyArgumentList{
				"metrics-port": {"foo"},
			},
			err: `invalid metrics-port "foo"`,
		},
		{
			description: "bad cidr",
			overrides: map[string]operv1.ProxyArgumentList{
				"cluster-cidr": {"foo"},
			},
			err: `invalid cluster-cidr "foo"`,
		},
		{
			description: "bad int",
			overrides: map[string]operv1.ProxyArgumentList{
				"iptables-masquerade-bit": {"foo"},
			},
			err: `invalid iptables-masquerade-bit "foo"`,
		},
		{
			description: "bad bool",
			overrides: map[string]operv1.ProxyArgumentList{
				"masquerade-all": {"maybe"},
			},
			err: `invalid masquerade-all "maybe"`,
		},
		{
			description: "bad duration",
			overrides: map[string]operv1.ProxyArgumentList{
				"iptables-sync-period": {"foo"},
			},
			err: `invalid iptables-sync-period "foo"`,
		},
		{
			description: "bad port range",
			overrides: map[string]operv1.ProxyArgumentList{
				"proxy-port-range": {"foo"},
			},
			err: `invalid proxy-port-range "foo"`,
		},
		{
			description: "extra args",
			overrides: map[string]operv1.ProxyArgumentList{
				"masquerade-all":           {"true"},
				"iptables-min-sync-period": {"10s"},
				"ipvs-exclude-cidrs":       {"1.2.3.4/8,5.6.7.8/16"},
				"proxy-port-range":         {"1000+10"},
				"blah-blah-blah":           {"99"},
			},
			err: "unused arguments: blah-blah-blah",
		},
		{
			description: "deprecated args",
			overrides: map[string]operv1.ProxyArgumentList{
				"conntrack-max": {"100"},
			},
			err: "unused arguments: conntrack-max",
		},
		{
			description: "bad feature-gates syntax",
			overrides: map[string]operv1.ProxyArgumentList{
				"feature-gates": {"FG1=true,FG2=false,FG3=false=false"},
			},
			err: `invalid "FG1=true,FG2=false,FG3=false=false" (invalid value of FG3: false=false, err: strconv.ParseBool: parsing "false=false": invalid syntax)`,
		},
		{
			description: "bad feature-gates value",
			overrides: map[string]operv1.ProxyArgumentList{
				"feature-gates": {"FG1=foo,FG2=true"},
			},
			err: `invalid "FG1=foo,FG2=true" (invalid value of FG1: foo, err: strconv.ParseBool: parsing "foo": invalid syntax)`,
		},
	}

	for _, test := range tests {
		args := MergeKubeProxyArguments(defaults, test.overrides)
		config, err := GenerateKubeProxyConfiguration(args)
		if test.err == "" {
			if err != nil {
				t.Fatalf("unexpected error in %q: %v", test.description, err)
			}
			if config != test.output[1:] {
				t.Fatalf("mismatch in %q: expected\n%s\n\ngot:\n%s\n\n", test.description, test.output, config)
			}
		} else {
			if err == nil {
				t.Fatalf("unexpected non-error in %q: config: %v, args: %v", test.description, config, args)
			} else if !strings.Contains(err.Error(), test.err) {
				t.Fatalf("bad error in %q: expected %q, got: %v", test.description, test.err, err)
			}
		}
	}
}
