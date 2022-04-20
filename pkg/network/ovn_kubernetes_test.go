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

	"github.com/ghodss/yaml"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/assert"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	kapierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"github.com/openshift/cluster-network-operator/pkg/names"
)

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
			NodeMode: "full",
			HyperShiftConfig: &bootstrap.OVNHyperShiftBootstrapResult{
				Enabled: false,
			},
		},
	}

	objs, _, err := renderOVNKubernetes(config, bootstrapResult, manifestDirOvn)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-ovn-kubernetes", "ovnkube-node")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-ovn-kubernetes", "ovnkube-master")))

	// It's important that the namespace is first
	g.Expect(objs[0]).To(HaveKubernetesID("Namespace", "", "openshift-ovn-kubernetes"))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ClusterRole", "", "openshift-ovn-kubernetes-node")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ClusterRole", "", "openshift-ovn-kubernetes-controller")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ServiceAccount", "openshift-ovn-kubernetes", "ovn-kubernetes-node")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ServiceAccount", "openshift-ovn-kubernetes", "ovn-kubernetes-controller")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ClusterRoleBinding", "", "openshift-ovn-kubernetes-node")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-ovn-kubernetes", "ovnkube-master")))
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
			NodeMode: "full",
			HyperShiftConfig: &bootstrap.OVNHyperShiftBootstrapResult{
				Enabled: false,
			},
		},
	}
	objs, _, err := renderOVNKubernetes(config, bootstrapResult, manifestDirOvn)
	g.Expect(err).NotTo(HaveOccurred())

	script, err := findNBDBPostStart(objs)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(script).To(ContainSubstring("pssl:9641"))

	bootstrapResult = fakeBootstrapResult()
	bootstrapResult.OVN = bootstrap.OVNBootstrapResult{
		MasterAddresses: []string{"fd01::1", "fd01::2", "fd01::3"},
		OVNKubernetesConfig: &bootstrap.OVNConfigBoostrapResult{
			NodeMode: "full",
			HyperShiftConfig: &bootstrap.OVNHyperShiftBootstrapResult{
				Enabled: false,
			},
		},
	}
	objs, _, err = renderOVNKubernetes(config, bootstrapResult, manifestDirOvn)
	g.Expect(err).NotTo(HaveOccurred())

	script, err = findNBDBPostStart(objs)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(script).To(ContainSubstring("pssl:9641:[::]"))
}

