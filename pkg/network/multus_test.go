package network

import (
	"testing"

	operv1 "github.com/openshift/api/operator/v1"

	. "github.com/onsi/gomega"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var MultusConfig = operv1.Network{
	Spec: operv1.NetworkSpec{
		ServiceNetwork: []string{"172.30.0.0/16"},
		ClusterNetwork: []operv1.ClusterNetworkEntry{
			{
				CIDR:       "10.128.0.0/15",
				HostPrefix: 23,
			},
		},
		DefaultNetwork: operv1.DefaultNetworkDefinition{
			Type: operv1.NetworkTypeOVNKubernetes,
		},
	},
}

// TestRenderMultus has some simple rendering tests
func TestRenderMultus(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := MultusConfig.DeepCopy()
	config := &crd.Spec
	disabled := true
	config.DisableMultiNetwork = &disabled
	fillDefaults(config, nil)

	// disable Multus
	objs, err := renderMultus(config, fakeBootstrapResult(), manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).NotTo(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "multus")))

	// enable Multus
	enabled := false
	config.DisableMultiNetwork = &enabled
	objs, err = renderMultus(config, fakeBootstrapResult(), manifestDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "multus")))

	// It's important that the namespace is first
	g.Expect(len(objs)).To(Equal(32), "Expected 32 multus related objects")
	g.Expect(objs[0]).To(HaveKubernetesID("CustomResourceDefinition", "", "network-attachment-definitions.k8s.cni.cncf.io"))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("Namespace", "", "openshift-multus")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ServiceAccount", "openshift-multus", "multus-ancillary-tools")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ClusterRole", "", "multus-ancillary-tools")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ClusterRoleBinding", "", "multus-ancillary-tools")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ClusterRole", "", "multus")))
	g.Expect(objs).To(ContainElement(HaveKubernetesID("DaemonSet", "openshift-multus", "multus")))
}

// TestMultusServiceAccountWithoutNodeIdentity tests service account is set when node identity is disabled
func TestMultusServiceAccountWithoutNodeIdentity(t *testing.T) {
	g := NewGomegaWithT(t)

	crd := MultusConfig.DeepCopy()
	config := &crd.Spec
	enabled := false
	config.DisableMultiNetwork = &enabled
	fillDefaults(config, nil)

	// Test without node identity - should have service account
	bootstrapWithoutNodeIdentity := fakeBootstrapResult()
	bootstrapWithoutNodeIdentity.Infra.NetworkNodeIdentityEnabled = false

	objs, err := renderMultus(config, bootstrapWithoutNodeIdentity, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())

	daemonSet := findDaemonSet(objs, "openshift-multus", "multus")
	g.Expect(daemonSet).NotTo(BeNil())

	serviceAccount, found, err := uns.NestedString(daemonSet.Object, "spec", "template", "spec", "serviceAccountName")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(serviceAccount).To(Equal("multus"))

	// Test with node identity - should NOT have service account (uses default)
	bootstrapWithNodeIdentity := fakeBootstrapResult()
	bootstrapWithNodeIdentity.Infra.NetworkNodeIdentityEnabled = true

	objs, err = renderMultus(config, bootstrapWithNodeIdentity, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())

	daemonSet = findDaemonSet(objs, "openshift-multus", "multus")
	g.Expect(daemonSet).NotTo(BeNil())

	_, found, err = uns.NestedString(daemonSet.Object, "spec", "template", "spec", "serviceAccountName")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeFalse(), "serviceAccountName should not be set when NETWORK_NODE_IDENTITY_ENABLE=true")
}

func findDaemonSet(objs []*uns.Unstructured, namespace, name string) *uns.Unstructured {
	for _, obj := range objs {
		if obj.GetKind() == "DaemonSet" && obj.GetNamespace() == namespace && obj.GetName() == name {
			return obj
		}
	}
	return nil
}
