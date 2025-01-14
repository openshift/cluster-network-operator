package network

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ghodss/yaml"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/assert"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	kapierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	cnofake "github.com/openshift/cluster-network-operator/pkg/client/fake"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/util"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
)

//nolint:errcheck
func init() {
	operv1.AddToScheme(scheme.Scheme)
	appsv1.AddToScheme(scheme.Scheme)
}

var OVNKubernetesConfig = operv1.Network{
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
			Type: operv1.NetworkTypeOVNKubernetes,
			OVNKubernetesConfig: &operv1.OVNKubernetesConfig{
				GenevePort: ptrToUint32(8061),
			},
		},
	},
}

var manifestDirOvn = "../../bindata"

// TestRenderOVNKubernetes has some simple rendering tests
func TestRenderOVNKubernetes(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := OVNKubernetesConfig.DeepCopy()
	config := &crd.Spec

	errs := validateOVNKubernetes(config)
	g.Expect(errs).To(HaveLen(0))
	fillDefaults(config, nil)

	bootstrapResult := fakeBootstrapResult()
	bootstrapResult.OVN = bootstrap.OVNBootstrapResult{
		MasterAddresses: []string{"1.2.3.4", "5.6.7.8", "9.10.11.12"},
		OVNKubernetesConfig: &bootstrap.OVNConfigBoostrapResult{
			DpuHostModeLabel:     OVN_NODE_SELECTOR_DEFAULT_DPU_HOST,
			DpuModeLabel:         OVN_NODE_SELECTOR_DEFAULT_DPU,
			SmartNicModeLabel:    OVN_NODE_SELECTOR_DEFAULT_SMART_NIC,
			MgmtPortResourceName: "",
			HyperShiftConfig: &bootstrap.OVNHyperShiftBootstrapResult{
				Enabled: false,
			},
		},
	}
	featureGatesCNO := featuregates.NewFeatureGate([]configv1.FeatureGateName{configv1.FeatureGateAdminNetworkPolicy}, []configv1.FeatureGateName{})
	fakeClient := cnofake.NewFakeClient()

	objs, _, err := renderOVNKubernetes(config, bootstrapResult, manifestDirOvn, fakeClient, featureGatesCNO)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-ovn-kubernetes", "ovnkube-node")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("Deployment", "openshift-ovn-kubernetes", "ovnkube-control-plane")))

	// It's important that the namespace is first
	g.Expect(objs[0]).To(HaveKubernetesID("Namespace", "", "openshift-ovn-kubernetes"))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ClusterRole", "", "openshift-ovn-kubernetes-node-limited")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ClusterRole", "", "openshift-ovn-kubernetes-controller-limited")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ServiceAccount", "openshift-ovn-kubernetes", "ovn-kubernetes-node")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ServiceAccount", "openshift-ovn-kubernetes", "ovn-kubernetes-controller")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ClusterRoleBinding", "", "openshift-ovn-kubernetes-node-limited")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("Deployment", "openshift-ovn-kubernetes", "ovnkube-control-plane")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-ovn-kubernetes", "ovnkube-node")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ConfigMap", "openshift-ovn-kubernetes", "ovnkube-config")))

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
}

// TestRenderOVNKubernetesIPv6 tests IPv6 support
func TestRenderOVNKubernetesIPv6(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := OVNKubernetesConfig.DeepCopy()
	config := &crd.Spec

	errs := validateOVNKubernetes(config)
	g.Expect(errs).To(HaveLen(0))
	fillDefaults(config, nil)

	bootstrapResult := fakeBootstrapResult()
	bootstrapResult.OVN = bootstrap.OVNBootstrapResult{
		MasterAddresses: []string{"1.2.3.4", "5.6.7.8", "9.10.11.12"},
		OVNKubernetesConfig: &bootstrap.OVNConfigBoostrapResult{
			DpuHostModeLabel:     OVN_NODE_SELECTOR_DEFAULT_DPU_HOST,
			DpuModeLabel:         OVN_NODE_SELECTOR_DEFAULT_DPU,
			SmartNicModeLabel:    OVN_NODE_SELECTOR_DEFAULT_SMART_NIC,
			MgmtPortResourceName: "",
			HyperShiftConfig: &bootstrap.OVNHyperShiftBootstrapResult{
				Enabled: false,
			},
		},
	}
	featureGatesCNO := featuregates.NewFeatureGate([]configv1.FeatureGateName{configv1.FeatureGateAdminNetworkPolicy}, []configv1.FeatureGateName{})
	fakeClient := cnofake.NewFakeClient()
	objs, _, err := renderOVNKubernetes(config, bootstrapResult, manifestDirOvn, fakeClient, featureGatesCNO)
	g.Expect(err).NotTo(HaveOccurred())

	err = checkOVNKubernetesPostStart(objs)
	g.Expect(err).NotTo(HaveOccurred())

	bootstrapResult = fakeBootstrapResult()
	bootstrapResult.OVN = bootstrap.OVNBootstrapResult{
		MasterAddresses: []string{"fd01::1", "fd01::2", "fd01::3"},
		OVNKubernetesConfig: &bootstrap.OVNConfigBoostrapResult{
			DpuHostModeLabel:     OVN_NODE_SELECTOR_DEFAULT_DPU_HOST,
			DpuModeLabel:         OVN_NODE_SELECTOR_DEFAULT_DPU,
			SmartNicModeLabel:    OVN_NODE_SELECTOR_DEFAULT_SMART_NIC,
			MgmtPortResourceName: "",
			HyperShiftConfig: &bootstrap.OVNHyperShiftBootstrapResult{
				Enabled: false,
			},
		},
	}
	objs, _, err = renderOVNKubernetes(config, bootstrapResult, manifestDirOvn, fakeClient, featureGatesCNO)
	g.Expect(err).NotTo(HaveOccurred())

	err = checkOVNKubernetesPostStart(objs)
	g.Expect(err).NotTo(HaveOccurred())
}

