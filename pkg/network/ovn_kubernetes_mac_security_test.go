package network

import (
	"fmt"
	"testing"

	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	apifeatures "github.com/openshift/api/features"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TestRenderMACSecuritySchemas checks API exposure and validation constraints
// independently of the OVN operand, including omission when the gate is disabled.
func TestRenderMACSecuritySchemas(t *testing.T) {
	for _, gates := range []struct {
		name               string
		macSecurityEnabled bool
	}{
		{name: "MAC security disabled"},
		{name: "MAC security enabled", macSecurityEnabled: true},
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
				if feature == apifeatures.FeatureGateMACSecurity {
					isEnabled = gates.macSecurityEnabled
				}
				if isEnabled {
					enabled = append(enabled, feature)
				} else {
					disabled = append(disabled, feature)
				}
			}
			objects, _, err := renderOVNKubernetes(&config.Spec, bootstrapResult, manifestDirOvn,
				featuregates.NewFeatureGate(enabled, disabled))
			g.Expect(err).NotTo(HaveOccurred(), "render OVN manifests with %s", gates.name)

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
				versions, found, versionsErr := unstructured.NestedSlice(crd.Object, "spec", "versions")
				g.Expect(versionsErr).NotTo(HaveOccurred(), "%s: read CRD versions", tc.name)
				g.Expect(found).To(BeTrue(), "%s: CRD versions must exist", tc.name)
				for _, version := range versions {
					versionSchema := version.(map[string]any)
					versionContext := fmt.Sprintf("%s %s", tc.name, versionSchema["name"])
					path := []string{"schema", "openAPIV3Schema", "properties", "spec"}
					if tc.name == "clusteruserdefinednetworks.k8s.ovn.org" {
						path = append(path, "properties", "network")
					}
					network, found, networkErr := unstructured.NestedMap(versionSchema, path...)
					g.Expect(networkErr).NotTo(HaveOccurred(), "%s: read network schema", versionContext)
					g.Expect(found).To(BeTrue(), "%s: network schema must exist", versionContext)
					for _, topology := range tc.topologies {
						context := versionContext + " " + topology
						schema, found, schemaErr := unstructured.NestedMap(network, "properties", topology)
						g.Expect(schemaErr).NotTo(HaveOccurred(), "%s: read topology schema", context)
						g.Expect(found).To(BeTrue(), "%s: topology schema must exist", context)
						macSecurity, found, schemaErr := unstructured.NestedMap(schema, "properties", "macSecurity")
						g.Expect(schemaErr).NotTo(HaveOccurred(), "%s: read macSecurity", context)
						g.Expect(found).To(Equal(gates.macSecurityEnabled), "%s: macSecurity exposure", context)
						validations, found, schemaErr := unstructured.NestedSlice(schema, "x-kubernetes-validations")
						g.Expect(schemaErr).NotTo(HaveOccurred(), "%s: read CEL validations", context)
						g.Expect(found).To(BeTrue(), "%s: CEL validations must exist", context)
						if !gates.macSecurityEnabled {
							g.Expect(validations).NotTo(ContainElement(HaveKeyWithValue("rule", ContainSubstring("macSecurity"))),
								"%s: CEL must not reference the disabled field", context)
							continue
						}
						g.Expect(macSecurity["required"]).To(ConsistOf("mode"), "%s: macSecurity requires mode", context)
						mode, found, schemaErr := unstructured.NestedMap(macSecurity, "properties", "mode")
						g.Expect(schemaErr).NotTo(HaveOccurred(), "%s: read macSecurity.mode", context)
						g.Expect(found).To(BeTrue(), "%s: macSecurity.mode must exist", context)
						g.Expect(mode["enum"]).To(ConsistOf("Enabled", "Disabled"), "%s: allowed MAC security modes", context)
						g.Expect(mode).NotTo(HaveKey("default"), "%s: leave default protection to OVN", context)

						g.Expect(validations).To(ContainElement(HaveKeyWithValue("rule",
							"!has(self.macSecurity) || self.macSecurity.mode != 'Disabled' || (has(self.ipam) && has(self.ipam.mode) && self.ipam.mode == 'Disabled')")),
							"%s: disabling MAC security requires disabled IPAM", context)
						if topology == "layer2" {
							g.Expect(validations).To(ContainElement(HaveKeyWithValue("rule",
								"!has(self.macSecurity) || self.role == 'Secondary'")),
								"%s: MAC security requires the secondary role", context)
						}
					}
					// Existing network-level immutability also protects macSecurity.
					g.Expect(network["x-kubernetes-validations"]).To(ContainElement(HaveKeyWithValue("rule", "self == oldSelf")),
						"%s: network configuration must remain immutable", versionContext)
					_, found, networkErr = unstructured.NestedMap(network, "properties", "layer3", "properties", "macSecurity")
					g.Expect(networkErr).NotTo(HaveOccurred(), "%s: inspect Layer3 schema", versionContext)
					g.Expect(found).To(BeFalse(), "%s: macSecurity is not supported on Layer3", versionContext)
				}
			}
		})
	}
}
