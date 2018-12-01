package network

import (
	"testing"

	. "github.com/onsi/gomega"
)

func TestIsChangeSafe(t *testing.T) {
	g := NewGomegaWithT(t)

	prev := OpenShiftSDNConfig.Spec.DeepCopy()
	FillDefaults(prev)
	next := OpenShiftSDNConfig.Spec.DeepCopy()
	FillDefaults(next)

	err := IsChangeSafe(prev, next)
	g.Expect(err).NotTo(HaveOccurred())

	next.ClusterNetworks[0].HostSubnetLength = 1
	err = IsChangeSafe(prev, next)
	g.Expect(err).To(MatchError(ContainSubstring("cannot change ClusterNetworks")))

	next = OpenShiftSDNConfig.Spec.DeepCopy()
	FillDefaults(next)
	next.ServiceNetwork = "1.2.3.4/99"
	err = IsChangeSafe(prev, next)
	g.Expect(err).To(MatchError(ContainSubstring("cannot change ServiceNetwork")))

	next = OpenShiftSDNConfig.Spec.DeepCopy()
	FillDefaults(next)
	next.DefaultNetwork.Type = "Kuryr"
	err = IsChangeSafe(prev, next)
	g.Expect(err).To(MatchError(ContainSubstring("cannot change default network type")))
}
