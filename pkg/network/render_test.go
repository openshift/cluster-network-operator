package network

import (
	"testing"

	. "github.com/onsi/gomega"

	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
)

func TestIsChangeSafe(t *testing.T) {
	g := NewGomegaWithT(t)

	prev := OpenShiftSDNConfig.Spec.DeepCopy()
	FillDefaults(prev, nil)
	next := OpenShiftSDNConfig.Spec.DeepCopy()
	FillDefaults(next, nil)

	err := IsChangeSafe(prev, next)
	g.Expect(err).NotTo(HaveOccurred())

	next.ClusterNetwork[0].HostPrefix = 31
	err = IsChangeSafe(prev, next)
	g.Expect(err).To(MatchError(ContainSubstring("cannot change ClusterNetwork")))

	next = OpenShiftSDNConfig.Spec.DeepCopy()
	FillDefaults(next, nil)
	next.ServiceNetwork = []string{"1.2.3.4/99", "8.8.8.0/30"}
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

	config := operv1.Network{
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

	objs, err := Render(prev, &bootstrap.BootstrapResult{}, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())

	// Validate that openshift-sdn isn't rendered
	g.Expect(objs).NotTo(ContainElement(HaveKubernetesID("DaemonSet", "openshift-sdn", "ovs")))

	// validate that Multus is still rendered
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "multus")))

	// TODO(cdc) validate that kube-proxy is rendered
}