func TestRenderedOVNKubernetesConfig(t *testing.T) {
	type testcase struct {
		desc                string
		expected            string
		hybridOverlayConfig *operv1.HybridOverlayConfig
		gatewayConfig       *operv1.GatewayConfig
		masterIPs           []string
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

[kubernetes]
service-cidrs="172.30.0.0/16"
ovn-config-namespace="openshift-ovn-kubernetes"
apiserver="https://testing.test:8443"
host-network-namespace="openshift-host-network"
platform-type="GCP"

[ovnkubernetesfeature]
enable-egress-ip=true
enable-egress-firewall=true

[gateway]
mode=shared
nodeport=true`,
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

[kubernetes]
service-cidrs="172.30.0.0/16"
ovn-config-namespace="openshift-ovn-kubernetes"
apiserver="https://testing.test:8443"
host-network-namespace="openshift-host-network"
no-hostsubnet-nodes="kubernetes.io/os=windows"
platform-type="GCP"

[ovnkubernetesfeature]
enable-egress-ip=true
enable-egress-firewall=true

[gateway]
mode=local
nodeport=true

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
			desc: "HybridOverlay with custom VXLAN port",
			expected: `
[default]
mtu="1500"
cluster-subnets="10.128.0.0/15/23,10.0.0.0/14/24"
encap-port="8061"
enable-lflow-cache=true
lflow-cache-limit-kb=1048576

[kubernetes]
service-cidrs="172.30.0.0/16"
ovn-config-namespace="openshift-ovn-kubernetes"
apiserver="https://testing.test:8443"
host-network-namespace="openshift-host-network"
no-hostsubnet-nodes="kubernetes.io/os=windows"
platform-type="GCP"

[ovnkubernetesfeature]
enable-egress-ip=true
enable-egress-firewall=true

[gateway]
mode=local
nodeport=true

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

[kubernetes]
service-cidrs="172.30.0.0/16"
ovn-config-namespace="openshift-ovn-kubernetes"
apiserver="https://testing.test:8443"
host-network-namespace="openshift-host-network"
no-hostsubnet-nodes="kubernetes.io/os=windows"
platform-type="GCP"

[ovnkubernetesfeature]
enable-egress-ip=true
enable-egress-firewall=true

[gateway]
mode=shared
nodeport=true

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

[kubernetes]
service-cidrs="172.30.0.0/16"
ovn-config-namespace="openshift-ovn-kubernetes"
apiserver="https://testing.test:8443"
host-network-namespace="openshift-host-network"
platform-type="GCP"

[ovnkubernetesfeature]
enable-egress-ip=true
enable-egress-firewall=true

[gateway]
mode=shared
nodeport=true

[masterha]
election-lease-duration=137
election-renew-deadline=107
election-retry-period=26`,
			masterIPs: []string{"1.2.3.4"},
			gatewayConfig: &operv1.GatewayConfig{
				RoutingViaHost: false,
			},
		},
	}
	g := NewGomegaWithT(t)

	for i, tc := range testcases {
		t.Run(fmt.Sprintf("%d:%s", i, tc.desc), func(t *testing.T) {
			OVNKubeConfig := OVNKubernetesConfig.DeepCopy()
			if tc.hybridOverlayConfig != nil {
				OVNKubeConfig.Spec.DefaultNetwork.OVNKubernetesConfig.HybridOverlayConfig = tc.hybridOverlayConfig
			}
			if tc.hybridOverlayConfig != nil {
				OVNKubeConfig.Spec.DefaultNetwork.OVNKubernetesConfig.GatewayConfig = tc.gatewayConfig
			}

			//set a few inputs so that the tests are not machine dependant
			OVNKubeConfig.Spec.DefaultNetwork.OVNKubernetesConfig.MTU = ptrToUint32(1500)

			crd := OVNKubeConfig.DeepCopy()
			config := &crd.Spec

			errs := validateOVNKubernetes(config)
			g.Expect(errs).To(HaveLen(0))
			fillDefaults(config, nil)

			bootstrapResult := fakeBootstrapResult()
			bootstrapResult.OVN = bootstrap.OVNBootstrapResult{
				MasterAddresses: tc.masterIPs,
				OVNKubernetesConfig: &bootstrap.OVNConfigBoostrapResult{
					NodeMode: "full",
					HyperShiftConfig: &bootstrap.OVNHyperShiftBootstrapResult{
						Enabled: false,
					},
				},
			}
			objs, _, err := renderOVNKubernetes(config, bootstrapResult, manifestDirOvn)
			g.Expect(err).NotTo(HaveOccurred())
			confFile := extractOVNKubeConfig(g, objs)
			g.Expect(confFile).To(Equal(strings.TrimSpace(tc.expected)))
			// check that the daemonset has the IP family mode annotations
			ipFamilyMode := names.IPFamilySingleStack
			g.Expect(checkDaemonsetAnnotation(g, objs, names.NetworkIPFamilyModeAnnotation, ipFamilyMode)).To(BeTrue())
		})
	}

}

func findNBDBPostStart(objects []*uns.Unstructured) (string, error) {
	var master *uns.Unstructured
	for _, obj := range objects {
		if obj.GetKind() == "DaemonSet" && obj.GetNamespace() == "openshift-ovn-kubernetes" && obj.GetName() == "ovnkube-master" {
			master = obj
			break
		}
	}
	if master == nil {
		return "", fmt.Errorf("could not find DaemonSet openshift-ovn-kubernetes/ovnkube-master")
	}

	containers, found, err := uns.NestedSlice(master.Object, "spec", "template", "spec", "containers")
	if err != nil {
		return "", err
	} else if !found {
		return "", fmt.Errorf("could not find containers in DaemonSet ovnkube-master")
	}

	var nbdb map[string]interface{}
	for _, container := range containers {
		cmap := container.(map[string]interface{})
		name, found, err := uns.NestedString(cmap, "name")
		if found && err == nil && name == "nbdb" {
			nbdb = cmap
			break
		}
	}
	if nbdb == nil {
		return "", fmt.Errorf("could not find nbdb container in DaemonSet ovnkube-master")
	}

	script, found, err := uns.NestedStringSlice(nbdb, "lifecycle", "postStart", "exec", "command")
	if err != nil {
		return "", err
	} else if !found {
		return "", fmt.Errorf("could not find nbdb postStart script")
	}

	return strings.Join(script, " "), nil
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

	// invalid ipv6 mtu
	config.ServiceNetwork = []string{"fd02::/112"}
	config.ClusterNetwork = []operv1.ClusterNetworkEntry{{
		CIDR: "fd01::/48", HostPrefix: 64,
	}}
	ovnConfig.MTU = ptrToUint32(576)
	errExpect("invalid MTU 576")

	config.ClusterNetwork = nil
	errExpect("ClusterNetwork cannot be empty")
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
	g.Expect(errs).To(HaveLen(1))
	g.Expect(errs[0]).To(MatchError("cannot start a hybrid overlay network after install time"))

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
		expectNode    bool // true if node changed
		expectMaster  bool // true if master changed
		expectPrePull bool // true if pre-puller rendered
		node          string
		master        string
		prepull       string // a (maybe) existing pre-puller daemonset
		rv            string // release version
	}{

		// No node, prepuller and master - upgrade = true and config the same
		{
			expectNode:    true,
			expectMaster:  true,
			expectPrePull: false,
			node: `
apiVersion: apps/v1
kind: DaemonSet
`,
			master: `
apiVersion: apps/v1
kind: DaemonSet
`,
		},
		// PrePuller has to pull image before node can upgrade
		{
			expectNode:    false,
			expectMaster:  true,
			expectPrePull: true,
			node: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 4.7.0-0.ci-2021-01-10-200841
  namespace: openshift-ovn-kubernetes
  name: ovnkube-node
`,
			master: `
apiVersion: apps/v1
kind: DaemonSet
`,
		},

		{
			expectNode:   true,
			expectMaster: true,
			// Note: For reducing testing complexity, prepuller is set to false
			// because it hits the condition where the node's version (null) is same
			// as release version (null). In reality if node's version is differnt
			// from expected, prePull will be true.
			expectPrePull: false,
			master: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 4.7.0-0.ci-2021-01-10-200841
  namespace: openshift-ovn-kubernetes
  name: ovnkube-master
`,
			node: `
apiVersion: apps/v1
kind: DaemonSet
`,
		},

		// steady state, no prepuller
		{
			expectNode:    true,
			expectMaster:  true,
			expectPrePull: false,
			rv:            "2.0.0",
			master: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 2.0.0
  namespace: openshift-ovn-kubernetes
  name: ovnkube-master
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
			expectNode:    false,
			expectMaster:  false,
			expectPrePull: true,
			rv:            "2.0.0",
			master: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 1.9.9
  namespace: openshift-ovn-kubernetes
  name: ovnkube-master
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
			expectNode:    false,
			expectMaster:  false,
			expectPrePull: true,
			rv:            "2.0.0",
			master: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 1.9.9
  namespace: openshift-ovn-kubernetes
  name: ovnkube-master
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
			expectNode:    false,
			expectMaster:  false,
			expectPrePull: true,
			rv:            "2.0.0",
			master: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 1.9.9
  namespace: openshift-ovn-kubernetes
  name: ovnkube-master
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
			expectNode:    true,
			expectMaster:  false,
			expectPrePull: false,
			rv:            "2.0.0",
			master: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 1.9.9
  namespace: openshift-ovn-kubernetes
  name: ovnkube-master
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
			expectNode:    true,
			expectMaster:  false,
			expectPrePull: false,

			rv: "2.0.0",
			master: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 1.9.9
  namespace: openshift-ovn-kubernetes
  name: ovnkube-master
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
  numberMisscheduled: 0
  numberReady: 5
  observedGeneration: 2
  updatedNumberScheduled: 5
`,
		},

		// node upgrade hung but not made progress
		{
			expectNode:    true,
			expectMaster:  false,
			expectPrePull: false,
			rv:            "2.0.0",
			master: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 1.9.9
  namespace: openshift-ovn-kubernetes
  name: ovnkube-master
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
			expectNode:    true,
			expectMaster:  true,
			expectPrePull: false,
			rv:            "2.0.0",
			master: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 1.9.9
  namespace: openshift-ovn-kubernetes
  name: ovnkube-master
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
			expectNode:    true,
			expectMaster:  true,
			expectPrePull: false,
			rv:            "2.0.0",
			master: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 1.9.9
  namespace: openshift-ovn-kubernetes
  name: ovnkube-master
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
			expectNode:    false,
			expectMaster:  true,
			expectPrePull: false,
			rv:            "1.8.9",
			master: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 1.9.9
  namespace: openshift-ovn-kubernetes
  name: ovnkube-master
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

		// master downgrade applied, not yet rolled out
		{
			expectNode:    false,
			expectMaster:  true,
			expectPrePull: false,
			rv:            "1.8.9",
			master: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 1.8.9
  namespace: openshift-ovn-kubernetes
  name: ovnkube-master
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
			expectNode:    false,
			expectMaster:  true,
			expectPrePull: false,
			rv:            "1.8.9",
			master: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 1.8.9
  namespace: openshift-ovn-kubernetes
  name: ovnkube-master
  generation: 2
status:
  currentNumberScheduled: 6
  desiredNumberScheduled: 6
  numberAvailable: 5
  numberUnavailable: 1
  numberMisscheduled: 0
  numberReady: 5
  observedGeneration: 2
  updatedNumberScheduled:
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
			expectNode:    false,
			expectMaster:  true,
			expectPrePull: false,
			rv:            "1.8.9",
			master: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 1.8.9
    networkoperator.openshift.io/rollout-hung: ""
  namespace: openshift-ovn-kubernetes
  name: ovnkube-master
  generation: 2
status:
  currentNumberScheduled: 3
  desiredNumberScheduled: 3
  numberAvailable: 2
  numberUnavailable: 1
  numberMisscheduled: 0
  numberReady: 2
  observedGeneration: 2
  updatedNumberScheduled: 1
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
		// except we always wait for 100% master.
		{
			expectNode:    false,
			expectMaster:  true,
			expectPrePull: false,
			rv:            "1.8.9",
			master: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 1.8.9
    networkoperator.openshift.io/rollout-hung: ""
  namespace: openshift-ovn-kubernetes
  name: ovnkube-master
  generation: 2
status:
  currentNumberScheduled: 3
  desiredNumberScheduled: 3
  numberAvailable: 2
  numberUnavailable: 1
  numberMisscheduled: 0
  numberReady: 2
  observedGeneration: 2
  updatedNumberScheduled: 3
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
			var master *appsv1.DaemonSet
			var prepuller *appsv1.DaemonSet
			crd := OVNKubernetesConfig.DeepCopy()
			config := &crd.Spec
			os.Setenv("RELEASE_VERSION", tc.rv)

			errs := validateOVNKubernetes(config)
			g.Expect(errs).To(HaveLen(0))
			fillDefaults(config, nil)

			node = &appsv1.DaemonSet{}
			err := yaml.Unmarshal([]byte(tc.node), node)
			if err != nil {
				t.Fatal(err)
			}

			master = &appsv1.DaemonSet{}
			err = yaml.Unmarshal([]byte(tc.master), master)
			if err != nil {
				t.Fatal(err)
			}

			if tc.prepull != "" {
				prepuller = &appsv1.DaemonSet{}
				err = yaml.Unmarshal([]byte(tc.prepull), prepuller)
				if err != nil {
					t.Fatal(err)
				}
			}

			bootstrapResult := fakeBootstrapResult()
			bootstrapResult.OVN = bootstrap.OVNBootstrapResult{
				MasterAddresses:         []string{"1.2.3.4", "5.6.7.8", "9.10.11.12"},
				ExistingMasterDaemonset: master,
				ExistingNodeDaemonset:   node,
				OVNKubernetesConfig: &bootstrap.OVNConfigBoostrapResult{
					NodeMode: "full",
					HyperShiftConfig: &bootstrap.OVNHyperShiftBootstrapResult{
						Enabled: false,
					},
				},
				PrePullerDaemonset: prepuller,
			}

			objs, _, err := renderOVNKubernetes(config, bootstrapResult, manifestDirOvn)
			g.Expect(err).NotTo(HaveOccurred())

			renderedNode := findInObjs("apps", "DaemonSet", "ovnkube-node", "openshift-ovn-kubernetes", objs)
			_, preserveNode := renderedNode.GetAnnotations()[names.CreateOnlyAnnotation]
			renderedMaster := findInObjs("apps", "DaemonSet", "ovnkube-master", "openshift-ovn-kubernetes", objs)
			_, preserveMaster := renderedMaster.GetAnnotations()[names.CreateOnlyAnnotation]
			renderedPrePuller := findInObjs("apps", "DaemonSet", "ovnkube-upgrades-prepuller", "openshift-ovn-kubernetes", objs) != nil

			// if we expect a node update, the original node and the rendered one must be different
			g.Expect(tc.expectNode).To(Equal(!preserveNode), "Check node rendering")
			// if we expect a master update, the original master and the rendered one must be different
			g.Expect(tc.expectMaster).To(Equal(!preserveMaster), "Check master rendering")
			// if we expect a prepuller update, the original prepuller and the rendered one must be different
			g.Expect(tc.expectPrePull).To(Equal(renderedPrePuller), "Check prepuller rendering")

			updateNode, updateMaster := shouldUpdateOVNKonUpgrade(node, master, tc.rv)
			g.Expect(updateMaster).To(Equal(tc.expectMaster), "Check master")
			if updateNode {
				var updatePrePuller bool
				updateNode, updatePrePuller = shouldUpdateOVNKonPrepull(node, prepuller, tc.rv)
				g.Expect(updatePrePuller).To(Equal(tc.expectPrePull), "Check prepuller")
			}
			g.Expect(updateNode).To(Equal(tc.expectNode), "Check node")
		})
	}
}

