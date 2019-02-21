package network

import (
	"testing"

	. "github.com/onsi/gomega"

	netv1 "github.com/openshift/cluster-network-operator/pkg/apis/networkoperator/v1"
)

func TestIsChangeSafe(t *testing.T) {
	g := NewGomegaWithT(t)

	prev := OpenShiftSDNConfig.Spec.DeepCopy()
	FillDefaults(prev, nil)
	next := OpenShiftSDNConfig.Spec.DeepCopy()
	FillDefaults(next, nil)

	err := IsChangeSafe(prev, next)
	g.Expect(err).NotTo(HaveOccurred())

	next.ClusterNetworks[0].HostSubnetLength = 1
	err = IsChangeSafe(prev, next)
	g.Expect(err).To(MatchError(ContainSubstring("cannot change ClusterNetworks")))

	next = OpenShiftSDNConfig.Spec.DeepCopy()
	FillDefaults(next, nil)
	next.ServiceNetwork = "1.2.3.4/99"
	err = IsChangeSafe(prev, next)
	g.Expect(err).To(MatchError(ContainSubstring("cannot change ServiceNetwork")))

	next = OpenShiftSDNConfig.Spec.DeepCopy()
	FillDefaults(next, nil)
	next.DefaultNetwork.Type = "Kuryr"
	err = IsChangeSafe(prev, next)
	g.Expect(err).To(MatchError(ContainSubstring("cannot change default network type")))
}

func TestRenderUnknownNetwork(t *testing.T) {
	g := NewGomegaWithT(t)

	config := netv1.NetworkConfig{
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
				Type: "MyAwesomeThirdPartyPlugin",
			},
		},
	}

	err := Validate(&config.Spec)
	g.Expect(err).NotTo(HaveOccurred())

	prev := config.Spec.DeepCopy()
	FillDefaults(prev, nil)
	next := config.Spec.DeepCopy()
	FillDefaults(next, nil)

	err = IsChangeSafe(prev, next)
	g.Expect(err).NotTo(HaveOccurred())

	objs, err := Render(prev, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())

	// Validate that openshift-sdn isn't rendered
	g.Expect(objs).NotTo(ContainElement(HaveKubernetesID("DaemonSet", "openshift-sdn", "ovs")))

	// validate that Multus is still rendered
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "multus")))

	// TODO(cdc) validate that kube-proxy is rendered
}
