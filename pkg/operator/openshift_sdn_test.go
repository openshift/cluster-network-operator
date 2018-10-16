package operator

import (
	"testing"

	yaml "github.com/ghodss/yaml"
	netv1 "github.com/openshift/cluster-network-operator/pkg/apis/networkoperator/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	. "github.com/onsi/gomega"
)

var OpenshiftSDNConfig = netv1.NetworkConfig{
	Spec: netv1.NetworkConfigSpec{
		ServiceNetwork: "172.30.0.0/16",
		ClusterNetworks: []netv1.ClusterNetwork{
			{
				CIDR:             "10.128.0.0/15",
				HostSubnetLength: 9,
			},
			{
				CIDR:             "10.0.0.0/14",
				HostSubnetLength: 8,
			},
		},
		DefaultNetwork: netv1.DefaultNetworkDefinition{
			Type: netv1.NetworkTypeOpenshiftSDN,
			OpenshiftSDNConfig: &netv1.OpenshiftSDNConfig{
				Mode: netv1.SDNModePolicy,
			},
		},
	},
}

var manifestDir = "../../bindata"

// TestRenderOpenshiftSDN has some simple rendering tests
func TestRenderOpenshiftSDN(t *testing.T) {
	g := NewGomegaWithT(t)

	h := Handler{
		config:      OpenshiftSDNConfig.DeepCopy(),
		ManifestDir: manifestDir,
	}
	config := h.config.Spec
	sdnConfig := config.DefaultNetwork.OpenshiftSDNConfig

	errs := h.validateOpenshiftSDN()
	g.Expect(errs).To(HaveLen(0))

	// Make sure the OVS daemonset isn't created
	truth := true
	sdnConfig.UseExternalOpenvswitch = &truth
	objs, err := h.renderOpenshiftSDN()
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).NotTo(ContainElement(HaveKubernetesID("DaemonSet", "openshift-sdn", "ovs")))

	// enable openvswitch
	sdnConfig.UseExternalOpenvswitch = nil
	objs, err = h.renderOpenshiftSDN()
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

func TestValidateOpenshiftSDN(t *testing.T) {
	g := NewGomegaWithT(t)

	h := Handler{
		config:      OpenshiftSDNConfig.DeepCopy(),
		ManifestDir: manifestDir,
	}
	config := &h.config.Spec
	sdnconfig := config.DefaultNetwork.OpenshiftSDNConfig

	err := h.validateOpenshiftSDN()
	g.Expect(err).To(BeEmpty())

	errExpect := func(substr string) {
		t.Helper()
		g.Expect(h.validateOpenshiftSDN()).To(
			ContainElement(MatchError(
				ContainSubstring(substr))))
	}

	// set mtu to insanity
	mtu := uint32(70000)
	sdnconfig.MTU = &mtu
	errExpect("invalid MTU 70000")

	sdnconfig.Mode = "broken"
	errExpect("invalid openshift-sdn mode \"broken\"")

	port := uint32(66666)
	sdnconfig.VXLANPort = &port
	errExpect("invalid VXLANPort 66666")

	config.ClusterNetworks = nil
	errExpect("ClusterNetworks cannot be empty")
}

func TestProxyArgs(t *testing.T) {
	g := NewGomegaWithT(t)

	h := Handler{
		config:      OpenshiftSDNConfig.DeepCopy(),
		ManifestDir: manifestDir,
	}
	config := &h.config.Spec

	// iter through all objects, finding the sdn config map
	getSdnConfigFile := func(objs []*uns.Unstructured) *uns.Unstructured {
		for _, obj := range objs {
			if obj.GetKind() == "ConfigMap" && obj.GetName() == "sdn-config" {
				val, ok, err := uns.NestedString(obj.Object, "data", "sdn-config.yaml.in")
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
	objs, err := h.renderOpenshiftSDN()
	g.Expect(err).NotTo(HaveOccurred())
	cfg := getSdnConfigFile(objs)
	val, _, _ := uns.NestedString(cfg.Object, "servingInfo", "bindAddress")
	g.Expect(val).To(Equal("0.0.0.0:10251"))
	val, _, _ = uns.NestedString(cfg.Object, "iptablesSyncPeriod")
	g.Expect(val).To(Equal(""))

	// set sync period
	config.KubeProxyConfig = &netv1.ProxyConfig{
		IptablesSyncPeriod: "10s",
		BindAddress:        "1.2.3.4",
	}
	objs, err = h.renderOpenshiftSDN()
	g.Expect(err).NotTo(HaveOccurred())
	cfg = getSdnConfigFile(objs)
	g.Expect(cfg.Object).To(HaveKeyWithValue("iptablesSyncPeriod", "10s"))
	val, _, _ = uns.NestedString(cfg.Object, "servingInfo", "bindAddress")
	g.Expect(val).To(Equal("1.2.3.4:10251"))

	//set proxy args
	config.KubeProxyConfig.ProxyArguments = map[string][]string{
		"a": []string{"b"},
		"c": []string{"d", "e"},
	}
	objs, err = h.renderOpenshiftSDN()
	g.Expect(err).NotTo(HaveOccurred())
	cfg = getSdnConfigFile(objs)

	arg, _, _ := uns.NestedStringSlice(cfg.Object, "proxyArguments", "a")
	g.Expect(arg).To(Equal([]string{"b"}))

	arg, _, _ = uns.NestedStringSlice(cfg.Object, "proxyArguments", "c")
	g.Expect(arg).To(Equal([]string{"d", "e"}))

}