func TestShouldUpdateOVNKonIPFamilyChange(t *testing.T) {

	for _, tc := range []struct {
		name         string
		node         *appsv1.DaemonSet
		master       *appsv1.DaemonSet
		ipFamilyMode string
		expectNode   bool
		expectMaster bool
	}{
		{
			name:         "all empty",
			node:         &appsv1.DaemonSet{},
			master:       &appsv1.DaemonSet{},
			expectNode:   true,
			expectMaster: true,
			ipFamilyMode: names.IPFamilySingleStack,
		},
		{
			name:         "fresh cluster",
			node:         &appsv1.DaemonSet{},
			master:       &appsv1.DaemonSet{},
			expectNode:   true,
			expectMaster: true,
			ipFamilyMode: names.IPFamilySingleStack,
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
			master: &appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ovnkube-master",
					Namespace: "openshift-ovn-kubernetes",
					Annotations: map[string]string{
						names.NetworkIPFamilyModeAnnotation: names.IPFamilySingleStack,
					},
					Generation: 1,
				},
				Status: appsv1.DaemonSetStatus{
					CurrentNumberScheduled: 3,
					DesiredNumberScheduled: 3,
					NumberAvailable:        3,
					NumberMisscheduled:     0,
					NumberReady:            3,
					ObservedGeneration:     2,
					UpdatedNumberScheduled: 3,
				},
			},
			expectNode:   true,
			expectMaster: true,
			ipFamilyMode: names.IPFamilySingleStack,
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
			master: &appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ovnkube-master",
					Namespace: "openshift-ovn-kubernetes",
					Annotations: map[string]string{
						names.NetworkIPFamilyModeAnnotation: names.IPFamilySingleStack,
					},
				},
			},
			expectNode:   false,
			expectMaster: true,
			ipFamilyMode: names.IPFamilyDualStack,
		},
		{
			name: "configuration changed, master updated and node remaining",
			node: &appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ovnkube-node",
					Namespace: "openshift-ovn-kubernetes",
					Annotations: map[string]string{
						names.NetworkIPFamilyModeAnnotation: names.IPFamilySingleStack,
					},
				},
			},
			master: &appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ovnkube-master",
					Namespace: "openshift-ovn-kubernetes",
					Annotations: map[string]string{
						names.NetworkIPFamilyModeAnnotation: names.IPFamilyDualStack,
					},
					Generation: 1,
				},
				Status: appsv1.DaemonSetStatus{
					CurrentNumberScheduled: 3,
					DesiredNumberScheduled: 3,
					NumberAvailable:        3,
					NumberMisscheduled:     0,
					NumberReady:            3,
					ObservedGeneration:     2,
					UpdatedNumberScheduled: 3,
				},
			},
			expectNode:   true,
			expectMaster: true,
			ipFamilyMode: names.IPFamilyDualStack,
		},
		{
			name: "configuration changed, master updated and node remaining but still rolling out",
			node: &appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ovnkube-node",
					Namespace: "openshift-ovn-kubernetes",
					Annotations: map[string]string{
						names.NetworkIPFamilyModeAnnotation: names.IPFamilySingleStack,
					},
				},
			},
			master: &appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ovnkube-master",
					Namespace: "openshift-ovn-kubernetes",
					Annotations: map[string]string{
						names.NetworkIPFamilyModeAnnotation: names.IPFamilyDualStack,
					},
					Generation: 1,
				},
				Status: appsv1.DaemonSetStatus{
					CurrentNumberScheduled: 3,
					DesiredNumberScheduled: 3,
					NumberAvailable:        2,
					NumberUnavailable:      1,
					NumberMisscheduled:     0,
					NumberReady:            2,
					ObservedGeneration:     2,
					UpdatedNumberScheduled: 3,
				},
			},
			expectNode:   false,
			expectMaster: true,
			ipFamilyMode: names.IPFamilyDualStack,
		},
		// this should not be possible, because configuration changes always update master first
		{
			name: "configuration changed, node updated and master remaining",
			node: &appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ovnkube-node",
					Namespace: "openshift-ovn-kubernetes",
					Annotations: map[string]string{
						names.NetworkIPFamilyModeAnnotation: names.IPFamilyDualStack,
					},
				},
			},
			master: &appsv1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ovnkube-master",
					Namespace: "openshift-ovn-kubernetes",
					Annotations: map[string]string{
						names.NetworkIPFamilyModeAnnotation: names.IPFamilySingleStack,
					},
					Generation: 2,
				},
				Status: appsv1.DaemonSetStatus{
					CurrentNumberScheduled: 3,
					DesiredNumberScheduled: 3,
					NumberAvailable:        3,
					NumberMisscheduled:     0,
					NumberReady:            3,
					ObservedGeneration:     2,
					UpdatedNumberScheduled: 3,
				},
			},
			expectNode:   false,
			expectMaster: true,
			ipFamilyMode: names.IPFamilyDualStack,
		},
	} {

		t.Run(tc.name, func(t *testing.T) {
			updateNode, updateMaster := shouldUpdateOVNKonIPFamilyChange(tc.node, tc.master, tc.ipFamilyMode)
			if updateNode != tc.expectNode {
				t.Errorf("Expected node update: %v received %v", tc.expectNode, updateNode)
			}
			if updateMaster != tc.expectMaster {
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
	os.Setenv("RELEASE_VERSION", "2.0.0")

	// bootstrap also represents current status
	// the current cluster is single-stack and has version 1.9.9
	bootstrapResult := fakeBootstrapResult()
	bootstrapResult.OVN = bootstrap.OVNBootstrapResult{
		MasterAddresses: []string{"1.2.3.4", "5.6.7.8", "9.10.11.12"},
		ExistingMasterDaemonset: &appsv1.DaemonSet{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "apps/v1",
				Kind:       "DaemonSet",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "ovnkube-master",
				Namespace: "openshift-ovn-kubernetes",
				Annotations: map[string]string{
					names.NetworkIPFamilyModeAnnotation: names.IPFamilySingleStack,
					"release.openshift.io/version":      "1.9.9",
				},
			},
		},
		ExistingNodeDaemonset: &appsv1.DaemonSet{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "apps/v1",
				Kind:       "DaemonSet",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "ovnkube-node",
				Namespace: "openshift-ovn-kubernetes",
				Annotations: map[string]string{
					names.NetworkIPFamilyModeAnnotation: names.IPFamilySingleStack,
					"release.openshift.io/version":      "1.9.9",
				},
			},
		},
		OVNKubernetesConfig: &bootstrap.OVNConfigBoostrapResult{
			NodeMode: "full",
			HyperShiftConfig: &bootstrap.OVNHyperShiftBootstrapResult{
				Enabled: false,
			},
		},
	}

	// the new rendered config should hold the node to do the dualstack conversion
	// the upgrade code holds the masters to update the nodes first
	objs, _, err := renderOVNKubernetes(config, bootstrapResult, manifestDirOvn)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	renderedNode := findInObjs("apps", "DaemonSet", "ovnkube-node", "openshift-ovn-kubernetes", objs)
	renderedMaster := findInObjs("apps", "DaemonSet", "ovnkube-master", "openshift-ovn-kubernetes", objs)

	// the node has to be the same
	if _, ok := renderedNode.GetAnnotations()[names.CreateOnlyAnnotation]; !ok {
		t.Errorf("node DaemonSet should have create-only annotation, does not")
	}
	// the master has to use the new annotations for dual-stack so it has to be mutated
	if _, ok := renderedMaster.GetAnnotations()[names.CreateOnlyAnnotation]; ok {
		t.Errorf("master daemonset are equal, dual-stack should modify masters")
	}
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
			objs, _, err := renderOVNKubernetes(config, bootstrapResult, manifestDirOvn)
			g.Expect(err).ToNot(HaveOccurred())
			nodeDS := findInObjs("apps", "DaemonSet", "ovnkube-node", "openshift-ovn-kubernetes", objs)
			ds := appsv1.DaemonSet{}
			g.Expect(convert(nodeDS, &ds)).To(Succeed())
			nodeCont, ok := findContainer(ds.Spec.Template.Spec.Containers, "ovnkube-node")
			g.Expect(ok).To(BeTrue(), "expecting container named ovnkube-node in the DaemonSet")
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

type fakeClientReader struct {
	configMap *v1.ConfigMap
}

func (f *fakeClientReader) Get(_ context.Context, _ crclient.ObjectKey, obj crclient.Object) error {
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
			return string(val)
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
	foundMaster, foundNode := false, false
	for _, obj := range objs {
		if obj.GetAPIVersion() == "apps/v1" && obj.GetKind() == "DaemonSet" &&
			(obj.GetName() == "ovnkube-master" || obj.GetName() == "ovnkube-node") {

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
			if obj.GetName() == "ovnkube-master" {
				foundMaster = true
			} else {
				foundNode = true
			}
		}
	}
	return foundMaster && foundNode
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
