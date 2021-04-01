package network

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/ghodss/yaml"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/apply"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"github.com/openshift/cluster-network-operator/pkg/util/k8s"
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
	FillDefaults(config, nil)

	bootstrapResult := &bootstrap.BootstrapResult{
		OVN: bootstrap.OVNBootstrapResult{
			MasterIPs: []string{"1.2.3.4", "5.6.7.8", "9.10.11.12"},
		},
	}

	objs, err := renderOVNKubernetes(config, bootstrapResult, manifestDirOvn)
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

// TestRenderOVNKubernetesIPv6 tests IPv6 support
func TestRenderOVNKubernetesIPv6(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := OVNKubernetesConfig.DeepCopy()
	config := &crd.Spec

	errs := validateOVNKubernetes(config)
	g.Expect(errs).To(HaveLen(0))
	FillDefaults(config, nil)

	bootstrapResult := &bootstrap.BootstrapResult{
		OVN: bootstrap.OVNBootstrapResult{
			MasterIPs: []string{"1.2.3.4", "5.6.7.8", "9.10.11.12"},
		},
	}
	objs, err := renderOVNKubernetes(config, bootstrapResult, manifestDirOvn)
	g.Expect(err).NotTo(HaveOccurred())

	script, err := findNBDBPostStart(objs)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(script).To(ContainSubstring("pssl:9641"))

	bootstrapResult = &bootstrap.BootstrapResult{
		OVN: bootstrap.OVNBootstrapResult{
			MasterIPs: []string{"fd01::1", "fd01::2", "fd01::3"},
		},
	}
	objs, err = renderOVNKubernetes(config, bootstrapResult, manifestDirOvn)
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
	}
	testcases := []testcase{
		{
			desc: "default",
			expected: `
[default]
mtu="1500"
cluster-subnets="10.128.0.0/15/23,10.0.0.0/14/24"
encap-port="8061"

[kubernetes]
service-cidrs="172.30.0.0/16"
ovn-config-namespace="openshift-ovn-kubernetes"
apiserver="https://1.1.1.1:1111"

[ovnkubernetesfeature]
enable-egress-ip=true

[gateway]
mode=local
nodeport=true`,
		},

		{
			desc: "HybridOverlay",
			expected: `
[default]
mtu="1500"
cluster-subnets="10.128.0.0/15/23,10.0.0.0/14/24"
encap-port="8061"

[kubernetes]
service-cidrs="172.30.0.0/16"
ovn-config-namespace="openshift-ovn-kubernetes"
apiserver="https://1.1.1.1:1111"
no-hostsubnet-nodes="kubernetes.io/os=windows"

[ovnkubernetesfeature]
enable-egress-ip=true

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
		},
		{
			desc: "HybridOverlay with custom VXLAN port",
			expected: `
[default]
mtu="1500"
cluster-subnets="10.128.0.0/15/23,10.0.0.0/14/24"
encap-port="8061"

[kubernetes]
service-cidrs="172.30.0.0/16"
ovn-config-namespace="openshift-ovn-kubernetes"
apiserver="https://1.1.1.1:1111"
no-hostsubnet-nodes="kubernetes.io/os=windows"

[ovnkubernetesfeature]
enable-egress-ip=true

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
		},
		{
			desc: "HybridOverlay enabled with no ClusterNetworkEntry",
			expected: `
[default]
mtu="1500"
cluster-subnets="10.128.0.0/15/23,10.0.0.0/14/24"
encap-port="8061"

[kubernetes]
service-cidrs="172.30.0.0/16"
ovn-config-namespace="openshift-ovn-kubernetes"
apiserver="https://1.1.1.1:1111"
no-hostsubnet-nodes="kubernetes.io/os=windows"

[ovnkubernetesfeature]
enable-egress-ip=true

[gateway]
mode=local
nodeport=true

[hybridoverlay]
enabled=true`,

			hybridOverlayConfig: &operv1.HybridOverlayConfig{},
		},
	}
	g := NewGomegaWithT(t)

	os.Setenv("KUBERNETES_SERVICE_HOST", "1.1.1.1")
	os.Setenv("KUBERNETES_SERVICE_PORT", "1111")

	for i, tc := range testcases {
		t.Run(fmt.Sprintf("%d:%s", i, tc.desc), func(t *testing.T) {
			OVNKubeConfig := OVNKubernetesConfig.DeepCopy()
			if tc.hybridOverlayConfig != nil {
				OVNKubeConfig.Spec.DefaultNetwork.OVNKubernetesConfig.HybridOverlayConfig = tc.hybridOverlayConfig
			}
			//set a few inputs so that the tests are not machine dependant
			OVNKubeConfig.Spec.DefaultNetwork.OVNKubernetesConfig.MTU = ptrToUint32(1500)

			crd := OVNKubeConfig.DeepCopy()
			config := &crd.Spec

			errs := validateOVNKubernetes(config)
			g.Expect(errs).To(HaveLen(0))
			FillDefaults(config, nil)

			bootstrapResult := &bootstrap.BootstrapResult{
				OVN: bootstrap.OVNBootstrapResult{
					MasterIPs: []string{"1.2.3.4", "5.6.7.8", "9.10.11.12"},
				},
			}
			objs, err := renderOVNKubernetes(config, bootstrapResult, manifestDirOvn)
			g.Expect(err).NotTo(HaveOccurred())
			confFile := extractOVNKubeConfig(g, objs)
			g.Expect(confFile).To(Equal(strings.TrimSpace(tc.expected)))
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
	FillDefaults(config, nil)

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

	config.ClusterNetwork = nil
	errExpect("ClusterNetwork cannot be empty")
}

func TestValidateOVNKubernetesDualStack(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := OVNKubernetesConfig.DeepCopy()
	config := &crd.Spec

	err := validateOVNKubernetes(config)
	g.Expect(err).To(BeEmpty())
	FillDefaults(config, nil)

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
	FillDefaults(prev, nil)
	next := OVNKubernetesConfig.Spec.DeepCopy()
	FillDefaults(next, nil)

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

	// change the mtu
	next.DefaultNetwork.OVNKubernetesConfig.MTU = ptrToUint32(70000)

	// change the geneve port
	next.DefaultNetwork.OVNKubernetesConfig.GenevePort = ptrToUint32(34001)
	errs = isOVNKubernetesChangeSafe(prev, next)
	g.Expect(errs).To(HaveLen(2))
	g.Expect(errs[0]).To(MatchError("cannot change ovn-kubernetes MTU"))
	g.Expect(errs[1]).To(MatchError("cannot change ovn-kubernetes genevePort"))
}

// TestOVNKubernetesShouldUpdateMaster checks to see that
func TestOVNKubernetestShouldUpdateMaster(t *testing.T) {

	for idx, tc := range []struct {
		expectNode   bool
		expectMaster bool
		node         string
		master       string
		rv           string // release version
	}{

		// No node and master - upgrade = true
		{
			expectNode:   true,
			expectMaster: true,
		},

		{
			expectNode:   true,
			expectMaster: true,
			node: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 4.7.0-0.ci-2021-01-10-200841
  namespace: openshift-ovn-kubernetes
  name: ovnkube-node
`,
		},

		{
			expectNode:   true,
			expectMaster: true,
			master: `
apiVersion: apps/v1
kind: DaemonSet
metadata:
  annotations:
    release.openshift.io/version: 4.7.0-0.ci-2021-01-10-200841
  namespace: openshift-ovn-kubernetes
  name: ovnkube-master
`,
		},

		// steady state
		{
			expectNode:   true,
			expectMaster: true,
			rv:           "2.0.0",
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

		// upgrade not yet applied
		{
			expectNode:   true,
			expectMaster: false,
			rv:           "2.0.0",
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

		// node upgrade applied, upgrade not yet rolled out
		{
			expectNode:   true,
			expectMaster: false,
			rv:           "2.0.0",
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
			expectNode:   true,
			expectMaster: false,

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
			expectNode:   true,
			expectMaster: false,
			rv:           "2.0.0",
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
			expectNode:   true,
			expectMaster: true,
			rv:           "2.0.0",
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
			expectNode:   true,
			expectMaster: true,
			rv:           "2.0.0",
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
			expectNode:   false,
			expectMaster: true,
			rv:           "1.8.9",
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
			expectNode:   false,
			expectMaster: true,
			rv:           "1.8.9",
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
			expectNode:   false,
			expectMaster: true,

			rv: "1.8.9",
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
			expectNode:   false,
			expectMaster: true,
			rv:           "1.8.9",
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
			expectNode:   false,
			expectMaster: true,
			rv:           "1.8.9",
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
			crd := OVNKubernetesConfig.DeepCopy()
			config := &crd.Spec
			os.Setenv("RELEASE_VERSION", tc.rv)

			errs := validateOVNKubernetes(config)
			g.Expect(errs).To(HaveLen(0))
			FillDefaults(config, nil)

			if tc.node != "" {
				node = &appsv1.DaemonSet{}
				err := yaml.Unmarshal([]byte(tc.node), node)
				if err != nil {
					t.Fatal(err)
				}
			}

			if tc.master != "" {
				master = &appsv1.DaemonSet{}
				err := yaml.Unmarshal([]byte(tc.master), master)
				if err != nil {
					t.Fatal(err)
				}
			}

			usNode, _ := k8s.ToUnstructured(node)
			usMaster, _ := k8s.ToUnstructured(master)

			bootstrapResult := &bootstrap.BootstrapResult{
				OVN: bootstrap.OVNBootstrapResult{
					MasterIPs:               []string{"1.2.3.4", "5.6.7.8", "9.10.11.12"},
					ExistingMasterDaemonset: master,
					ExistingNodeDaemonset:   node,
				},
			}

			objs, err := renderOVNKubernetes(config, bootstrapResult, manifestDirOvn)
			g.Expect(err).NotTo(HaveOccurred())

			renderedNode := findInObjs("apps", "DaemonSet", "ovnkube-node", "openshift-ovn-kubernetes", objs)
			renderedMaster := findInObjs("apps", "DaemonSet", "ovnkube-master", "openshift-ovn-kubernetes", objs)

			// if we expect a node update, the original node and the rendered one must be different
			g.Expect(tc.expectNode).To(Equal(!reflect.DeepEqual(renderedNode, usNode)), "Check node rendering")
			// if we expect a master update, the original master and the rendered one must be different
			g.Expect(tc.expectMaster).To(Equal(!reflect.DeepEqual(renderedMaster, usMaster)), "Check master rendering")

			updateNode, updateMaster := shouldUpdateOVNK(node, master, tc.rv)
			g.Expect(updateNode).To(Equal(tc.expectNode), "Check node")
			g.Expect(updateMaster).To(Equal(tc.expectMaster), "Check master")
		})
	}
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

func ptrToUint32(x uint32) *uint32 {
	return &x
}