func TestRenderedOVNKubernetesConfig(t *testing.T) {
	type testcase struct {
		desc                   string
		expected               string
		hybridOverlayConfig    *operv1.HybridOverlayConfig
		gatewayConfig          *operv1.GatewayConfig
		egressIPConfig         *operv1.EgressIPConfig
		masterIPs              []string
		v4InternalSubnet       string
		disableGRO             bool
		disableMultiNet        bool
		enableMultiNetPolicies bool
		enableAdminNetPolicies bool
	}
	testcases := []testcase{
		{
			desc: "default",
			expected: `
[default]
mtu="1500"
cluster-subnets="10.128.0.0/15/23,10.0.0.0/14/24"
encap-port="8061"
enable-lflow-cache=true
lflow-cache-limit-kb=1048576
enable-udp-aggregation=true

[kubernetes]
service-cidrs="172.30.0.0/16"
ovn-config-namespace="openshift-ovn-kubernetes"
apiserver="https://testing.test:8443"
host-network-namespace="openshift-host-network"
platform-type="GCP"
healthz-bind-address="0.0.0.0:10256"
dns-service-namespace="openshift-dns"
dns-service-name="dns-default"

[ovnkubernetesfeature]
enable-egress-ip=true
enable-egress-firewall=true
enable-egress-qos=true
enable-egress-service=true
egressip-node-healthcheck-port=9107
enable-multi-network=true

[gateway]
mode=shared
nodeport=true

[clustermanager]

[logging]
libovsdblogfile=/var/log/ovnkube/libovsdb.log
logfile-maxsize=100
logfile-maxbackups=5
logfile-maxage=0`,
			masterIPs: []string{"1.2.3.4", "2.3.4.5"},
		},
		{
			desc: "custom masquerade subnet",
			expected: `
[default]
mtu="1500"
cluster-subnets="10.128.0.0/15/23,10.0.0.0/14/24"
encap-port="8061"
enable-lflow-cache=true
lflow-cache-limit-kb=1048576
enable-udp-aggregation=true

[kubernetes]
service-cidrs="172.30.0.0/16"
ovn-config-namespace="openshift-ovn-kubernetes"
apiserver="https://testing.test:8443"
host-network-namespace="openshift-host-network"
platform-type="GCP"
healthz-bind-address="0.0.0.0:10256"
dns-service-namespace="openshift-dns"
dns-service-name="dns-default"

[ovnkubernetesfeature]
enable-egress-ip=true
enable-egress-firewall=true
enable-egress-qos=true
enable-egress-service=true
egressip-node-healthcheck-port=9107
enable-multi-network=true

[gateway]
mode=shared
nodeport=true
v4-masquerade-subnet="100.98.0.0/16"

[clustermanager]

[logging]
libovsdblogfile=/var/log/ovnkube/libovsdb.log
logfile-maxsize=100
logfile-maxbackups=5
logfile-maxage=0`,
			gatewayConfig: &operv1.GatewayConfig{
				IPv4: operv1.IPv4GatewayConfig{
					InternalMasqueradeSubnet: "100.98.0.0/16",
				},
			},

			masterIPs: []string{"1.2.3.4", "2.3.4.5"},
		},
		{
			desc: "HybridOverlay",
			expected: `
[default]
mtu="1500"
cluster-subnets="10.128.0.0/15/23,10.0.0.0/14/24"
encap-port="8061"
enable-lflow-cache=true
lflow-cache-limit-kb=1048576
enable-udp-aggregation=true

[kubernetes]
service-cidrs="172.30.0.0/16"
ovn-config-namespace="openshift-ovn-kubernetes"
apiserver="https://testing.test:8443"
host-network-namespace="openshift-host-network"
no-hostsubnet-nodes="kubernetes.io/os=windows"
platform-type="GCP"
healthz-bind-address="0.0.0.0:10256"
dns-service-namespace="openshift-dns"
dns-service-name="dns-default"

[ovnkubernetesfeature]
enable-egress-ip=true
enable-egress-firewall=true
enable-egress-qos=true
enable-egress-service=true
egressip-node-healthcheck-port=9107
enable-multi-network=true

[gateway]
mode=local
nodeport=true

[clustermanager]

[logging]
libovsdblogfile=/var/log/ovnkube/libovsdb.log
logfile-maxsize=100
logfile-maxbackups=5
logfile-maxage=0

[hybridoverlay]
enabled=true
cluster-subnets="10.132.0.0/14"`,
			hybridOverlayConfig: &operv1.HybridOverlayConfig{
				HybridClusterNetwork: []operv1.ClusterNetworkEntry{
					{CIDR: "10.132.0.0/14", HostPrefix: 23},
				},
			},
			gatewayConfig: &operv1.GatewayConfig{
				RoutingViaHost: true,
			},

			masterIPs: []string{"1.2.3.4", "2.3.4.5"},
		},
		{
			desc: "EgressIPConfig",
			expected: `
[default]
mtu="1500"
cluster-subnets="10.128.0.0/15/23,10.0.0.0/14/24"
encap-port="8061"
enable-lflow-cache=true
lflow-cache-limit-kb=1048576
enable-udp-aggregation=true

[kubernetes]
service-cidrs="172.30.0.0/16"
ovn-config-namespace="openshift-ovn-kubernetes"
apiserver="https://testing.test:8443"
host-network-namespace="openshift-host-network"
no-hostsubnet-nodes="kubernetes.io/os=windows"
platform-type="GCP"
healthz-bind-address="0.0.0.0:10256"
dns-service-namespace="openshift-dns"
dns-service-name="dns-default"

[ovnkubernetesfeature]
enable-egress-ip=true
enable-egress-firewall=true
enable-egress-qos=true
enable-egress-service=true
egressip-reachability-total-timeout=3
egressip-node-healthcheck-port=9107
enable-multi-network=true

[gateway]
mode=local
nodeport=true

[clustermanager]

[logging]
libovsdblogfile=/var/log/ovnkube/libovsdb.log
logfile-maxsize=100
logfile-maxbackups=5
logfile-maxage=0

[hybridoverlay]
enabled=true
cluster-subnets="10.132.0.0/14"`,
			hybridOverlayConfig: &operv1.HybridOverlayConfig{
				HybridClusterNetwork: []operv1.ClusterNetworkEntry{
					{CIDR: "10.132.0.0/14", HostPrefix: 23},
				},
			},
			gatewayConfig: &operv1.GatewayConfig{
				RoutingViaHost: true,
			},
			egressIPConfig: &operv1.EgressIPConfig{
				ReachabilityTotalTimeoutSeconds: ptrToUint32(3),
			},
			masterIPs: []string{"1.2.3.4", "2.3.4.5"},
		},
		{
			desc: "EgressIPConfig with disable reachability check",
			expected: `
[default]
mtu="1500"
cluster-subnets="10.128.0.0/15/23,10.0.0.0/14/24"
encap-port="8061"
enable-lflow-cache=true
lflow-cache-limit-kb=1048576
enable-udp-aggregation=true

[kubernetes]
service-cidrs="172.30.0.0/16"
ovn-config-namespace="openshift-ovn-kubernetes"
apiserver="https://testing.test:8443"
host-network-namespace="openshift-host-network"
no-hostsubnet-nodes="kubernetes.io/os=windows"
platform-type="GCP"
healthz-bind-address="0.0.0.0:10256"
dns-service-namespace="openshift-dns"
dns-service-name="dns-default"

[ovnkubernetesfeature]
enable-egress-ip=true
enable-egress-firewall=true
enable-egress-qos=true
enable-egress-service=true
egressip-reachability-total-timeout=0
egressip-node-healthcheck-port=9107
enable-multi-network=true

[gateway]
mode=local
nodeport=true

[clustermanager]

[logging]
libovsdblogfile=/var/log/ovnkube/libovsdb.log
logfile-maxsize=100
logfile-maxbackups=5
logfile-maxage=0

[hybridoverlay]
enabled=true
cluster-subnets="10.132.0.0/14"`,
			hybridOverlayConfig: &operv1.HybridOverlayConfig{
				HybridClusterNetwork: []operv1.ClusterNetworkEntry{
					{CIDR: "10.132.0.0/14", HostPrefix: 23},
				},
			},
			gatewayConfig: &operv1.GatewayConfig{
				RoutingViaHost: true,
			},
			egressIPConfig: &operv1.EgressIPConfig{
				ReachabilityTotalTimeoutSeconds: ptrToUint32(0),
			},
			masterIPs: []string{"1.2.3.4", "2.3.4.5"},
		},
		{
			desc: "HybridOverlay with custom VXLAN port",
			expected: `
[default]
mtu="1500"
cluster-subnets="10.128.0.0/15/23,10.0.0.0/14/24"
encap-port="8061"
enable-lflow-cache=true
lflow-cache-limit-kb=1048576
enable-udp-aggregation=true

[kubernetes]
service-cidrs="172.30.0.0/16"
ovn-config-namespace="openshift-ovn-kubernetes"
apiserver="https://testing.test:8443"
host-network-namespace="openshift-host-network"
no-hostsubnet-nodes="kubernetes.io/os=windows"
platform-type="GCP"
healthz-bind-address="0.0.0.0:10256"
dns-service-namespace="openshift-dns"
dns-service-name="dns-default"

[ovnkubernetesfeature]
enable-egress-ip=true
enable-egress-firewall=true
enable-egress-qos=true
enable-egress-service=true
egressip-node-healthcheck-port=9107
enable-multi-network=true

[gateway]
mode=local
nodeport=true

[clustermanager]

[logging]
libovsdblogfile=/var/log/ovnkube/libovsdb.log
logfile-maxsize=100
logfile-maxbackups=5
logfile-maxage=0

[hybridoverlay]
enabled=true
cluster-subnets="10.132.0.0/14"
hybrid-overlay-vxlan-port="9000"`,

			hybridOverlayConfig: &operv1.HybridOverlayConfig{
				HybridClusterNetwork: []operv1.ClusterNetworkEntry{
					{CIDR: "10.132.0.0/14", HostPrefix: 23},
				},
				HybridOverlayVXLANPort: ptrToUint32(9000),
			},
			gatewayConfig: &operv1.GatewayConfig{
				RoutingViaHost: true,
			},
			masterIPs: []string{"1.2.3.4", "2.3.4.5"},
		},
		{
			desc: "HybridOverlay enabled with no ClusterNetworkEntry",
			expected: `
[default]
mtu="1500"
cluster-subnets="10.128.0.0/15/23,10.0.0.0/14/24"
encap-port="8061"
enable-lflow-cache=true
lflow-cache-limit-kb=1048576
enable-udp-aggregation=true

[kubernetes]
service-cidrs="172.30.0.0/16"
ovn-config-namespace="openshift-ovn-kubernetes"
apiserver="https://testing.test:8443"
host-network-namespace="openshift-host-network"
no-hostsubnet-nodes="kubernetes.io/os=windows"
platform-type="GCP"
healthz-bind-address="0.0.0.0:10256"
dns-service-namespace="openshift-dns"
dns-service-name="dns-default"

[ovnkubernetesfeature]
enable-egress-ip=true
enable-egress-firewall=true
enable-egress-qos=true
enable-egress-service=true
egressip-node-healthcheck-port=9107
enable-multi-network=true

[gateway]
mode=shared
nodeport=true

[clustermanager]

[logging]
libovsdblogfile=/var/log/ovnkube/libovsdb.log
logfile-maxsize=100
logfile-maxbackups=5
logfile-maxage=0

[hybridoverlay]
enabled=true`,

			hybridOverlayConfig: &operv1.HybridOverlayConfig{},
			masterIPs:           []string{"1.2.3.4", "2.3.4.5"},
		},
		{
			desc: "Single Node OpenShift should contain SNO specific leader election settings",
			expected: `
[default]
mtu="1500"
cluster-subnets="10.128.0.0/15/23,10.0.0.0/14/24"
encap-port="8061"
enable-lflow-cache=true
lflow-cache-limit-kb=1048576
enable-udp-aggregation=true

[kubernetes]
service-cidrs="172.30.0.0/16"
ovn-config-namespace="openshift-ovn-kubernetes"
apiserver="https://testing.test:8443"
host-network-namespace="openshift-host-network"
platform-type="GCP"
healthz-bind-address="0.0.0.0:10256"
dns-service-namespace="openshift-dns"
dns-service-name="dns-default"

[ovnkubernetesfeature]
enable-egress-ip=true
enable-egress-firewall=true
enable-egress-qos=true
enable-egress-service=true
egressip-node-healthcheck-port=9107
enable-multi-network=true

[gateway]
mode=shared
nodeport=true

[clustermanager]

[logging]
libovsdblogfile=/var/log/ovnkube/libovsdb.log
logfile-maxsize=100
logfile-maxbackups=5
logfile-maxage=0

[masterha]
election-lease-duration=137
election-renew-deadline=107
election-retry-period=26`,
			masterIPs: []string{"1.2.3.4"},
			gatewayConfig: &operv1.GatewayConfig{
				RoutingViaHost: false,
			},
		},
		{
			desc: "disable UDP aggregation",
			expected: `
[default]
mtu="1500"
cluster-subnets="10.128.0.0/15/23,10.0.0.0/14/24"
encap-port="8061"
enable-lflow-cache=true
lflow-cache-limit-kb=1048576
enable-udp-aggregation=false

[kubernetes]
service-cidrs="172.30.0.0/16"
ovn-config-namespace="openshift-ovn-kubernetes"
apiserver="https://testing.test:8443"
host-network-namespace="openshift-host-network"
platform-type="GCP"
healthz-bind-address="0.0.0.0:10256"
dns-service-namespace="openshift-dns"
dns-service-name="dns-default"

[ovnkubernetesfeature]
enable-egress-ip=true
enable-egress-firewall=true
enable-egress-qos=true
enable-egress-service=true
egressip-node-healthcheck-port=9107
enable-multi-network=true

[gateway]
mode=shared
nodeport=true

[clustermanager]

[logging]
libovsdblogfile=/var/log/ovnkube/libovsdb.log
logfile-maxsize=100
logfile-maxbackups=5
logfile-maxage=0`,
			masterIPs:  []string{"1.2.3.4", "2.3.4.5"},
			disableGRO: true,
		},
		{
			desc: "disabled multi-network",
			expected: `
[default]
mtu="1500"
cluster-subnets="10.128.0.0/15/23,10.0.0.0/14/24"
encap-port="8061"
enable-lflow-cache=true
lflow-cache-limit-kb=1048576
enable-udp-aggregation=true

[kubernetes]
service-cidrs="172.30.0.0/16"
ovn-config-namespace="openshift-ovn-kubernetes"
apiserver="https://testing.test:8443"
host-network-namespace="openshift-host-network"
platform-type="GCP"
healthz-bind-address="0.0.0.0:10256"
dns-service-namespace="openshift-dns"
dns-service-name="dns-default"

[ovnkubernetesfeature]
enable-egress-ip=true
enable-egress-firewall=true
enable-egress-qos=true
enable-egress-service=true
egressip-node-healthcheck-port=9107

[gateway]
mode=shared
nodeport=true

[clustermanager]

[logging]
libovsdblogfile=/var/log/ovnkube/libovsdb.log
logfile-maxsize=100
logfile-maxbackups=5
logfile-maxage=0`,
			masterIPs:       []string{"1.2.3.4", "2.3.4.5"},
			disableMultiNet: true,
		},
		{
			desc: "enable multi-network policies and admin network policies",
			expected: `
[default]
mtu="1500"
cluster-subnets="10.128.0.0/15/23,10.0.0.0/14/24"
encap-port="8061"
enable-lflow-cache=true
lflow-cache-limit-kb=1048576
enable-udp-aggregation=true

[kubernetes]
service-cidrs="172.30.0.0/16"
ovn-config-namespace="openshift-ovn-kubernetes"
apiserver="https://testing.test:8443"
host-network-namespace="openshift-host-network"
platform-type="GCP"
healthz-bind-address="0.0.0.0:10256"
dns-service-namespace="openshift-dns"
dns-service-name="dns-default"

[ovnkubernetesfeature]
enable-egress-ip=true
enable-egress-firewall=true
enable-egress-qos=true
enable-egress-service=true
egressip-node-healthcheck-port=9107
enable-multi-network=true
enable-multi-networkpolicy=true
enable-admin-network-policy=true

[gateway]
mode=shared
nodeport=true

[clustermanager]

[logging]
libovsdblogfile=/var/log/ovnkube/libovsdb.log
logfile-maxsize=100
logfile-maxbackups=5
logfile-maxage=0`,
			masterIPs:              []string{"1.2.3.4", "2.3.4.5"},
			enableMultiNetPolicies: true,
			enableAdminNetPolicies: true,
		},
		{
			desc: "enable multi-network policies without multi-network support",
			expected: `
[default]
mtu="1500"
cluster-subnets="10.128.0.0/15/23,10.0.0.0/14/24"
encap-port="8061"
enable-lflow-cache=true
lflow-cache-limit-kb=1048576
enable-udp-aggregation=true

[kubernetes]
service-cidrs="172.30.0.0/16"
ovn-config-namespace="openshift-ovn-kubernetes"
apiserver="https://testing.test:8443"
host-network-namespace="openshift-host-network"
platform-type="GCP"
healthz-bind-address="0.0.0.0:10256"
dns-service-namespace="openshift-dns"
dns-service-name="dns-default"

[ovnkubernetesfeature]
enable-egress-ip=true
enable-egress-firewall=true
enable-egress-qos=true
enable-egress-service=true
egressip-node-healthcheck-port=9107
enable-admin-network-policy=true

[gateway]
mode=shared
nodeport=true

[clustermanager]

[logging]
libovsdblogfile=/var/log/ovnkube/libovsdb.log
logfile-maxsize=100
logfile-maxbackups=5
logfile-maxage=0`,
			masterIPs:              []string{"1.2.3.4", "2.3.4.5"},
			disableMultiNet:        true,
			enableMultiNetPolicies: true,
			enableAdminNetPolicies: true,
		},
	}
	g := NewGomegaWithT(t)

	for i, tc := range testcases {
		t.Run(fmt.Sprintf("%d:%s", i, tc.desc), func(t *testing.T) {
			OVNKubeConfig := OVNKubernetesConfig.DeepCopy()
			if tc.hybridOverlayConfig != nil {
				OVNKubeConfig.Spec.DefaultNetwork.OVNKubernetesConfig.HybridOverlayConfig = tc.hybridOverlayConfig
			}
			if tc.gatewayConfig != nil {
				OVNKubeConfig.Spec.DefaultNetwork.OVNKubernetesConfig.GatewayConfig = tc.gatewayConfig
				if tc.gatewayConfig.IPv4.InternalMasqueradeSubnet != "" {
					OVNKubeConfig.Spec.DefaultNetwork.OVNKubernetesConfig.GatewayConfig.IPv4.InternalMasqueradeSubnet = tc.gatewayConfig.IPv4.InternalMasqueradeSubnet
				}
			}
			if tc.egressIPConfig != nil {
				OVNKubeConfig.Spec.DefaultNetwork.OVNKubernetesConfig.EgressIPConfig = *tc.egressIPConfig
			}
			//set a few inputs so that the tests are not machine dependant
			OVNKubeConfig.Spec.DefaultNetwork.OVNKubernetesConfig.MTU = ptrToUint32(1500)

			if tc.v4InternalSubnet != "" {
				OVNKubeConfig.Spec.DefaultNetwork.OVNKubernetesConfig.V4InternalSubnet = tc.v4InternalSubnet
			}

			OVNKubeConfig.Spec.DisableMultiNetwork = &tc.disableMultiNet
			OVNKubeConfig.Spec.UseMultiNetworkPolicy = &tc.enableMultiNetPolicies

			crd := OVNKubeConfig.DeepCopy()
			config := &crd.Spec

			errs := validateOVNKubernetes(config)
			g.Expect(errs).To(HaveLen(0))
			fillDefaults(config, nil)

			bootstrapResult := fakeBootstrapResult()
			bootstrapResult.OVN = bootstrap.OVNBootstrapResult{
				MasterAddresses: tc.masterIPs,
				OVNKubernetesConfig: &bootstrap.OVNConfigBoostrapResult{
					DpuHostModeLabel:     OVN_NODE_SELECTOR_DEFAULT_DPU_HOST,
					DpuModeLabel:         OVN_NODE_SELECTOR_DEFAULT_DPU,
					SmartNicModeLabel:    OVN_NODE_SELECTOR_DEFAULT_SMART_NIC,
					MgmtPortResourceName: "",
					HyperShiftConfig: &bootstrap.OVNHyperShiftBootstrapResult{
						Enabled: false,
					},
					DisableUDPAggregation: tc.disableGRO,
				},
			}
			enabled := []configv1.FeatureGateName{}
			disabled := []configv1.FeatureGateName{configv1.FeatureGateAdminNetworkPolicy}
			if tc.enableAdminNetPolicies {
				disabled = []configv1.FeatureGateName{}
				enabled = []configv1.FeatureGateName{configv1.FeatureGateAdminNetworkPolicy}
			}
			featureGatesCNO := featuregates.NewFeatureGate(enabled, disabled)
			fakeClient := cnofake.NewFakeClient()
			objs, _, err := renderOVNKubernetes(config, bootstrapResult, manifestDirOvn, fakeClient, featureGatesCNO)
			g.Expect(err).NotTo(HaveOccurred())
			confFile := extractOVNKubeConfig(g, objs)
			msg := fmt.Sprintf("XXX TC Desc: %s\n\nXXX GOT: %s\n\nXXX Expected: %s\n", tc.desc, confFile, strings.TrimSpace(tc.expected))
			g.Expect(confFile).To(Equal(strings.TrimSpace(tc.expected)), msg)
			// check that the daemonset has the IP family mode annotations
			ipFamilyMode := names.IPFamilySingleStack
			g.Expect(checkDaemonsetAnnotation(g, objs, names.NetworkIPFamilyModeAnnotation, ipFamilyMode)).To(BeTrue())
		})
	}

}

