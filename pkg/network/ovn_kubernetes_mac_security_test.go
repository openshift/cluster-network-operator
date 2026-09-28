package network

import (
	"testing"

	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	apifeatures "github.com/openshift/api/features"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestRenderMACSecuritySchemas(t *testing.T) {
	for _, gates := range []struct {
		name               string
		macSecurityEnabled bool
		evpnEnabled        bool
	}{
		{name: "both disabled"},
		{name: "MAC security enabled", macSecurityEnabled: true},
		{name: "EVPN enabled", evpnEnabled: true},
		{name: "both enabled", macSecurityEnabled: true, evpnEnabled: true},
	} {
		t.Run(gates.name, func(t *testing.T) {
			g := NewGomegaWithT(t)
			config := OVNKubernetesConfig.DeepCopy()
			fillDefaults(&config.Spec, nil)
			bootstrapResult := fakeBootstrapResult()
			bootstrapResult.OVN = bootstrap.OVNBootstrapResult{
				ControlPlaneReplicaCount: 3,
				OVNKubernetesConfig: &bootstrap.OVNConfigBoostrapResult{
					DpuHostModeLabel:          OVNNodeSelectorDefaultDPUHost,
					DpuModeLabel:              OVNNodeSelectorDefaultDPU,
					SmartNicModeLabel:         OVNNodeSelectorDefaultSmartNIC,
					DpuNodeLeaseRenewInterval: DPUNodeLeaseRenewIntervalDefault,
					DpuNodeLeaseDuration:      DPUNodeLeaseDurationDefault,
					HyperShiftConfig:          &bootstrap.OVNHyperShiftBootstrapResult{},
				},
			}
			var enabled, disabled []configv1.FeatureGateName
			for _, feature := range getDefaultFeatureGates().KnownFeatures() {
				isEnabled := getDefaultFeatureGates().Enabled(feature)
				switch feature {
				case apifeatures.FeatureGateMACSecurity:
					isEnabled = gates.macSecurityEnabled
				case apifeatures.FeatureGateEVPN:
					isEnabled = gates.evpnEnabled
				}
				if isEnabled {
					enabled = append(enabled, feature)
				} else {
					disabled = append(disabled, feature)
				}
			}
			objects, _, err := renderOVNKubernetes(&config.Spec, bootstrapResult, manifestDirOvn,
				featuregates.NewFeatureGate(enabled, disabled))
			g.Expect(err).NotTo(HaveOccurred())

			for _, tc := range []struct {
				name       string
				topologies []string
			}{
				{"userdefinednetworks.k8s.ovn.org", []string{"layer2"}},
				{"clusteruserdefinednetworks.k8s.ovn.org", []string{"layer2", "localnet"}},
			} {
				var crd *unstructured.Unstructured
				for _, obj := range objects {
					if obj.GetKind() == "CustomResourceDefinition" && obj.GetName() == tc.name {
						crd = obj
						break
					}
				}
				g.Expect(crd).NotTo(BeNil(), "rendered CRD %s", tc.name)
				versions, found, err := unstructured.NestedSlice(crd.Object, "spec", "versions")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(found).To(BeTrue())
				for _, version := range versions {
					path := []string{"schema", "openAPIV3Schema", "properties", "spec"}
					if tc.name == "clusteruserdefinednetworks.k8s.ovn.org" {
						path = append(path, "properties", "network")
					}
					network, found, err := unstructured.NestedMap(version.(map[string]any), path...)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(found).To(BeTrue())
					for _, topology := range tc.topologies {
						schema, found, err := unstructured.NestedMap(network, "properties", topology)
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(found).To(BeTrue())
						macSecurity, found, err := unstructured.NestedMap(schema, "properties", "macSecurity")
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(found).To(Equal(gates.macSecurityEnabled), "%s %s macSecurity exposure", tc.name, topology)
						validations, found, err := unstructured.NestedSlice(schema, "x-kubernetes-validations")
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(found).To(BeTrue())
						if !gates.macSecurityEnabled {
							g.Expect(validations).NotTo(ContainElement(HaveKeyWithValue("rule", ContainSubstring("macSecurity"))),
								"%s %s must not reference the disabled field", tc.name, topology)
							continue
						}
						g.Expect(macSecurity["required"]).To(ConsistOf("mode"))
						mode, found, err := unstructured.NestedMap(macSecurity, "properties", "mode")
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(found).To(BeTrue())
						g.Expect(mode["enum"]).To(ConsistOf("Enabled", "Disabled"))
						g.Expect(mode).NotTo(HaveKey("default"))

						g.Expect(validations).To(ContainElement(HaveKeyWithValue("rule",
							"!has(self.macSecurity) || self.macSecurity.mode != 'Disabled' || (has(self.ipam) && has(self.ipam.mode) && self.ipam.mode == 'Disabled')")))
						if topology == "layer2" {
							g.Expect(validations).To(ContainElement(HaveKeyWithValue("rule",
								"!has(self.macSecurity) || self.role == 'Secondary'")))
						}
					}
					// Existing network-level immutability also protects macSecurity.
					g.Expect(network["x-kubernetes-validations"]).To(ContainElement(HaveKeyWithValue("rule", "self == oldSelf")))
					_, found, err = unstructured.NestedMap(network, "properties", "layer3", "properties", "macSecurity")
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(found).To(BeFalse(), "macSecurity is not supported on Layer3")
				}
			}
		})
	}
}
