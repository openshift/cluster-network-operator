package network

import (
	"testing"

	. "github.com/onsi/gomega"

	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
)

func TestIsChangeSafe(t *testing.T) {
	g := NewGomegaWithT(t)

	// NOTE: IsChangeSafe() requires you to have called Validate() beforehand, so we
	// don't have to check that invalid configs are considered unsafe to change to.

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
	next.ClusterNetwork = append(next.ClusterNetwork, operv1.ClusterNetworkEntry{
		CIDR:       "1.2.0.0/16",
		HostPrefix: 24,
	})
	err = IsChangeSafe(prev, next)
	g.Expect(err).To(MatchError(ContainSubstring("cannot change ClusterNetwork")))

	next = OpenShiftSDNConfig.Spec.DeepCopy()
	FillDefaults(next, nil)
	next.ServiceNetwork = []string{"1.2.3.0/24"}
	err = IsChangeSafe(prev, next)
	g.Expect(err).To(MatchError(ContainSubstring("cannot change ServiceNetwork")))

	next = OpenShiftSDNConfig.Spec.DeepCopy()
	FillDefaults(next, nil)
	next.DefaultNetwork.Type = "Kuryr"
	err = IsChangeSafe(prev, next)
	g.Expect(err).To(MatchError(ContainSubstring("cannot change default network type when not doing migration")))

	// You can change a single-stack config to dual-stack
	next = OpenShiftSDNConfig.Spec.DeepCopy()
	FillDefaults(next, nil)
	next.ServiceNetwork = append(next.ServiceNetwork, "fd02::/112")
	next.ClusterNetwork = append(next.ClusterNetwork, operv1.ClusterNetworkEntry{
		CIDR:       "fd01::/48",
		HostPrefix: 64,
	})
	err = IsChangeSafe(prev, next)
	g.Expect(err).NotTo(HaveOccurred())
	// ...and vice-versa
	err = IsChangeSafe(next, prev)
	g.Expect(err).NotTo(HaveOccurred())

	// But you can't go from single-stack IPv4 to dual-stack IPv6-primary
	next = OpenShiftSDNConfig.Spec.DeepCopy()
	FillDefaults(next, nil)
	next.ServiceNetwork = append([]string{"fd02::/112"}, prev.ServiceNetwork...)
	next.ClusterNetwork = append([]operv1.ClusterNetworkEntry{{
		CIDR:       "fd01::/48",
		HostPrefix: 64,
	}}, prev.ClusterNetwork...)
	err = IsChangeSafe(prev, next)
	g.Expect(err).To(MatchError(ContainSubstring("cannot change ServiceNetwork")))
	// ...or vice-versa
	err = IsChangeSafe(next, prev)
	g.Expect(err).To(MatchError(ContainSubstring("cannot change ServiceNetwork")))

	// You can add multiple ClusterNetworks of the new IP family
	next = OpenShiftSDNConfig.Spec.DeepCopy()
	FillDefaults(next, nil)
	next.ServiceNetwork = append(next.ServiceNetwork, "fd02::/112")
	next.ClusterNetwork = append(next.ClusterNetwork,
		operv1.ClusterNetworkEntry{
			CIDR:       "fd01::/48",
			HostPrefix: 64,
		},
		operv1.ClusterNetworkEntry{
			CIDR:       "fd02::/48",
			HostPrefix: 64,
		},
	)
	err = IsChangeSafe(prev, next)
	g.Expect(err).NotTo(HaveOccurred())
	// ...and vice-versa
	err = IsChangeSafe(next, prev)
	g.Expect(err).NotTo(HaveOccurred())

	// You can't add any new ClusterNetworks of the old IP family
	next = OpenShiftSDNConfig.Spec.DeepCopy()
	FillDefaults(next, nil)
	next.ServiceNetwork = append(next.ServiceNetwork, "fd02::/112")
	next.ClusterNetwork = append(next.ClusterNetwork,
		operv1.ClusterNetworkEntry{
			CIDR:       "fd01::/48",
			HostPrefix: 64,
		},
		operv1.ClusterNetworkEntry{
			CIDR:       "1.2.0.0/16",
			HostPrefix: 24,
		},
	)
	err = IsChangeSafe(prev, next)
	g.Expect(err).To(MatchError(ContainSubstring("cannot change ClusterNetwork")))

	// You can't change default network type to non-target migration network type
	next = OpenShiftSDNConfig.Spec.DeepCopy()
	FillDefaults(next, nil)
	prev.Migration = &operv1.NetworkMigration{NetworkType: "OVNKubernetes"}
	next.DefaultNetwork.Type = "Kuryr"
	err = IsChangeSafe(prev, next)
	g.Expect(err).To(MatchError(ContainSubstring("can only change default network type to the target migration network type")))

	// You can't change the migration network type when it is not null.
	next = OpenShiftSDNConfig.Spec.DeepCopy()
	FillDefaults(next, nil)
	next.Migration = &operv1.NetworkMigration{NetworkType: "OVNKubernetes"}
	prev.Migration = &operv1.NetworkMigration{NetworkType: "Kuryr"}
	err = IsChangeSafe(prev, next)
	g.Expect(err).To(MatchError(ContainSubstring("cannot change migration network type after migration is start")))
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