func checkOVNKubernetesPostStart(objects []*uns.Unstructured) error {
	// check that ovnkube-control-plane is inside the rendered objects
	var controlPlane *uns.Unstructured
	for _, obj := range objects {
		if obj.GetKind() == "Deployment" && obj.GetNamespace() == "openshift-ovn-kubernetes" && obj.GetName() == "ovnkube-control-plane" {
			controlPlane = obj
			break
		}
	}
	if controlPlane == nil {
		return fmt.Errorf("could not find control-plane deployment")
	}

	// check that ovnkube-node is inside the rendered objects and that it defines the nbdb container
	var ovnkubeNode *uns.Unstructured
	for _, obj := range objects {
		if obj.GetKind() == "DaemonSet" && obj.GetNamespace() == "openshift-ovn-kubernetes" && obj.GetName() == "ovnkube-node" {

			ovnkubeNode = obj
		}
	}

	ovnkubeNodeContainers, found, err := uns.NestedSlice(ovnkubeNode.Object, "spec", "template", "spec", "containers")
	if err != nil {
		return fmt.Errorf("failed to get containers from ovnkube-node daemonset : %w", err)
	}
	if !found {
		return fmt.Errorf("unable to find containers in ovnkube-node daemonset : %w", err)
	}

	var nbdb map[string]interface{}
	for _, container := range ovnkubeNodeContainers {
		cmap := container.(map[string]interface{})
		name, found, err := uns.NestedString(cmap, "name")
		if found && err == nil && name == "nbdb" {
			nbdb = cmap
			break
		}
	}

	// Check ndbd node containers to have expected script
	if nbdb == nil {
		return fmt.Errorf("daemonSet openshift-ovn-kubernetes/ovnkube-node is expected to have nbdb container")
	}

	script, found, err := uns.NestedStringSlice(nbdb, "lifecycle", "postStart", "exec", "command")
	if err != nil {
		return fmt.Errorf("unable to get postStart in daemonset %s : %w", ovnkubeNode.GetName(), err)
	}
	if !found {
		return fmt.Errorf("could not find nbdb postStart script in daemonset %s", ovnkubeNode.GetName())
	}

	expectedScriptSubStr := "nbdb-post-start"
	if !strings.Contains(strings.Join(script, " "), expectedScriptSubStr) {
		return fmt.Errorf("postStart script in daemonset %s does not contain %s: %s", ovnkubeNode.GetName(), expectedScriptSubStr, script)
	}

	return nil
}

func TestFillOVNKubernetesDefaults(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := OVNKubernetesConfig.DeepCopy()
	conf := &crd.Spec
	conf.DefaultNetwork.OVNKubernetesConfig = nil

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
			Type: operv1.NetworkTypeOVNKubernetes,
			OVNKubernetesConfig: &operv1.OVNKubernetesConfig{
				MTU:        ptrToUint32(8900),
				GenevePort: ptrToUint32(6081),
				PolicyAuditConfig: &operv1.PolicyAuditConfig{
					RateLimit:      ptrToUint32(20),
					MaxFileSize:    ptrToUint32(50),
					Destination:    "null",
					SyslogFacility: "local0",
				},
			},
		},
	}

	fillOVNKubernetesDefaults(conf, nil, 9000)

	g.Expect(conf).To(Equal(&expected))

}

func TestFillOVNKubernetesDefaultsIPsec(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := OVNKubernetesConfig.DeepCopy()
	conf := &crd.Spec
	conf.DefaultNetwork.OVNKubernetesConfig.IPsecConfig = &operv1.IPsecConfig{}

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
			Type: operv1.NetworkTypeOVNKubernetes,
			OVNKubernetesConfig: &operv1.OVNKubernetesConfig{
				MTU:         ptrToUint32(8854),
				GenevePort:  ptrToUint32(8061),
				IPsecConfig: &operv1.IPsecConfig{},
				PolicyAuditConfig: &operv1.PolicyAuditConfig{
					RateLimit:      ptrToUint32(20),
					MaxFileSize:    ptrToUint32(50),
					Destination:    "null",
					SyslogFacility: "local0",
				},
			},
		},
	}

	fillOVNKubernetesDefaults(conf, conf, 9000)

	g.Expect(conf).To(Equal(&expected))

}
func TestValidateOVNKubernetes(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := OVNKubernetesConfig.DeepCopy()
	config := &crd.Spec
	ovnConfig := config.DefaultNetwork.OVNKubernetesConfig

	err := validateOVNKubernetes(config)
	g.Expect(err).To(BeEmpty())
	fillDefaults(config, nil)

	errExpect := func(substr string) {
		t.Helper()
		g.Expect(validateOVNKubernetes(config)).To(
			ContainElement(MatchError(
				ContainSubstring(substr))))
	}

	// set mtu to insanity
	ovnConfig.MTU = ptrToUint32(70000)
	errExpect("invalid MTU 70000")

	// set geneve port to insanity
	ovnConfig.GenevePort = ptrToUint32(70001)
	errExpect("invalid GenevePort 70001")

	config.ClusterNetwork = []operv1.ClusterNetworkEntry{{
		CIDR: "fd01::/48", HostPrefix: 64,
	}}

	// invalid ipv6 mtu
	ovnConfig.MTU = ptrToUint32(576)
	errExpect("invalid MTU 576")

	config.ClusterNetwork = nil
	errExpect("ClusterNetwork cannot be empty")
}

func TestValidateOVNKubernetesSubnetsIPv4(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := OVNKubernetesConfig.DeepCopy()
	config := &crd.Spec
	ovnConfig := config.DefaultNetwork.OVNKubernetesConfig
	ovnConfig.GatewayConfig = &operv1.GatewayConfig{}
	ovnConfig.IPv4 = &operv1.IPv4OVNKubernetesConfig{}
	ovnConfig.IPv6 = &operv1.IPv6OVNKubernetesConfig{}

	err := validateOVNKubernetesSubnets(config)
	g.Expect(err).NotTo(HaveOccurred())
	fillDefaults(config, nil)

	errExpect := func(substr string) {
		t.Helper()
		g.Expect(validateOVNKubernetesSubnets(config)).To(
			ContainElement(MatchError(
				ContainSubstring(substr))))
	}

	errNotExpect := func(substr string) {
		t.Helper()
		g.Expect(validateOVNKubernetesSubnets(config)).To(
			Not(
				ContainElement(MatchError(
					ContainSubstring(substr)))))
	}
	// IP family(IPv4) and subnet length check
	ovnConfig.V4InternalSubnet = "100.64.0.0/22"
	errExpect("v4InternalJoinSubnet 100.64.0.0/22 is not large enough for the maximum number of nodes which can be supported by ClusterNetwork")
	ovnConfig.V6InternalSubnet = "fd01::/48"
	errExpect("JoinSubnet fd01::/48 and ClusterNetwork must have matching IP families")
	ovnConfig.V4InternalSubnet = "100.64.0.0/21"
	errNotExpect("v4InternalJoinSubnet 100.64.0.0/21 is not large enough for the maximum number of nodes which can be supported by ClusterNetwork")
	ovnConfig.IPv4.InternalJoinSubnet = "100.64.0.0/22"
	ovnConfig.V4InternalSubnet = ""
	errNotExpect("v4InternalSubnet will be deprecated soon, until then it must be same as v4InternalJoinSubnet 100.64.0.0/22")
	errExpect("v4InternalJoinSubnet 100.64.0.0/22 is not large enough for the maximum number of nodes which can be supported by ClusterNetwork")
	ovnConfig.IPv4.InternalJoinSubnet = "100.64.0.0/21"
	errNotExpect("v4InternalJoinSubnet 100.64.0.0/21 is not large enough for the maximum number of nodes which can be supported by ClusterNetwork")
	ovnConfig.IPv6.InternalJoinSubnet = "fd01::/48"
	errExpect("JoinSubnet fd01::/48 and ClusterNetwork must have matching IP families")
	ovnConfig.IPv4.InternalJoinSubnet = "100.64.0.0/22"
	ovnConfig.V4InternalSubnet = "100.64.0.0/22"
	errNotExpect("v4InternalSubnet will be deprecated soon, until then it must be same as v4InternalJoinSubnet 100.64.0.0/22")
	ovnConfig.IPv4.InternalJoinSubnet = "100.64.0.0/23"
	ovnConfig.V4InternalSubnet = "100.64.0.0/22"
	errExpect("v4InternalSubnet will be deprecated soon, until then it must be same as v4InternalJoinSubnet 100.64.0.0/23")
	ovnConfig.IPv4.InternalTransitSwitchSubnet = "100.88.0.0/22"
	errExpect("v4InternalTransitSwitchSubnet 100.88.0.0/22 is not large enough for the maximum number of nodes which can be supported by ClusterNetwork")
	ovnConfig.IPv4.InternalTransitSwitchSubnet = "100.88.0.0/21"
	errNotExpect("v4InternalTransitSwitchSubnet 100.88.0.0/21 is not large enough for the maximum number of nodes which can be supported by ClusterNetwork")
	ovnConfig.IPv6.InternalTransitSwitchSubnet = "fd97::/48"
	errExpect("v6InternalTransitSwitchSubnet fd97::/48 and ClusterNetwork must have matching IP families")
	ovnConfig.GatewayConfig.IPv6.InternalMasqueradeSubnet = "fd01::/48"
	errExpect("v6InternalMasqueradeSubnet fd01::/48 and ClusterNetwork must have matching IP families")

	// IPv4 subnet overlap check
	ovnConfig.V4InternalSubnet = ""
	ovnConfig.IPv4.InternalJoinSubnet = "10.128.0.0/16"
	errExpect("Whole or subset of v4InternalJoinSubnet CIDR 10.128.0.0/16 is already in use: CIDRs 10.128.0.0/15 and 10.128.0.0/16 overlap")
	ovnConfig.IPv4.InternalTransitSwitchSubnet = "10.128.0.0/16"
	errExpect("Whole or subset of v4InternalTransitSwitchSubnet CIDR 10.128.0.0/16 is already in use: CIDRs 10.128.0.0/15 and 10.128.0.0/16 overlap")
	ovnConfig.GatewayConfig.IPv4.InternalMasqueradeSubnet = "10.128.0.0/16"
	errExpect("Whole or subset of v4InternalMasqueradeSubnet CIDR 10.128.0.0/16 is already in use: CIDRs 10.128.0.0/15 and 10.128.0.0/16 overlap")
	ovnConfig.IPv4.InternalJoinSubnet = "100.99.0.0/16"
	ovnConfig.GatewayConfig.IPv4.InternalMasqueradeSubnet = "100.99.0.0/16"
	errExpect("Whole or subset of v4InternalMasqueradeSubnet CIDR 100.99.0.0/16 is already in use: CIDRs 100.99.0.0/16 and 100.99.0.0/16 overlap")
	ovnConfig.IPv4.InternalJoinSubnet = "100.99.0.0/16"
	ovnConfig.IPv4.InternalTransitSwitchSubnet = "100.99.0.0/16"
	errExpect("Whole or subset of v4InternalTransitSwitchSubnet CIDR 100.99.0.0/16 is already in use: CIDRs 100.99.0.0/16 and 100.99.0.0/16 overlap")
	ovnConfig.IPv4.InternalTransitSwitchSubnet = "100.99.0.0/16"
	ovnConfig.GatewayConfig.IPv4.InternalMasqueradeSubnet = "100.99.0.0/16"
	errExpect("Whole or subset of v4InternalMasqueradeSubnet CIDR 100.99.0.0/16 is already in use: CIDRs 100.99.0.0/16 and 100.99.0.0/16 overlap")
}

