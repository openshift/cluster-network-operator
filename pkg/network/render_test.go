package network

import (
	"testing"

	. "github.com/onsi/gomega"

	operv1 "github.com/openshift/api/operator/v1"
)

func TestIsChangeSafe(t *testing.T) {
	g := NewGomegaWithT(t)

	// NOTE: IsChangeSafe() requires you to have called Validate() beforehand, so we
	// don't have to check that invalid configs are considered unsafe to change to.

	// OpenShiftSDN validation
	// =================================

	prev := OpenShiftSDNConfig.Spec.DeepCopy()
	FillDefaults(prev, nil)
	next := OpenShiftSDNConfig.Spec.DeepCopy()
	FillDefaults(next, nil)

	// No error should occur when prev equals next.
	err := IsChangeSafe(prev, next)
	g.Expect(err).NotTo(HaveOccurred())

	// Changes to the cluster network's prefix are not supported.
	next.ClusterNetwork[0].HostPrefix = 31
	err = IsChangeSafe(prev, next)
	g.Expect(err).To(MatchError(ContainSubstring("unsupported change to ClusterNetwork")))

	// It is not supported to append another cluster network of the same type.
	next = OpenShiftSDNConfig.Spec.DeepCopy()
	FillDefaults(next, nil)
	next.ClusterNetwork = append(next.ClusterNetwork, operv1.ClusterNetworkEntry{
		CIDR:       "1.2.0.0/16",
		HostPrefix: 24,
	})
	err = IsChangeSafe(prev, next)
	g.Expect(err).To(MatchError(ContainSubstring("unsupported change to ClusterNetwork")))

	// It is not supported to change the ServiceNetwork.
	next = OpenShiftSDNConfig.Spec.DeepCopy()
	FillDefaults(next, nil)
	next.ServiceNetwork = []string{"1.2.3.0/24"}
	err = IsChangeSafe(prev, next)
	g.Expect(err).To(MatchError(ContainSubstring("unsupported change to ServiceNetwork")))

	// Migration from OpenShiftSDN to OVNKubernetes validation
	// =================================

	prev = OpenShiftSDNConfig.Spec.DeepCopy()
	FillDefaults(prev, nil)
	prev.Migration = &operv1.NetworkMigration{NetworkType: "OVNKubernetes"}
	next = OVNKubernetesConfig.Spec.DeepCopy()
	FillDefaults(next, nil)

	// You can change cluster network during migration.
	next.ClusterNetwork = append(next.ClusterNetwork,
		operv1.ClusterNetworkEntry{
			CIDR:       "1.2.0.0/16",
			HostPrefix: 24,
		},
	)
	err = IsChangeSafe(prev, next)
	g.Expect(err).NotTo(HaveOccurred())

	// You can't change service network during migration.
	next = OVNKubernetesConfig.Spec.DeepCopy()
	FillDefaults(next, nil)
	next.ServiceNetwork = []string{"1.2.3.0/24"}
	err = IsChangeSafe(prev, next)
	g.Expect(err).To(MatchError(ContainSubstring("cannot change ServiceNetwork during migration")))

	// Invalid miscellaneous migration validation
	// =================================

	prev = OpenShiftSDNConfig.Spec.DeepCopy()
	FillDefaults(prev, nil)
	next = OpenShiftSDNConfig.Spec.DeepCopy()
	FillDefaults(prev, nil)

	// You can't change default network type when not doing migration.
	next.DefaultNetwork.Type = "Kuryr"
	err = IsChangeSafe(prev, next)
	g.Expect(err).To(MatchError(ContainSubstring("cannot change default network type when not doing migration")))

	// You can't change default network type to non-target migration network type.
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
	g.Expect(err).To(MatchError(ContainSubstring("cannot change migration network type after migration has started")))

	// OVNKubernetes DualStack validation
	// =================================

	prev = OVNKubernetesConfig.Spec.DeepCopy()
	FillDefaults(prev, nil)
	next = OVNKubernetesConfig.Spec.DeepCopy()
	FillDefaults(next, nil)

	// You can change a single-stack config to dual-stack ...
	next.ServiceNetwork = append(next.ServiceNetwork, "fd02::/112")
	next.ClusterNetwork = append(next.ClusterNetwork, operv1.ClusterNetworkEntry{
		CIDR:       "fd01::/48",
		HostPrefix: 64,
	})
	err = IsChangeSafe(prev, next)
	g.Expect(err).NotTo(HaveOccurred())
	// ... and vice-versa.
	err = IsChangeSafe(next, prev)
	g.Expect(err).NotTo(HaveOccurred())

	// But you can't change the ServiceNetwork from single-stack IPv4 to dual-stack IPv6-primary ...
	next = OVNKubernetesConfig.Spec.DeepCopy()
	FillDefaults(next, nil)
	next.ServiceNetwork = append([]string{"fd02::/112"}, prev.ServiceNetwork...)
	next.ClusterNetwork = append([]operv1.ClusterNetworkEntry{{
		CIDR:       "fd01::/48",
		HostPrefix: 64,
	}}, prev.ClusterNetwork...)
	err = IsChangeSafe(prev, next)
	g.Expect(err).To(MatchError(ContainSubstring("cannot change primary ServiceNetwork when migrating to/from dual-stack")))
	// ... or vice-versa.
	err = IsChangeSafe(next, prev)
	g.Expect(err).To(MatchError(ContainSubstring("cannot change primary ServiceNetwork when migrating to/from dual-stack")))

	// You also cannot change the ClusterNetwork from single-stack IPv4 to dual-stack IPv6-primary ...
	next = OVNKubernetesConfig.Spec.DeepCopy()
	FillDefaults(next, nil)
	next.ServiceNetwork = append(next.ServiceNetwork, "fd02::/112")
	next.ClusterNetwork = append([]operv1.ClusterNetworkEntry{{
		CIDR:       "fd01::/48",
		HostPrefix: 64,
	}}, prev.ClusterNetwork...)
	err = IsChangeSafe(prev, next)
	g.Expect(err).To(MatchError(ContainSubstring("cannot change primary ClusterNetwork when migrating to/from dual-stack")))
	// ... or vice-versa.
	err = IsChangeSafe(next, prev)
	g.Expect(err).To(MatchError(ContainSubstring("cannot change primary ClusterNetwork when migrating to/from dual-stack")))

	// You can add multiple ClusterNetworks of the new IP family ...
	next = OVNKubernetesConfig.Spec.DeepCopy()
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
	// ... and vice-versa.
	err = IsChangeSafe(next, prev)
	g.Expect(err).NotTo(HaveOccurred())

	// You can't add any new ClusterNetworks of the old IP family.
	next = OVNKubernetesConfig.Spec.DeepCopy()
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
	g.Expect(err).To(MatchError(ContainSubstring("cannot add additional ClusterNetwork values of original IP family when migrating to dual stack")))
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

	bootstrapResult, err := Bootstrap(&config, nil)
	g.Expect(err).NotTo(HaveOccurred())

	objs, err := Render(prev, bootstrapResult, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())

	// Validate that openshift-sdn isn't rendered
	g.Expect(objs).NotTo(ContainElement(HaveKubernetesID("DaemonSet", "openshift-sdn", "ovs")))

	// validate that Multus is still rendered
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "multus")))

	// validate that the openshift-network-features namespace and role bindings are still rendered
	g.Expect(objs).To(ContainElement(HaveKubernetesID("Role", "openshift-config-managed", "openshift-network-public-role")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("RoleBinding", "openshift-config-managed", "openshift-network-public-role-binding")))

	// TODO(cdc) validate that kube-proxy is rendered
}
