package network

import (
	"path/filepath"
	"testing"

	operv1 "github.com/openshift/api/operator/v1"

	. "github.com/onsi/gomega"
	"github.com/openshift/cluster-network-operator/pkg/render"
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

	// Verify that multus-node-identity service account is NOT created when node identity is disabled
	g.Expect(objs).NotTo(ContainElement(HaveKubernetesID("ServiceAccount", "openshift-multus", "multus-node-identity")))

	// Test with node identity - should use multus-node-identity service account
	bootstrapWithNodeIdentity := fakeBootstrapResult()
	bootstrapWithNodeIdentity.Infra.NetworkNodeIdentityEnabled = true

	objs, err = renderMultus(config, bootstrapWithNodeIdentity, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())

	daemonSet = findDaemonSet(objs, "openshift-multus", "multus")
	g.Expect(daemonSet).NotTo(BeNil())

	serviceAccount, found, err = uns.NestedString(daemonSet.Object, "spec", "template", "spec", "serviceAccountName")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue(), "serviceAccountName should be set when NETWORK_NODE_IDENTITY_ENABLE=true")
	g.Expect(serviceAccount).To(Equal("multus-node-identity"), "daemonset should use multus-node-identity service account when node identity is enabled")

	// Verify that multus-node-identity service account is created when node identity is enabled
	g.Expect(objs).To(ContainElement(HaveKubernetesID("ServiceAccount", "openshift-multus", "multus-node-identity")))
}

func findDaemonSet(objs []*uns.Unstructured, namespace, name string) *uns.Unstructured {
	for _, obj := range objs {
		if obj.GetKind() == "DaemonSet" && obj.GetNamespace() == namespace && obj.GetName() == name {
			return obj
		}
	}
	return nil
}

// TestAllowlistDaemonSetServiceAccount verifies that cni-sysctl-allowlist-ds is using its bespoke service account
func TestAllowlistDaemonSetServiceAccount(t *testing.T) {
	g := NewGomegaWithT(t)

	// Render the allowlist daemonset manifests
	data := render.MakeRenderData()
	data.Data["MultusImage"] = "test-multus-image:latest"
	data.Data["CniSysctlAllowlist"] = "cni-sysctl-allowlist"
	data.Data["ReleaseVersion"] = "test-version"

	objs, err := render.RenderDir(filepath.Join(manifestDir, "allowlist/daemonset"), &data)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(objs).NotTo(BeEmpty())

	// Find the cni-sysctl-allowlist-ds daemonset
	daemonSet := findDaemonSet(objs, "openshift-multus", "cni-sysctl-allowlist-ds")
	g.Expect(daemonSet).NotTo(BeNil(), "cni-sysctl-allowlist-ds daemonset should be rendered")

	// Verify that the serviceAccountName is set to multus-ancillary-tools
	serviceAccount, found, err := uns.NestedString(daemonSet.Object, "spec", "template", "spec", "serviceAccountName")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue(), "serviceAccountName should be set in the daemonset")
	g.Expect(serviceAccount).To(Equal("multus-ancillary-tools"), "daemonset should use multus-ancillary-tools service account")
}