func TestValidateOVNKubernetesSubnetsIPv6(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := OVNKubernetesConfig.DeepCopy()
	config := &crd.Spec
	ovnConfig := config.DefaultNetwork.OVNKubernetesConfig
	ovnConfig.GatewayConfig = &operv1.GatewayConfig{}
	ovnConfig.IPv4 = &operv1.IPv4OVNKubernetesConfig{}
	ovnConfig.IPv6 = &operv1.IPv6OVNKubernetesConfig{}

	err := validateOVNKubernetesSubnets(config)
	g.Expect(err).NotTo(HaveOccurred())
	fillDefaults(config, nil)

	errExpect := func(substr string) {
		t.Helper()
		g.Expect(validateOVNKubernetesSubnets(config)).To(
			ContainElement(MatchError(
				ContainSubstring(substr))))
	}

	errNotExpect := func(substr string) {
		t.Helper()
		g.Expect(validateOVNKubernetesSubnets(config)).To(
			Not(
				ContainElement(MatchError(
					ContainSubstring(substr)))))
	}

	config.ServiceNetwork = []string{"fd02::/112"}
	config.ClusterNetwork = []operv1.ClusterNetworkEntry{{
		CIDR: "fd01::/48", HostPrefix: 64,
	}}

	// IP family(IPv6) and subnet length check
	ovnConfig.V6InternalSubnet = "fd03::/112"
	errExpect("v6InternalJoinSubnet fd03::/112 is not large enough for the maximum number of nodes which can be supported by ClusterNetwork")
	ovnConfig.V4InternalSubnet = "100.64.0.0/22"
	errExpect("JoinSubnet 100.64.0.0/22 and ClusterNetwork must have matching IP families")
	ovnConfig.V6InternalSubnet = "fd03::/111"
	errNotExpect("v6InternalJoinSubnet fd03::/111 is not large enough for the maximum number of nodes which can be supported by ClusterNetwork")
	ovnConfig.IPv6.InternalJoinSubnet = "fd03::/112"
	ovnConfig.V6InternalSubnet = ""
	errNotExpect("v6InternalJoinSubnet will be deprecated soon, until then it must be same as v6InternalJoinSubnet fd03::/112")
	errExpect("v6InternalJoinSubnet fd03::/112 is not large enough for the maximum number of nodes which can be supported by ClusterNetwork")
	ovnConfig.IPv6.InternalJoinSubnet = "fd03::/111"
	errNotExpect("v6InternalJoinSubnet fd03::/111 is not large enough for the maximum number of nodes which can be supported by ClusterNetwork")
	ovnConfig.IPv4.InternalJoinSubnet = "100.64.0.0/22"
	errExpect("JoinSubnet 100.64.0.0/22 and ClusterNetwork must have matching IP families")
	ovnConfig.IPv6.InternalJoinSubnet = "fd03::/112"
	ovnConfig.V6InternalSubnet = "fd03::/112"
	errNotExpect("v6InternalSubnet will be deprecated soon, until then it must be same as v6InternalJoinSubnet fd03::/112")
	ovnConfig.IPv6.InternalJoinSubnet = "fd03::/112"
	ovnConfig.V6InternalSubnet = "fd03::/113"
	errExpect("v6InternalSubnet will be deprecated soon, until then it must be same as v6InternalJoinSubnet fd03::/112")
	ovnConfig.IPv6.InternalTransitSwitchSubnet = "fd03::/112"
	errExpect("v6InternalTransitSwitchSubnet fd03::/112 is not large enough for the maximum number of nodes which can be supported by ClusterNetwork")
	ovnConfig.IPv6.InternalTransitSwitchSubnet = "fd03::/111"
	errNotExpect("v6InternalTransitSwitchSubnet fd03::/111 is not large enough for the maximum number of nodes which can be supported by ClusterNetwork")
	ovnConfig.IPv4.InternalTransitSwitchSubnet = "100.88.0.0/22"
	errExpect("v4InternalTransitSwitchSubnet 100.88.0.0/22 and ClusterNetwork must have matching IP families")
	ovnConfig.GatewayConfig.IPv4.InternalMasqueradeSubnet = "169.254.169.0/29"
	errExpect("v4InternalMasqueradeSubnet 169.254.169.0/29 and ClusterNetwork must have matching IP families")

	// IPv6 subnet overlap check
	ovnConfig.V6InternalSubnet = ""
	ovnConfig.IPv6.InternalJoinSubnet = "fd01::/64"
	errExpect("Whole or subset of v6InternalJoinSubnet CIDR fd01::/64 is already in use: CIDRs fd01::/48 and fd01::/64 overlap")
	ovnConfig.IPv6.InternalTransitSwitchSubnet = "fd01::/64"
	errExpect("Whole or subset of v6InternalTransitSwitchSubnet CIDR fd01::/64 is already in use: CIDRs fd01::/48 and fd01::/64 overlap")
	ovnConfig.GatewayConfig.IPv6.InternalMasqueradeSubnet = "fd01::/64"
	errExpect("Whole or subset of v6InternalMasqueradeSubnet CIDR fd01::/64 is already in use: CIDRs fd01::/48 and fd01::/64 overlap")
	ovnConfig.IPv6.InternalJoinSubnet = "fd69::/111"
	ovnConfig.GatewayConfig.IPv6.InternalMasqueradeSubnet = "fd69::/111"
	errExpect("Whole or subset of v6InternalMasqueradeSubnet CIDR fd69::/111 is already in use: CIDRs fd69::/111 and fd69::/111 overlap")
	ovnConfig.IPv6.InternalJoinSubnet = "fd69::/111"
	ovnConfig.IPv6.InternalTransitSwitchSubnet = "fd69::/111"
	errExpect("Whole or subset of v6InternalTransitSwitchSubnet CIDR fd69::/111 is already in use: CIDRs fd69::/111 and fd69::/111 overlap")
	ovnConfig.IPv6.InternalTransitSwitchSubnet = "fd69::/111"
	ovnConfig.GatewayConfig.IPv6.InternalMasqueradeSubnet = "fd69::/111"
	errExpect("Whole or subset of v6InternalMasqueradeSubnet CIDR fd69::/111 is already in use: CIDRs fd69::/111 and fd69::/111 overlap")
}

func TestValidateOVNKubernetesDualStack(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := OVNKubernetesConfig.DeepCopy()
	config := &crd.Spec

	err := validateOVNKubernetes(config)
	g.Expect(err).To(BeEmpty())
	fillDefaults(config, nil)

	errExpect := func(substr string) {
		t.Helper()
		g.Expect(validateOVNKubernetes(config)).To(
			ContainElement(MatchError(
				ContainSubstring(substr))))
	}

	config.ClusterNetwork = []operv1.ClusterNetworkEntry{
		{CIDR: "10.128.0.0/14", HostPrefix: 23},
		{CIDR: "10.0.0.0/14", HostPrefix: 23},
	}
	err = validateOVNKubernetes(config)
	g.Expect(err).To(BeEmpty())

	config.ServiceNetwork = []string{
		"fd02::/112",
	}
	errExpect("ClusterNetwork and ServiceNetwork must have matching IP families")

	config.ClusterNetwork = append(config.ClusterNetwork, operv1.ClusterNetworkEntry{
		CIDR: "fd01::/48", HostPrefix: 64,
	})
	errExpect("ClusterNetwork and ServiceNetwork must have matching IP families")

	config.ServiceNetwork = append(config.ServiceNetwork, "172.30.0.0/16")
	err = validateOVNKubernetes(config)
	g.Expect(err).To(BeEmpty())

	config.ServiceNetwork = append(config.ServiceNetwork, "172.31.0.0/16")
	errExpect("ServiceNetwork must have either a single CIDR or a dual-stack pair of CIDRs")
}

func TestOVNKubernetesIsSafe(t *testing.T) {
	g := NewGomegaWithT(t)

	prev := OVNKubernetesConfig.Spec.DeepCopy()
	fillDefaults(prev, nil)
	next := OVNKubernetesConfig.Spec.DeepCopy()
	fillDefaults(next, nil)

	errs := isOVNKubernetesChangeSafe(prev, next)
	g.Expect(errs).To(BeEmpty())

	// try to add a new hybrid overlay config
	hybridOverlayConfigNext :=
		operv1.HybridOverlayConfig{
			HybridClusterNetwork: []operv1.ClusterNetworkEntry{
				{CIDR: "10.132.0.0/14", HostPrefix: 23},
			},
		}
	next.DefaultNetwork.OVNKubernetesConfig.HybridOverlayConfig = &hybridOverlayConfigNext

	errs = isOVNKubernetesChangeSafe(prev, next)
	g.Expect(errs).To(BeEmpty())

	//try to change a previous hybrid overlay
	hybridOverlayConfigPrev :=
		operv1.HybridOverlayConfig{
			HybridClusterNetwork: []operv1.ClusterNetworkEntry{
				{CIDR: "10.135.0.0/14", HostPrefix: 23},
			},
		}
	prev.DefaultNetwork.OVNKubernetesConfig.HybridOverlayConfig = &hybridOverlayConfigPrev
	errs = isOVNKubernetesChangeSafe(prev, next)
	g.Expect(errs).To(HaveLen(1))
	g.Expect(errs[0]).To(MatchError("cannot edit a running hybrid overlay network"))

	prev.DefaultNetwork.OVNKubernetesConfig.HybridOverlayConfig = nil
	next.DefaultNetwork.OVNKubernetesConfig.HybridOverlayConfig = nil

	//try to disable a running hybrid overlay
	prev.DefaultNetwork.OVNKubernetesConfig.HybridOverlayConfig = &hybridOverlayConfigPrev
	next.DefaultNetwork.OVNKubernetesConfig.HybridOverlayConfig = nil

	errs = isOVNKubernetesChangeSafe(prev, next)
	g.Expect(errs).To(BeEmpty())

	// change the mtu without migration
	next.DefaultNetwork.OVNKubernetesConfig.MTU = ptrToUint32(70000)

	// change the geneve port
	next.DefaultNetwork.OVNKubernetesConfig.GenevePort = ptrToUint32(34001)
	errs = isOVNKubernetesChangeSafe(prev, next)
	g.Expect(errs).To(HaveLen(2))
	g.Expect(errs[0]).To(MatchError("cannot change ovn-kubernetes MTU without migration"))
	g.Expect(errs[1]).To(MatchError("cannot change ovn-kubernetes genevePort"))

	next.DefaultNetwork.OVNKubernetesConfig.MTU = prev.DefaultNetwork.OVNKubernetesConfig.MTU
	next.DefaultNetwork.OVNKubernetesConfig.GenevePort = prev.DefaultNetwork.OVNKubernetesConfig.GenevePort

	// mtu migration

	// valid mtu migration
	next.Migration = &operv1.NetworkMigration{
		MTU: &operv1.MTUMigration{
			Network: &operv1.MTUMigrationValues{
				From: prev.DefaultNetwork.OVNKubernetesConfig.MTU,
				To:   ptrToUint32(1300),
			},
			Machine: &operv1.MTUMigrationValues{
				To: ptrToUint32(1500),
			},
		},
	}
	errs = isOVNKubernetesChangeSafe(prev, next)
	g.Expect(errs).To(BeEmpty())

	// missing fields
	next.Migration.MTU.Network.From = nil
	errs = isOVNKubernetesChangeSafe(prev, next)
	g.Expect(errs).To(HaveLen(1))
	g.Expect(errs[0]).To(MatchError("invalid Migration.MTU, at least one of the required fields is missing"))

	// invalid Migration.MTU.Network.From, not equal to previously applied MTU
	next.Migration.MTU.Network.From = ptrToUint32(*prev.DefaultNetwork.OVNKubernetesConfig.MTU + 100)
	errs = isOVNKubernetesChangeSafe(prev, next)
	g.Expect(errs).To(HaveLen(1))
	g.Expect(errs[0]).To(MatchError(fmt.Sprintf("invalid Migration.MTU.Network.From(%d) not equal to the currently applied MTU(%d)", *next.Migration.MTU.Network.From, *prev.DefaultNetwork.OVNKubernetesConfig.MTU)))

	next.Migration.MTU.Network.From = prev.DefaultNetwork.OVNKubernetesConfig.MTU

	// invalid Migration.MTU.Network.To, lower than minimum MTU for IPv4
	next.Migration.MTU.Network.To = ptrToUint32(100)
	errs = isOVNKubernetesChangeSafe(prev, next)
	g.Expect(errs).To(HaveLen(1))
	g.Expect(errs[0]).To(MatchError(fmt.Sprintf("invalid Migration.MTU.Network.To(%d), has to be in range: %d-%d", *next.Migration.MTU.Network.To, MinMTUIPv4, MaxMTU)))

	// invalid Migration.MTU.Network.To, higher than maximum MTU for IPv4
	next.Migration.MTU.Network.To = ptrToUint32(MaxMTU + 1)
	errs = isOVNKubernetesChangeSafe(prev, next)
	g.Expect(errs).To(HaveLen(2))
	g.Expect(errs[0]).To(MatchError(fmt.Sprintf("invalid Migration.MTU.Network.To(%d), has to be in range: %d-%d", *next.Migration.MTU.Network.To, MinMTUIPv4, MaxMTU)))

	next.Migration.MTU.Network.To = ptrToUint32(1300)

	// invalid Migration.MTU.Machine.To, not big enough to accommodate next.Migration.MTU.Network.To with encap overhead
	next.Migration.MTU.Network.To = ptrToUint32(1500)
	errs = isOVNKubernetesChangeSafe(prev, next)
	g.Expect(errs).To(HaveLen(1))
	g.Expect(errs[0]).To(MatchError(fmt.Sprintf("invalid Migration.MTU.Machine.To(%d), has to be at least %d", *next.Migration.MTU.Machine.To, *next.Migration.MTU.Network.To+getOVNEncapOverhead(next))))

	// invalid Migration.MTU.Network.To, lower than minimum MTU for IPv6
	next.Migration.MTU.Network.To = ptrToUint32(1200)
	next.ClusterNetwork = []operv1.ClusterNetworkEntry{
		{
			CIDR:       "fd00:1:2:3::/64",
			HostPrefix: 56,
		},
	}
	errs = isOVNKubernetesChangeSafe(prev, next)
	g.Expect(errs).To(HaveLen(1))
	g.Expect(errs[0]).To(MatchError(fmt.Sprintf("invalid Migration.MTU.Network.To(%d), has to be in range: %d-%d", *next.Migration.MTU.Network.To, MinMTUIPv6, MaxMTU)))

	// invalid Migration.MTU.Machine.To, higher than max MTU
	next.Migration.MTU.Network.To = ptrToUint32(MaxMTU)
	next.Migration.MTU.Machine.To = ptrToUint32(*next.Migration.MTU.Network.To + getOVNEncapOverhead(next))
	errs = isOVNKubernetesChangeSafe(prev, next)
	g.Expect(errs).To(HaveLen(1))
	g.Expect(errs[0]).To(MatchError(fmt.Sprintf("invalid Migration.MTU.Machine.To(%d), has to be in range: %d-%d", *next.Migration.MTU.Machine.To, MinMTUIPv6, MaxMTU)))
}

// TestOVNKubernetesShouldUpdateMasterOnUpgrade checks to see that
func TestOVNKubernetestShouldUpdateMasterOnUpgrade(t *testing.T) {

	for idx, tc := range []struct {
		expectNode         bool // true if node changed
		expectControlPlane bool // true if master changed
		expectPrePull      bool // true if pre-puller rendered
		node               string
		controlPlane       string
		prepull            string // a (maybe) existing pre-puller daemonset
		rv                 string // release version
	}{

		// No node, prepuller and controlPlane - upgrade = true and config the same
		{
			expectNode:         true,
			expectControlPlane: true,
			expectPrePull:      false,
			node: `
apiVersion: apps/v1
kind: DaemonSet
`,
			controlPlane: `
apiVersion: apps/v1
kind: DaemonSet
`,
		},
		// PrePuller has to pull image before node can upgrade
		{
			expectNode:         false,
			expectControlPlane: true,
			expectPrePull:      true,
			node: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 4.7.0-0.ci-2021-01-10-200841
  namespace: openshift-ovn-kubernetes
  name: ovnkube-node
`,
			controlPlane: `
apiVersion: apps/v1
kind: DaemonSet
`,
		},

		{
			expectNode:         true,
			expectControlPlane: true,
			// Note: For reducing testing complexity, prepuller is set to false
			// because it hits the condition where the node's version (null) is same
			// as release version (null). In reality if node's version is different
			// from expected, prePull will be true.
			expectPrePull: false,
			controlPlane: `
apiVersion: apps/v1
kind: Deployment
metadata:
  annotations:
    release.openshift.io/version: 4.7.0-0.ci-2021-01-10-200841
  namespace: openshift-ovn-kubernetes
  name: ovnkube-control-plane
`,
			node: `
apiVersion: apps/v1
kind: DaemonSet
`,
		},

		// steady state, no prepuller
		{
			expectNode:         true,
			expectControlPlane: true,
			expectPrePull:      false,
			rv:                 "2.0.0",
			controlPlane: `
apiVersion: apps/v1
kind: Deployment
metadata:
  annotations:
    release.openshift.io/version: 2.0.0
  namespace: openshift-ovn-kubernetes
  name: ovnkube-control-plane
`,
			node: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 2.0.0
namespace: openshift-ovn-kubernetes
name: ovnkube-node
`,
		},

		// upgrade not yet applied, expecting prepuller to get created
		{
			expectNode:         false,
			expectControlPlane: false,
			expectPrePull:      true,
			rv:                 "2.0.0",
			controlPlane: `
apiVersion: apps/v1
kind: Deployment
metadata:
  annotations:
    release.openshift.io/version: 1.9.9
  namespace: openshift-ovn-kubernetes
  name: ovnkube-control-plane
`,
			node: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 1.9.9
  namespace: openshift-ovn-kubernetes
  name: ovnkube-node
`,
		},

		// upgrade not yet applied, prepuller rolling out
		{
			expectNode:         false,
			expectControlPlane: false,
			expectPrePull:      true,
			rv:                 "2.0.0",
			controlPlane: `
apiVersion: apps/v1
kind: Deployment
metadata:
  annotations:
    release.openshift.io/version: 1.9.9
  namespace: openshift-ovn-kubernetes
  name: ovnkube-control-plane
`,
			node: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 1.9.9
  namespace: openshift-ovn-kubernetes
  name: ovnkube-node
`,
			prepull: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 2.0.0
  namespace: openshift-ovn-kubernetes
  name: ovnkube-upgrades-prepuller
  generation: 2
status:
  currentNumberScheduled: 6
  desiredNumberScheduled: 6
  numberAvailable: 6
  numberMisscheduled: 0
  numberReady: 6
  observedGeneration: 1
  updatedNumberScheduled: 6
`,
		},

		// upgrade not yet applied, prepuller having wrong image version
		{
			expectNode:         false,
			expectControlPlane: false,
			expectPrePull:      true,
			rv:                 "2.0.0",
			controlPlane: `
apiVersion: apps/v1
kind: Deployment
metadata:
  annotations:
    release.openshift.io/version: 1.9.9
  namespace: openshift-ovn-kubernetes
  name: ovnkube-control-plane
`,
			node: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 1.9.9
  namespace: openshift-ovn-kubernetes
  name: ovnkube-node
`,
			prepull: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 2.0.1
  namespace: openshift-ovn-kubernetes
  name: ovnkube-upgrades-prepuller
`,
		},

		// node upgrade applied, upgrade not yet rolled out, prepuller has done its work.
		{
			expectNode:         true,
			expectControlPlane: false,
			expectPrePull:      false,
			rv:                 "2.0.0",
			controlPlane: `
apiVersion: apps/v1
kind: Deployment
metadata:
  annotations:
    release.openshift.io/version: 1.9.9
  namespace: openshift-ovn-kubernetes
  name: ovnkube-control-plane
`,
			node: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 2.0.0
  namespace: openshift-ovn-kubernetes
  name: ovnkube-node
  generation: 2
status:
  currentNumberScheduled: 6
  desiredNumberScheduled: 6
  numberAvailable: 6
  numberMisscheduled: 0
  numberReady: 6
  observedGeneration: 1
  updatedNumberScheduled: 6
`,
		},

		// node upgrade rolling out
		{
			expectNode:         true,
			expectControlPlane: false,
			expectPrePull:      false,

			rv: "2.0.0",
			controlPlane: `
apiVersion: apps/v1
kind: Deployment
metadata:
  annotations:
    release.openshift.io/version: 1.9.9
  namespace: openshift-ovn-kubernetes
  name: ovnkube-control-plane
`,
			node: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 2.0.0
  namespace: openshift-ovn-kubernetes
  name: ovnkube-node
  generation: 2
status:
  currentNumberScheduled: 6
  desiredNumberScheduled: 6
  numberAvailable: 5
  numberUnavailable: 1
  numberReady: 5
  observedGeneration: 2
  updatedNumberScheduled: 5
`,
		},

		// node upgrade hung but not made progress
		{
			expectNode:         true,
			expectControlPlane: false,
			expectPrePull:      false,
			rv:                 "2.0.0",
			controlPlane: `
apiVersion: apps/v1
kind: Deployment
metadata:
  annotations:
    release.openshift.io/version: 1.9.9
  namespace: openshift-ovn-kubernetes
  name: ovnkube-control-plane
`,
			node: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 2.0.0
    networkoperator.openshift.io/rollout-hung: ""
  namespace: openshift-ovn-kubernetes
  name: ovnkube-node
  generation: 2
status:
  currentNumberScheduled: 6
  desiredNumberScheduled: 6
  numberAvailable: 5
  numberUnavailable: 1
  numberMisscheduled: 0
  numberReady: 5
  observedGeneration: 2
  updatedNumberScheduled: 4
`,
		},

		// node upgrade hung but made enough progress
		{
			expectNode:         true,
			expectControlPlane: true,
			expectPrePull:      false,
			rv:                 "2.0.0",
			controlPlane: `
apiVersion: apps/v1
kind: Deployment
metadata:
  annotations:
    release.openshift.io/version: 1.9.9
  namespace: openshift-ovn-kubernetes
  name: ovnkube-control-plane
`,
			node: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 2.0.0
    networkoperator.openshift.io/rollout-hung: ""
  namespace: openshift-ovn-kubernetes
  name: ovnkube-node
  generation: 2
status:
  currentNumberScheduled: 6
  desiredNumberScheduled: 6
  numberAvailable: 5
  numberUnavailable: 1
  numberMisscheduled: 0
  numberReady: 5
  observedGeneration: 2
  updatedNumberScheduled: 5
`,
		},

		// Upgrade rolled out, everything is good
		{
			expectNode:         true,
			expectControlPlane: true,
			expectPrePull:      false,
			rv:                 "2.0.0",
			controlPlane: `
apiVersion: apps/v1
kind: Deployment
metadata:
  annotations:
    release.openshift.io/version: 1.9.9
  namespace: openshift-ovn-kubernetes
  name: ovnkube-control-plane
`,
			node: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 2.0.0
  namespace: openshift-ovn-kubernetes
  name: ovnkube-node
  generation: 2
status:
  currentNumberScheduled: 6
  desiredNumberScheduled: 6
  numberAvailable: 6
  numberMisscheduled: 0
  numberReady: 6
  observedGeneration: 2
  updatedNumberScheduled: 6
`,
		},

		// downgrade not yet applied
		{
			expectNode:         false,
			expectControlPlane: true,
			expectPrePull:      false,
			rv:                 "1.8.9",
			controlPlane: `
apiVersion: apps/v1
kind: Deployment
metadata:
  annotations:
    release.openshift.io/version: 1.9.9
  namespace: openshift-ovn-kubernetes
  name: ovnkube-control-plane
`,
			node: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 1.9.9
  namespace: openshift-ovn-kubernetes
  name: ovnkube-node
`,
		},

		// controlPlane downgrade applied, not yet rolled out
		{
			expectNode:         false,
			expectControlPlane: true,
			expectPrePull:      false,
			rv:                 "1.8.9",
			controlPlane: `
apiVersion: apps/v1
kind: Deployment
metadata:
  annotations:
    release.openshift.io/version: 1.8.9
  namespace: openshift-ovn-kubernetes
  name: ovnkube-control-plane
  generation: 2
status:
  availableReplicas: 6
  observedGeneration: 1
  readyReplicas: 6
  replicas: 6
  unavailableReplicas: 0
  updatedReplicas: 6
`,
			node: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 1.9.9
  namespace: openshift-ovn-kubernetes
  name: ovnkube-node
`,
		},

		// downgrade rolling out
		{
			expectNode:         false,
			expectControlPlane: true,
			expectPrePull:      false,
			rv:                 "1.8.9",
			controlPlane: `
apiVersion: apps/v1
kind: Deployment
metadata:
  annotations:
    release.openshift.io/version: 1.8.9
  namespace: openshift-ovn-kubernetes
  name: ovnkube-control-plane
  generation: 2
status:
  availableReplicas: 5
  observedGeneration: 2
  readyReplicas: 5
  replicas: 6
  unavailableReplicas: 1
  updatedReplicas:
`,
			node: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 1.9.9
  namespace: openshift-ovn-kubernetes
  name: ovnkube-node
`,
		},

		// downgrade hung but not made progress
		{
			expectNode:         false,
			expectControlPlane: true,
			expectPrePull:      false,
			rv:                 "1.8.9",
			controlPlane: `
apiVersion: apps/v1
kind: Deployment
metadata:
  annotations:
    release.openshift.io/version: 1.8.9
    networkoperator.openshift.io/rollout-hung: ""
  namespace: openshift-ovn-kubernetes
  name: ovnkube-control-plane
  generation: 2
status:
  availableReplicas: 2
  observedGeneration: 2
  readyReplicas: 2
  replicas: 3
  unavailableReplicas: 1
  updatedReplicas: 1
`,
			node: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 1.9.9
  namespace: openshift-ovn-kubernetes
  name: ovnkube-node
`,
		},

		// downgrade hung but made enough progress
		// except we always wait for 100% controlPlane.
		{
			expectNode:         false,
			expectControlPlane: true,
			expectPrePull:      false,
			rv:                 "1.8.9",
			controlPlane: `
apiVersion: apps/v1
kind: Deployment
metadata:
  annotations:
    release.openshift.io/version: 1.8.9
    networkoperator.openshift.io/rollout-hung: ""
  namespace: openshift-ovn-kubernetes
  name: ovnkube-control-plane
  generation: 2
status:
  availableReplicas: 2
  observedGeneration: 2
  readyReplicas: 2
  replicas: 3
  unavailableReplicas: 1
  updatedReplicas: 3
`,
			node: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 1.9.9
  namespace: openshift-ovn-kubernetes
  name: ovnkube-node
`,
		},
	} {
		t.Run(strconv.Itoa(idx), func(t *testing.T) {
			g := NewGomegaWithT(t)

			var node *appsv1.DaemonSet
			var controlPlane *appsv1.Deployment
			var prepuller *appsv1.DaemonSet
			nodeStatus := &bootstrap.OVNUpdateStatus{}
			controlPlaneStatus := &bootstrap.OVNUpdateStatus{}
			prepullerStatus := &bootstrap.OVNUpdateStatus{}
			crd := OVNKubernetesConfig.DeepCopy()
			config := &crd.Spec
			t.Setenv("RELEASE_VERSION", tc.rv)

			errs := validateOVNKubernetes(config)
			g.Expect(errs).To(HaveLen(0))
			fillDefaults(config, nil)

			node = &appsv1.DaemonSet{}
			err := yaml.Unmarshal([]byte(tc.node), node)
			if err != nil {
				t.Fatal(err)
			}
			nodeStatus.Kind = node.Kind
			nodeStatus.Namespace = node.Namespace
			nodeStatus.Name = node.Name
			nodeStatus.IPFamilyMode = node.GetAnnotations()[names.NetworkIPFamilyModeAnnotation]
			nodeStatus.Version = node.GetAnnotations()["release.openshift.io/version"]
			nodeStatus.Progressing = daemonSetProgressing(node, true)

			controlPlane = &appsv1.Deployment{}
			err = yaml.Unmarshal([]byte(tc.controlPlane), controlPlane)
			if err != nil {
				t.Fatal(err)
			}
			controlPlaneStatus.Kind = controlPlane.Kind
			controlPlaneStatus.Namespace = controlPlane.Namespace
			controlPlaneStatus.Name = controlPlane.Name
			controlPlaneStatus.IPFamilyMode = controlPlane.GetAnnotations()[names.NetworkIPFamilyModeAnnotation]
			controlPlaneStatus.Version = controlPlane.GetAnnotations()["release.openshift.io/version"]
			controlPlaneStatus.Progressing = deploymentProgressing(controlPlane)

			if tc.prepull != "" {
				prepuller = &appsv1.DaemonSet{}
				err = yaml.Unmarshal([]byte(tc.prepull), prepuller)
				if err != nil {
					t.Fatal(err)
				}
				prepullerStatus.Kind = prepuller.Kind
				prepullerStatus.Namespace = prepuller.Namespace
				prepullerStatus.Name = prepuller.Name
				prepullerStatus.IPFamilyMode = prepuller.GetAnnotations()[names.NetworkIPFamilyModeAnnotation]
				prepullerStatus.Version = prepuller.GetAnnotations()["release.openshift.io/version"]
				prepullerStatus.Progressing = daemonSetProgressing(prepuller, false)
			} else {
				prepullerStatus = nil
			}

			bootstrapResult := fakeBootstrapResult()
			bootstrapResult.OVN = bootstrap.OVNBootstrapResult{
				MasterAddresses:          []string{"1.2.3.4", "5.6.7.8", "9.10.11.12"},
				ControlPlaneUpdateStatus: controlPlaneStatus,
				NodeUpdateStatus:         nodeStatus,
				OVNKubernetesConfig: &bootstrap.OVNConfigBoostrapResult{
					DpuHostModeLabel:     OVN_NODE_SELECTOR_DEFAULT_DPU_HOST,
					DpuModeLabel:         OVN_NODE_SELECTOR_DEFAULT_DPU,
					SmartNicModeLabel:    OVN_NODE_SELECTOR_DEFAULT_SMART_NIC,
					MgmtPortResourceName: "",
					HyperShiftConfig: &bootstrap.OVNHyperShiftBootstrapResult{
						Enabled: false,
					},
				},
				PrePullerUpdateStatus: prepullerStatus,
			}
			featureGatesCNO := featuregates.NewFeatureGate([]configv1.FeatureGateName{configv1.FeatureGateAdminNetworkPolicy}, []configv1.FeatureGateName{})

			fakeClient := cnofake.NewFakeClient()
			objs, _, err := renderOVNKubernetes(config, bootstrapResult, manifestDirOvn, fakeClient, featureGatesCNO)
			g.Expect(err).NotTo(HaveOccurred())

			renderedNode := findInObjs("apps", "DaemonSet", "ovnkube-node", "openshift-ovn-kubernetes", objs)
			_, preserveNode := renderedNode.GetAnnotations()[names.CreateOnlyAnnotation]
			renderedControlPlane := findInObjs("apps", "Deployment", "ovnkube-control-plane", "openshift-ovn-kubernetes", objs)
			_, preserveControlPlane := renderedControlPlane.GetAnnotations()[names.CreateOnlyAnnotation]
			renderedPrePuller := findInObjs("apps", "DaemonSet", "ovnkube-upgrades-prepuller", "openshift-ovn-kubernetes", objs)

			// if we expect a node update, the original node and the rendered one must be different
			g.Expect(tc.expectNode).To(Equal(!preserveNode), "Check node rendering")
			// if we expect a controlPlane update, the original controlPlane and the rendered one must be different
			g.Expect(tc.expectControlPlane).To(Equal(!preserveControlPlane), "Check controlPlane rendering")
			// if we expect a prepuller update, the original prepuller and the rendered one must be different
			g.Expect(tc.expectPrePull).To(Equal(renderedPrePuller != nil), "Check prepuller rendering")

			// All the containers in the pre-puller should use the IfNotPresent image pull policy:
			if renderedPrePuller != nil {
				checkDaemonSetImagePullPolicy(g, renderedPrePuller)
			}

			updateNode, updateControlPlane := shouldUpdateOVNKonUpgrade(bootstrapResult.OVN, controlPlaneStatus, tc.rv)
			g.Expect(updateControlPlane).To(Equal(tc.expectControlPlane), "Check controlPlane")
			if updateNode {
				var updatePrePuller bool
				updateNode, updatePrePuller = shouldUpdateOVNKonPrepull(bootstrapResult.OVN, tc.rv)
				g.Expect(updatePrePuller).To(Equal(tc.expectPrePull), "Check prepuller")
			}
			g.Expect(updateNode).To(Equal(tc.expectNode), "Check node")
		})
	}
}

func TestShouldUpdateOVNKonIPFamilyChange(t *testing.T) {

	for _, tc := range []struct {
		name               string
		node               *appsv1.DaemonSet
		controlPlane       *appsv1.Deployment
		ipFamilyMode       string
		expectNode         bool
		expectControlPlane bool
	}{
		{
			name:               "all empty",
			node:               &appsv1.DaemonSet{},
			controlPlane:       &appsv1.Deployment{},
			expectNode:         true,
			expectControlPlane: true,
			ipFamilyMode:       names.IPFamilySingleStack,
		},
		{
			name:               "fresh cluster",
			node:               &appsv1.DaemonSet{},
			controlPlane:       &appsv1.Deployment{},
			expectNode:         true,
			expectControlPlane: true,
			ipFamilyMode:       names.IPFamilySingleStack,
		},
		{
			name: "no configuration change",
			node: &appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ovnkube-node",
					Namespace: "openshift-ovn-kubernetes",
					Annotations: map[string]string{
						names.NetworkIPFamilyModeAnnotation: names.IPFamilySingleStack,
					},
				},
			},
			controlPlane: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ovnkube-control-plane",
					Namespace: "openshift-ovn-kubernetes",
					Annotations: map[string]string{
						names.NetworkIPFamilyModeAnnotation: names.IPFamilySingleStack,
					},
					Generation: 1,
				},

				Status: appsv1.DeploymentStatus{
					Replicas:           3,
					AvailableReplicas:  3,
					ReadyReplicas:      3,
					ObservedGeneration: 2,
					UpdatedReplicas:    3,
				},
			},
			expectNode:         true,
			expectControlPlane: true,
			ipFamilyMode:       names.IPFamilySingleStack,
		},
		{
			name: "configuration changed",
			node: &appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ovnkube-node",
					Namespace: "openshift-ovn-kubernetes",
					Annotations: map[string]string{
						names.NetworkIPFamilyModeAnnotation: names.IPFamilySingleStack,
					},
				},
			},
			controlPlane: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ovnkube-control-plane",
					Namespace: "openshift-ovn-kubernetes",
					Annotations: map[string]string{
						names.NetworkIPFamilyModeAnnotation: names.IPFamilySingleStack,
					},
				},
			},
			expectNode:         false,
			expectControlPlane: true,
			ipFamilyMode:       names.IPFamilyDualStack,
		},
		{
			name: "configuration changed, controlPlane updated and node remaining",
			node: &appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ovnkube-node",
					Namespace: "openshift-ovn-kubernetes",
					Annotations: map[string]string{
						names.NetworkIPFamilyModeAnnotation: names.IPFamilySingleStack,
					},
				},
			},
			controlPlane: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ovnkube-control-plane",
					Namespace: "openshift-ovn-kubernetes",
					Annotations: map[string]string{
						names.NetworkIPFamilyModeAnnotation: names.IPFamilyDualStack,
					},
					Generation: 1,
				},
				Status: appsv1.DeploymentStatus{
					Replicas:           3,
					AvailableReplicas:  3,
					ReadyReplicas:      3,
					UpdatedReplicas:    3,
					ObservedGeneration: 2,
				},
			},
			expectNode:         true,
			expectControlPlane: true,
			ipFamilyMode:       names.IPFamilyDualStack,
		},
		{
			name: "configuration changed, controlPlane updated and node remaining but still rolling out",
			node: &appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ovnkube-node",
					Namespace: "openshift-ovn-kubernetes",
					Annotations: map[string]string{
						names.NetworkIPFamilyModeAnnotation: names.IPFamilySingleStack,
					},
				},
			},
			controlPlane: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ovnkube-control-plane",
					Namespace: "openshift-ovn-kubernetes",
					Annotations: map[string]string{
						names.NetworkIPFamilyModeAnnotation: names.IPFamilyDualStack,
					},
					Generation: 1,
				},
				Status: appsv1.DeploymentStatus{
					Replicas:            3,
					AvailableReplicas:   2,
					UnavailableReplicas: 1,
					ReadyReplicas:       2,
					ObservedGeneration:  2,
					UpdatedReplicas:     3,
				},
			},
			expectNode:         false,
			expectControlPlane: true,
			ipFamilyMode:       names.IPFamilyDualStack,
		},
		// this should not be possible, because configuration changes always update controlPlane first
		{
			name: "configuration changed, node updated and controlPlane remaining",
			node: &appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ovnkube-node",
					Namespace: "openshift-ovn-kubernetes",
					Annotations: map[string]string{
						names.NetworkIPFamilyModeAnnotation: names.IPFamilyDualStack,
					},
				},
			},
			controlPlane: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ovnkube-control-plane",
					Namespace: "openshift-ovn-kubernetes",
					Annotations: map[string]string{
						names.NetworkIPFamilyModeAnnotation: names.IPFamilySingleStack,
					},
					Generation: 2,
				},
				Status: appsv1.DeploymentStatus{
					Replicas:           3,
					AvailableReplicas:  3,
					ReadyReplicas:      3,
					ObservedGeneration: 2,
					UpdatedReplicas:    3,
				},
			},
			expectNode:         false,
			expectControlPlane: true,
			ipFamilyMode:       names.IPFamilyDualStack,
		},
	} {

		t.Run(tc.name, func(t *testing.T) {
			controlPlaneStatus := &bootstrap.OVNUpdateStatus{}
			nodeStatus := &bootstrap.OVNUpdateStatus{}
			if tc.controlPlane != nil {
				controlPlaneStatus.IPFamilyMode = tc.controlPlane.GetAnnotations()[names.NetworkIPFamilyModeAnnotation]
				controlPlaneStatus.Progressing = deploymentProgressing(tc.controlPlane)
			}
			if tc.node != nil {
				nodeStatus.IPFamilyMode = tc.node.GetAnnotations()[names.NetworkIPFamilyModeAnnotation]
			}
			bootResult := bootstrap.OVNBootstrapResult{
				ControlPlaneUpdateStatus: controlPlaneStatus,
				NodeUpdateStatus:         nodeStatus,
			}
			updateNode, updateControlPlane := shouldUpdateOVNKonIPFamilyChange(bootResult, controlPlaneStatus, tc.ipFamilyMode)
			if updateNode != tc.expectNode {
				t.Errorf("Expected node update: %v received %v", tc.expectNode, updateNode)
			}
			if updateControlPlane != tc.expectControlPlane {
				t.Errorf("Expected node update: %v received %v", tc.expectNode, updateNode)
			}

		})
	}

}

func TestRenderOVNKubernetesDualStackPrecedenceOverUpgrade(t *testing.T) {
	//cluster was in single-stack and receives a converts to dual-stack
	config := &operv1.NetworkSpec{
		ServiceNetwork: []string{"172.30.0.0/16", "fd00:3:2:1::/112"},
		ClusterNetwork: []operv1.ClusterNetworkEntry{
			{
				CIDR:       "10.128.0.0/15",
				HostPrefix: 23,
			},
			{
				CIDR:       "fd00:1:2:3::/64",
				HostPrefix: 56,
			},
		},
		DefaultNetwork: operv1.DefaultNetworkDefinition{
			Type: operv1.NetworkTypeOVNKubernetes,
			OVNKubernetesConfig: &operv1.OVNKubernetesConfig{
				GenevePort: ptrToUint32(8061),
			},
		},
	}
	errs := validateOVNKubernetes(config)
	if len(errs) > 0 {
		t.Errorf("Unexpected error: %v", errs)
	}
	fillDefaults(config, nil)

	// at the same time we have an upgrade
	t.Setenv("RELEASE_VERSION", "2.0.0")

	// bootstrap also represents current status
	// the current cluster is single-stack and has version 1.9.9
	bootstrapResult := fakeBootstrapResult()
	bootstrapResult.OVN = bootstrap.OVNBootstrapResult{
		MasterAddresses: []string{"1.2.3.4", "5.6.7.8", "9.10.11.12"},
		ControlPlaneUpdateStatus: &bootstrap.OVNUpdateStatus{
			Kind:         "Deployment",
			Namespace:    "openshift-ovn-kubernetes",
			Name:         "ovnkube-control-plane",
			Version:      "1.9.9",
			IPFamilyMode: names.IPFamilySingleStack,
		},
		NodeUpdateStatus: &bootstrap.OVNUpdateStatus{
			Kind:         "DaemonSet",
			Namespace:    "openshift-ovn-kubernetes",
			Name:         "ovnkube-node",
			Version:      "1.9.9",
			IPFamilyMode: names.IPFamilySingleStack,
		},
		OVNKubernetesConfig: &bootstrap.OVNConfigBoostrapResult{
			DpuHostModeLabel:     OVN_NODE_SELECTOR_DEFAULT_DPU_HOST,
			DpuModeLabel:         OVN_NODE_SELECTOR_DEFAULT_DPU,
			SmartNicModeLabel:    OVN_NODE_SELECTOR_DEFAULT_SMART_NIC,
			MgmtPortResourceName: "",
			HyperShiftConfig: &bootstrap.OVNHyperShiftBootstrapResult{
				Enabled: false,
			},
		},
	}
	featureGatesCNO := featuregates.NewFeatureGate([]configv1.FeatureGateName{configv1.FeatureGateAdminNetworkPolicy}, []configv1.FeatureGateName{})

	// the new rendered config should hold the node to do the dualstack conversion
	// the upgrade code holds the controlPlanes to update the nodes first
	fakeClient := cnofake.NewFakeClient()
	objs, _, err := renderOVNKubernetes(config, bootstrapResult, manifestDirOvn, fakeClient, featureGatesCNO)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	renderedNode := findInObjs("apps", "DaemonSet", "ovnkube-node", "openshift-ovn-kubernetes", objs)
	renderedControlPlane := findInObjs("apps", "Deployment", "ovnkube-control-plane", "openshift-ovn-kubernetes", objs)

	// the node has to be the same
	if _, ok := renderedNode.GetAnnotations()[names.CreateOnlyAnnotation]; !ok {
		t.Errorf("node DaemonSet should have create-only annotation, does not")
	}
	// the controlPlane has to use the new annotations for dual-stack so it has to be mutated
	if _, ok := renderedControlPlane.GetAnnotations()[names.CreateOnlyAnnotation]; ok {
		t.Errorf("controlPlane daemonset are equal, dual-stack should modify controlPlanes")
	}
}

// Check that, all throughout the upgrade from 4.13 to 4.14, the 4.13 ovn-ipsec DaemonSet is rendered
// up until the upgrade is over, at which point the two 4.14 ipsec DaemonSets (ovn-ipsec-host and ovn-ipsec-containerized)
// can finally be deployed. The subtlety in the implementation is that all three daemonsets are always rendered during the upgrade,
// but with the "create-wait" annotation, so that the running ovn-ipsec is not replaced and the two 4.14 DSs are not deployed.
// Once the upgrade is over, ovn-ipsec no longer rendered and the two 4.14 DSs are rendered without the annotation.
func TestRenderOVNKubernetesIPsecUpgradeFrom413(t *testing.T) {
	checkDaemonsetIsRendered := func(objs []*uns.Unstructured, daemonsetName, daemonsetNamespace string, mustExist, mustHaveCreateWaitAnnotation bool, errMessage string) {
		renderedDaemonset := findInObjs("apps", "DaemonSet", daemonsetName, daemonsetNamespace, objs)
		if renderedDaemonset == nil && mustExist {
			// daemonset wasn't rendered, but should have been
			t.Errorf("[%s] %s DaemonSet not rendered by renderOVNKubernetes", errMessage, daemonsetName)
		}
		if renderedDaemonset != nil {
			if !mustExist {
				// daemonset was rendered, but should not have been
				t.Errorf("[%s] %s DaemonSet rendered by renderOVNKubernetes, but should not have been", errMessage, daemonsetName)
			}

			_, hasAnnotation := renderedDaemonset.GetAnnotations()[names.CreateWaitAnnotation]
			if mustHaveCreateWaitAnnotation && !hasAnnotation {
				t.Errorf("[%s] %s DaemonSet should have create-wait annotation, but it does not", errMessage, daemonsetName)
			}
			if !mustHaveCreateWaitAnnotation && hasAnnotation {
				t.Errorf("[%s] %s DaemonSet should not have create-wait annotation, but it has it", errMessage, daemonsetName)
			}
		}
	}

	config := &operv1.NetworkSpec{
		ServiceNetwork: []string{"172.30.0.0/16", "fd00:3:2:1::/112"},
		ClusterNetwork: []operv1.ClusterNetworkEntry{
			{
				CIDR:       "10.128.0.0/15",
				HostPrefix: 23,
			},
			{
				CIDR:       "fd00:1:2:3::/64",
				HostPrefix: 56,
			},
		},
		DefaultNetwork: operv1.DefaultNetworkDefinition{
			Type: operv1.NetworkTypeOVNKubernetes,
			OVNKubernetesConfig: &operv1.OVNKubernetesConfig{
				GenevePort:  ptrToUint32(8061),
				IPsecConfig: &operv1.IPsecConfig{},
			},
		},
	}
	errs := validateOVNKubernetes(config)
	if len(errs) > 0 {
		t.Errorf("Unexpected error: %v", errs)
	}
	fillDefaults(config, nil)

	t.Setenv("RELEASE_VERSION", "4.14.0") // upgrading to 4.14.0

	// bootstrap represents the current status
	// the current cluster is single-stack and is on version 4.13.0
	bootstrapResult := fakeBootstrapResult()
	bootstrapResult.Infra = bootstrap.InfraStatus{}
	bootstrapResult.OVN = bootstrap.OVNBootstrapResult{
		MasterAddresses: []string{"1.2.3.4"},
		MasterUpdateStatus: &bootstrap.OVNUpdateStatus{
			Kind:                "Daemonset",
			Namespace:           util.OVN_NAMESPACE,
			Name:                util.OVN_MASTER,
			Version:             "4.13.0",
			IPFamilyMode:        names.IPFamilySingleStack,
			InterConnectEnabled: false,
		},
		NodeUpdateStatus: &bootstrap.OVNUpdateStatus{
			Kind:                 "DaemonSet",
			Namespace:            util.OVN_NAMESPACE,
			Name:                 util.OVN_NODE,
			Version:              "4.13.0",
			IPFamilyMode:         names.IPFamilySingleStack,
			InterConnectEnabled:  false,
			InterConnectZoneMode: "singlezone",
		},
		IPsecUpdateStatus: &bootstrap.OVNUpdateStatus{
			Kind:         "Daemonset",
			Namespace:    util.OVN_NAMESPACE,
			Name:         util.OVN_IPSEC,
			IPFamilyMode: names.IPFamilySingleStack,
			Version:      "4.13.0",
		},
		OVNKubernetesConfig: &bootstrap.OVNConfigBoostrapResult{
			DpuHostModeLabel:     OVN_NODE_SELECTOR_DEFAULT_DPU_HOST,
			DpuModeLabel:         OVN_NODE_SELECTOR_DEFAULT_DPU,
			SmartNicModeLabel:    OVN_NODE_SELECTOR_DEFAULT_SMART_NIC,
			MgmtPortResourceName: "",
			HyperShiftConfig: &bootstrap.OVNHyperShiftBootstrapResult{
				Enabled: false,
			},
		},
	}

	fmt.Println("\n============= Upgrade starts, CNO pushes interconnect configmap and starts rolling out ovnkube-node")
	errMsg := "upgrade from 4.13 starts"

	featureGatesCNO := featuregates.NewFeatureGate([]configv1.FeatureGateName{configv1.FeatureGateAdminNetworkPolicy}, []configv1.FeatureGateName{})
	fakeClient := cnofake.NewFakeClient()

	objs, _, err := renderOVNKubernetes(config, bootstrapResult, manifestDirOvn, fakeClient, featureGatesCNO)
	if err != nil {
		t.Errorf("[%s] Unexpected error: %v", errMsg, err)
	}

	checkDaemonsetIsRendered(objs, util.OVN_IPSEC_HOST, util.OVN_NAMESPACE, true, true, errMsg)
	checkDaemonsetIsRendered(objs, util.OVN_IPSEC_CONTAINERIZED, util.OVN_NAMESPACE, true, true, errMsg)
	checkDaemonsetIsRendered(objs, util.OVN_IPSEC, util.OVN_NAMESPACE, true, true, errMsg)

	// Move to phase 1 of the upgrade to 4.14: ovnk master and node are deployed with 4.14 image
	// The 4.13 ipsec daemonset should still be rendered along with the 4.14 ones.
	fmt.Println("\n============= phase 1 of the upgrade is ongoing (roll out of ovnk master)")
	errMsg = "phase 1 ongoing"
	bootstrapResult.OVN.MasterUpdateStatus.Version = "4.14.0"
	bootstrapResult.OVN.MasterUpdateStatus.InterConnectEnabled = true
	bootstrapResult.OVN.MasterUpdateStatus.Progressing = true

	bootstrapResult.OVN.NodeUpdateStatus.Version = "4.14.0"
	bootstrapResult.OVN.NodeUpdateStatus.InterConnectEnabled = true

	objs, _, err = renderOVNKubernetes(config, bootstrapResult, manifestDirOvn, fakeClient, featureGatesCNO)
	if err != nil {
		t.Errorf("[%s] Unexpected error: %v", errMsg, err)
	}

	checkDaemonsetIsRendered(objs, util.OVN_IPSEC_HOST, util.OVN_NAMESPACE, true, true, errMsg)
	checkDaemonsetIsRendered(objs, util.OVN_IPSEC_CONTAINERIZED, util.OVN_NAMESPACE, true, true, errMsg)
	checkDaemonsetIsRendered(objs, util.OVN_IPSEC, util.OVN_NAMESPACE, true, true, errMsg)

	// move to phase 2 of the upgrade: ovnk master, ovnk node, ovnk control plane are all up
	// The 4.13 ipsec daemonset should still be rendered along with the 4.14 ones.
	fmt.Println("\n============= phase 1 is done, phase 2 is about to start; will start roll out of ovnkube node")
	errMsg = "end of phase 1, will start phase 2"

	bootstrapResult.OVN.NodeUpdateStatus.Progressing = false
	bootstrapResult.OVN.MasterUpdateStatus.Progressing = false

	objs, _, err = renderOVNKubernetes(config, bootstrapResult, manifestDirOvn, fakeClient, featureGatesCNO)
	if err != nil {
		t.Errorf("[%s] Unexpected error: %v", errMsg, err)
	}
	checkDaemonsetIsRendered(objs, util.OVN_IPSEC_HOST, util.OVN_NAMESPACE, true, true, errMsg)
	checkDaemonsetIsRendered(objs, util.OVN_IPSEC_CONTAINERIZED, util.OVN_NAMESPACE, true, true, errMsg)
	checkDaemonsetIsRendered(objs, util.OVN_IPSEC, util.OVN_NAMESPACE, true, true, errMsg)

	// move to phase 2 of the upgrade: ovnk master, ovnk node, ovnk control plane are all up
	// The 4.13 ipsec daemonset should still be rendered along with the 4.14 ones.
	fmt.Println("\n============= phase 2 is ongoing: roll out of ovnk control plane")
	errMsg = "phase 2 ongoing"

	bootstrapResult.OVN.ControlPlaneUpdateStatus = &bootstrap.OVNUpdateStatus{
		Kind:         "Deployment",
		Namespace:    util.OVN_NAMESPACE,
		Name:         util.OVN_CONTROL_PLANE,
		Version:      "4.14.0",
		IPFamilyMode: names.IPFamilySingleStack,
		Progressing:  true,
	}

	bootstrapResult.OVN.NodeUpdateStatus.InterConnectZoneMode = "multizone" // ovnkube-node roll out is done and it's now on multizone

	objs, _, err = renderOVNKubernetes(config, bootstrapResult, manifestDirOvn, fakeClient, featureGatesCNO)
	if err != nil {
		t.Errorf("[%s] Unexpected error: %v", errMsg, err)
	}

	checkDaemonsetIsRendered(objs, util.OVN_IPSEC_HOST, util.OVN_NAMESPACE, true, true, errMsg)
	checkDaemonsetIsRendered(objs, util.OVN_IPSEC_CONTAINERIZED, util.OVN_NAMESPACE, true, true, errMsg)
	checkDaemonsetIsRendered(objs, util.OVN_IPSEC, util.OVN_NAMESPACE, true, true, errMsg)

	// move to the end of the upgrade: ovnk master is gone, ovnk node, ovnk control plane are up.
	// The 4.13 ipsec daemonset is gone; the two 4.14 ipsec daemonsets are finally rendered without
	// the create-wait annotation.
	fmt.Println("\n============= end of phase 2, will remove ovnkube master")
	errMsg = "end of phase 2"

	bootstrapResult.OVN.ControlPlaneUpdateStatus.Progressing = false // roll out of control plane is done

	// HACK: at the end the upgrade to OVN IC, we temporarily annotate ovnkube-node so that CNO can annotate it
	nodeDaemonSet := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{
		Name: util.OVN_NODE, Namespace: util.OVN_NAMESPACE, Annotations: map[string]string{}}}
	_, err = fakeClient.Default().Kubernetes().AppsV1().DaemonSets(util.OVN_NAMESPACE).Create(context.TODO(), nodeDaemonSet, metav1.CreateOptions{})
	if err != nil {
		t.Errorf("[%s] Unexpected error when creating ovnkube-node: %v", errMsg, err)
	}

	objs, _, err = renderOVNKubernetes(config, bootstrapResult, manifestDirOvn, fakeClient, featureGatesCNO)
	if err != nil {
		t.Errorf("[%s] Unexpected error: %v", errMsg, err)
	}

	checkDaemonsetIsRendered(objs, util.OVN_IPSEC, util.OVN_NAMESPACE, false, false, errMsg)              // ovn-ipsec is not rendered anymore
	checkDaemonsetIsRendered(objs, util.OVN_IPSEC_HOST, util.OVN_NAMESPACE, true, false, errMsg)          // ovn-ipsec-host is rendered without the create-wait annotation
	checkDaemonsetIsRendered(objs, util.OVN_IPSEC_CONTAINERIZED, util.OVN_NAMESPACE, true, false, errMsg) // ditto for ovn-ipsec-containerized

	fmt.Println("\n============= any further CNO iteration after the upgrade")
	errMsg = "iteration after upgrade"

	bootstrapResult.OVN.MasterUpdateStatus = nil // ovnk master is gone

	bootstrapResult.OVN.IPsecUpdateStatus.Name = util.OVN_IPSEC_HOST // ovn-ipsec-host now replaced ovn-ipsec
	bootstrapResult.OVN.IPsecUpdateStatus.Version = "4.14.0"

	objs, _, err = renderOVNKubernetes(config, bootstrapResult, manifestDirOvn, fakeClient, featureGatesCNO)
	if err != nil {
		t.Errorf("[%s] Unexpected error: %v", errMsg, err)
	}
	checkDaemonsetIsRendered(objs, util.OVN_IPSEC, util.OVN_NAMESPACE, false, false, errMsg)              // ovn-ipsec is not rendered anymore
	checkDaemonsetIsRendered(objs, util.OVN_IPSEC_HOST, util.OVN_NAMESPACE, true, false, errMsg)          // ovn-ipsec-host is rendered without the create-wait annotation
	checkDaemonsetIsRendered(objs, util.OVN_IPSEC_CONTAINERIZED, util.OVN_NAMESPACE, true, false, errMsg) // ditto for ovn-ipsec-containerized

}

func TestRenderOVNKubernetesOVSFlowsConfigMap(t *testing.T) {
	config := &operv1.NetworkSpec{
		ServiceNetwork: []string{"172.30.0.0/16"},
		ClusterNetwork: []operv1.ClusterNetworkEntry{
			{CIDR: "10.128.0.0/15", HostPrefix: 23},
		},
		DefaultNetwork: operv1.DefaultNetworkDefinition{
			Type: operv1.NetworkTypeOVNKubernetes,
			OVNKubernetesConfig: &operv1.OVNKubernetesConfig{
				GenevePort:        ptrToUint32(8061),
				PolicyAuditConfig: &operv1.PolicyAuditConfig{},
			},
		},
		DisableMultiNetwork: boolPtr(true),
	}
	testCases := []struct {
		Description string
		FlowsConfig *bootstrap.FlowsConfig
		Expected    []v1.EnvVar
		NotExpected []string
	}{
		{
			Description: "No detected OVN flows config",
			NotExpected: []string{"IPFIX_COLLECTORS", "IPFIX_CACHE_MAX_FLOWS",
				"IPFIX_CACHE_ACTIVE_TIMEOUT", "IPFIX_SAMPLING"},
		},
		{
			Description: "Only target is specified",
			FlowsConfig: &bootstrap.FlowsConfig{
				Target: "1.2.3.4:567",
			},
			Expected: []v1.EnvVar{{Name: "IPFIX_COLLECTORS", Value: "1.2.3.4:567"}},
			NotExpected: []string{"IPFIX_CACHE_MAX_FLOWS",
				"IPFIX_CACHE_ACTIVE_TIMEOUT", "IPFIX_SAMPLING"},
		},
		{
			Description: "IPFIX performance variables are specified",
			FlowsConfig: &bootstrap.FlowsConfig{
				Target:             "7.8.9.10:1112",
				CacheMaxFlows:      uintPtr(123),
				CacheActiveTimeout: uintPtr(456),
				Sampling:           uintPtr(789),
			},
			Expected: []v1.EnvVar{
				{Name: "IPFIX_COLLECTORS", Value: "7.8.9.10:1112"},
				{Name: "IPFIX_CACHE_MAX_FLOWS", Value: "123"},
				{Name: "IPFIX_CACHE_ACTIVE_TIMEOUT", Value: "456"},
				{Name: "IPFIX_SAMPLING", Value: "789"},
			},
		},
		{
			Description: "Wrong configuration: target missing but performance variables present",
			FlowsConfig: &bootstrap.FlowsConfig{
				CacheMaxFlows:      uintPtr(123),
				CacheActiveTimeout: uintPtr(456),
				Sampling:           uintPtr(789),
			},
			NotExpected: []string{"IPFIX_COLLECTORS", "IPFIX_CACHE_MAX_FLOWS",
				"IPFIX_CACHE_ACTIVE_TIMEOUT", "IPFIX_SAMPLING"},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.Description, func(t *testing.T) {
			RegisterTestingT(t)
			g := NewGomegaWithT(t)
			bootstrapResult := fakeBootstrapResult()
			bootstrapResult.OVN = bootstrap.OVNBootstrapResult{
				MasterAddresses: []string{"1.2.3.4"},
				OVNKubernetesConfig: &bootstrap.OVNConfigBoostrapResult{
					GatewayMode: "shared",
					HyperShiftConfig: &bootstrap.OVNHyperShiftBootstrapResult{
						Enabled: false,
					},
				},
				FlowsConfig: tc.FlowsConfig,
			}
			featureGatesCNO := featuregates.NewFeatureGate([]configv1.FeatureGateName{configv1.FeatureGateAdminNetworkPolicy}, []configv1.FeatureGateName{})
			fakeClient := cnofake.NewFakeClient()
			objs, _, err := renderOVNKubernetes(config, bootstrapResult, manifestDirOvn, fakeClient, featureGatesCNO)
			g.Expect(err).ToNot(HaveOccurred())
			nodeDS := findInObjs("apps", "DaemonSet", "ovnkube-node", "openshift-ovn-kubernetes", objs)
			ds := appsv1.DaemonSet{}
			g.Expect(convert(nodeDS, &ds)).To(Succeed())
			nodeCont, ok := findContainer(ds.Spec.Template.Spec.Containers, "ovnkube-node")
			if !ok {
				nodeCont, ok = findContainer(ds.Spec.Template.Spec.Containers, "ovnkube-controller")
			}
			g.Expect(ok).To(BeTrue(), "expecting container named ovnkube-node or ovnkube-controller in the DaemonSet")
			g.Expect(nodeCont.Env).To(ContainElements(tc.Expected))
			for _, ev := range nodeCont.Env {
				Expect(tc.NotExpected).ToNot(ContainElement(ev.Name))
			}
		})
	}
}

func TestBootStrapOvsConfigMap_SharedTarget(t *testing.T) {
	fc := bootstrapFlowsConfig(&fakeClientReader{
		configMap: &v1.ConfigMap{
			Data: map[string]string{
				"sharedTarget":       "1.2.3.4:3030",
				"cacheActiveTimeout": "3200ms",
				"cacheMaxFlows":      "33",
				"sampling":           "55",
			},
		},
	})

	assert.Equal(t, "1.2.3.4:3030", fc.Target)
	// verify that the 200ms get truncated
	assert.EqualValues(t, 3, *fc.CacheActiveTimeout)
	assert.EqualValues(t, 33, *fc.CacheMaxFlows)
	assert.EqualValues(t, 55, *fc.Sampling)
}

func TestBootStrapOvsConfigMap_NodePort(t *testing.T) {
	fc := bootstrapFlowsConfig(&fakeClientReader{
		configMap: &v1.ConfigMap{
			Data: map[string]string{
				"nodePort":           "3131",
				"cacheActiveTimeout": "invalid timeout",
				"cacheMaxFlows":      "invalid int",
			},
		},
	})

	assert.Equal(t, ":3131", fc.Target)
	// verify that invalid or unspecified fields are ignored
	assert.Nil(t, fc.CacheActiveTimeout)
	assert.Nil(t, fc.CacheMaxFlows)
	assert.Nil(t, fc.Sampling)
}

func TestBootStrapOvsConfigMap_IncompleteMap(t *testing.T) {
	fc := bootstrapFlowsConfig(&fakeClientReader{
		configMap: &v1.ConfigMap{
			Data: map[string]string{
				"cacheActiveTimeout": "3200ms",
				"cacheMaxFlows":      "33",
				"sampling":           "55",
			},
		},
	})

	// without sharedTarget nor nodePort, flow collection can't be set
	assert.Nil(t, fc)
}

func TestBootStrapOvsConfigMap_UnexistingMap(t *testing.T) {
	fc := bootstrapFlowsConfig(&fakeClientReader{configMap: nil})

	// without sharedTarget nor nodePort, flow collection can't be set
	assert.Nil(t, fc)
}

func Test_getDisableUDPAggregation(t *testing.T) {
	var disable bool

	disable = getDisableUDPAggregation(&fakeClientReader{configMap: nil})
	assert.Equal(t, false, disable, "with no configmap")

	disable = getDisableUDPAggregation(&fakeClientReader{
		configMap: &v1.ConfigMap{
			Data: map[string]string{
				"friday": "2",
			},
		},
	})
	assert.Equal(t, false, disable, "with bad configmap")

	disable = getDisableUDPAggregation(&fakeClientReader{
		configMap: &v1.ConfigMap{
			Data: map[string]string{
				"disable-udp-aggregation": "false",
			},
		},
	})
	assert.Equal(t, false, disable, "with configmap that sets 'disable-udp-aggregation' to 'false'")

	disable = getDisableUDPAggregation(&fakeClientReader{
		configMap: &v1.ConfigMap{
			Data: map[string]string{
				"disable-udp-aggregation": "bad",
			},
		},
	})
	assert.Equal(t, false, disable, "with configmap that sets 'disable-udp-aggregation' to 'bad'")

	disable = getDisableUDPAggregation(&fakeClientReader{
		configMap: &v1.ConfigMap{
			Data: map[string]string{
				"disable-udp-aggregation": "true",
			},
		},
	})
	assert.Equal(t, true, disable, "with configmap that sets 'disable-udp-aggregation' to 'true'")
}

func makeNodes(ips ...string) []*v1.Node {
	nodes := make([]*v1.Node, 0, len(ips))
	created := time.Now()
	for i, ip := range ips {
		ipStr := strings.ReplaceAll(ip, ".", "")
		ipStr = strings.ReplaceAll(ipStr, ":", "")
		nodeName := fmt.Sprintf("node-%d-%s", i, ipStr)

		created = created.Add(1 * time.Minute)
		node := &v1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:              nodeName,
				Labels:            map[string]string{"node-role.kubernetes.io/master": ""},
				CreationTimestamp: metav1.NewTime(created),
			},
			Status: v1.NodeStatus{
				Addresses: []v1.NodeAddress{},
			},
		}
		if ip != "" {
			node.Status.Addresses = []v1.NodeAddress{{Type: v1.NodeInternalIP, Address: ip}}
		}
		nodes = append(nodes, node)
	}
	return nodes
}

func TestGetMasterAddresses(t *testing.T) {
	testCases := []struct {
		Description  string
		ReplicaCount int
		Nodes        []*v1.Node
		HypershiftNS string
		Expected     []string
		Err          bool
	}{
		{
			Description:  "Three masters",
			ReplicaCount: 3,
			Nodes:        makeNodes("1.2.3.4", "1.2.3.5", "1.2.3.6"),
			Expected:     []string{"1.2.3.4", "1.2.3.5", "1.2.3.6"},
		},
		{
			Description:  "Too many masters",
			ReplicaCount: 3,
			Nodes:        makeNodes("1.2.3.4", "1.2.3.5", "1.2.3.6", "1.2.3.7"),
			Expected:     []string{"1.2.3.4", "1.2.3.5", "1.2.3.6"},
		},
		{
			Description:  "Three IPv6 masters",
			ReplicaCount: 3,
			Nodes:        makeNodes("fd01::1", "fd01::2", "fd01::3"),
			Expected:     []string{"fd01::1", "fd01::2", "fd01::3"},
		},
		{
			Description:  "Too many IPv6 masters",
			ReplicaCount: 3,
			Nodes:        makeNodes("fd01::4", "fd01::5", "fd01::6", "fd01::7"),
			Expected:     []string{"fd01::4", "fd01::5", "fd01::6"},
		},
		{
			Description:  "Master without address",
			ReplicaCount: 3,
			Nodes:        makeNodes("1.2.3.4", "1.2.3.5", ""),
			Err:          true,
		},
		{
			Description:  "Timeout because fewer masters than expected",
			ReplicaCount: 3,
			Nodes:        makeNodes("1.2.3.4", "1.2.3.5"),
			Expected:     []string{"1.2.3.4", "1.2.3.5"},
		},
		{
			Description:  "Only one master",
			ReplicaCount: 1,
			Nodes:        makeNodes("1.2.3.4"),
			Expected:     []string{"1.2.3.4"},
		},
		{
			Description:  "Hypershift",
			ReplicaCount: 1,
			HypershiftNS: "blahblah",
			Expected:     []string{"ovnkube-master-0.ovnkube-master-internal.blahblah.svc.cluster.local"},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.Description, func(t *testing.T) {
			t.Cleanup(func() {
				os.Unsetenv("HOSTED_CLUSTER_NAMESPACE")
			})
			if tc.HypershiftNS != "" {
				os.Setenv("HOSTED_CLUSTER_NAMESPACE", tc.HypershiftNS)
			}

			objects := make([]crclient.Object, 0, len(tc.Nodes))
			for _, node := range tc.Nodes {
				objects = append(objects, node)
			}

			client := crfake.NewClientBuilder().WithObjects(objects...).Build()
			foundAddrs, _, err := getMasterAddresses(client, tc.ReplicaCount, tc.HypershiftNS != "", 5)
			if tc.Err {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.ElementsMatch(t, foundAddrs, tc.Expected)
			}
		})
	}
}

type fakeClientReader struct {
	configMap *v1.ConfigMap
}

func (f *fakeClientReader) Get(_ context.Context, _ crclient.ObjectKey, obj crclient.Object, opts ...crclient.GetOption) error {
	if cmPtr, ok := obj.(*v1.ConfigMap); !ok {
		return fmt.Errorf("expecting *v1.ConfigMap, got %T", obj)
	} else if f.configMap == nil {
		return &kapierrors.StatusError{ErrStatus: metav1.Status{
			Reason: metav1.StatusReasonNotFound,
		}}
	} else {
		*cmPtr = *f.configMap
	}
	return nil
}

func (f *fakeClientReader) List(_ context.Context, _ crclient.ObjectList, _ ...crclient.ListOption) error {
	return errors.New("unexpected invocation to List")
}

func findContainer(conts []v1.Container, name string) (v1.Container, bool) {
	for _, cont := range conts {
		if cont.Name == name {
			return cont, true
		}
	}
	return v1.Container{}, false
}

func convert(src *uns.Unstructured, dst metav1.Object) error {
	j, err := src.MarshalJSON()
	if err != nil {
		return err
	}
	return json.Unmarshal(j, dst)
}

func findInObjs(group, kind, name, namespace string, objs []*uns.Unstructured) *uns.Unstructured {
	for _, obj := range objs {
		if (obj.GroupVersionKind().GroupKind() == schema.GroupKind{Group: group, Kind: kind} &&
			obj.GetNamespace() == namespace &&
			obj.GetName() == name) {
			return obj
		}
	}
	return nil
}

func extractOVNKubeConfig(g *WithT, objs []*uns.Unstructured) string {
	for _, obj := range objs {
		if obj.GetKind() == "ConfigMap" && obj.GetName() == "ovnkube-config" {
			val, ok, err := uns.NestedString(obj.Object, "data", "ovnkube.conf")
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(ok).To(BeTrue())
			return val
		}
	}
	return ""
}

// checkDaemonsetAnnotation check that all the daemonset have the annotation with the
// same key and value
func checkDaemonsetAnnotation(g *WithT, objs []*uns.Unstructured, key, value string) bool {
	if key == "" || value == "" {
		return false
	}
	foundControlPlane, foundNode := false, false
	for _, obj := range objs {
		if obj.GetAPIVersion() == "apps/v1" &&
			(obj.GetName() == "ovnkube-control-plane" && obj.GetKind() == "Deployment" ||
				obj.GetName() == "ovnkube-node" && obj.GetKind() == "DaemonSet") {

			// check daemonset annotation
			anno := obj.GetAnnotations()
			if anno == nil {
				return false
			}
			v, ok := anno[key]
			if !ok || v != value {
				return false
			}
			// check template annotation
			anno, _, _ = uns.NestedStringMap(obj.Object, "spec", "template", "metadata", "annotations")
			if anno == nil {
				return false
			}
			v, ok = anno[key]
			if !ok || v != value {
				return false
			}
			// record the daemonsets we have checked
			if obj.GetName() == "ovnkube-control-plane" {
				foundControlPlane = true
			} else {
				foundNode = true
			}
		}
	}
	return foundControlPlane && foundNode
}

func checkDaemonSetImagePullPolicy(g *WithT, obj *uns.Unstructured) {
	initContainers, _, err := uns.NestedSlice(obj.Object, "spec", "template", "spec", "initContainers")
	g.Expect(err).ToNot(HaveOccurred())
	checkContainersImagePullPolicy(g, initContainers)
	containers, _, err := uns.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	g.Expect(err).ToNot(HaveOccurred())
	checkContainersImagePullPolicy(g, containers)
}

func checkContainersImagePullPolicy(g *WithT, containers []any) {
	for _, container := range containers {
		checkContainerImagePullPolicy(g, container.(map[string]any))
	}
}

func checkContainerImagePullPolicy(g *WithT, container map[string]any) {
	policy, _, err := uns.NestedString(container, "imagePullPolicy")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(policy).To(Equal(string(v1.PullIfNotPresent)))
}

func ptrToUint32(x uint32) *uint32 {
	return &x
}

func uintPtr(x uint) *uint {
	return &x
}

func boolPtr(x bool) *bool {
	return &x
}
