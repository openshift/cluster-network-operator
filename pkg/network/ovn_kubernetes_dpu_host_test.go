package network

import (
	"testing"

	"github.com/ghodss/yaml"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openshift/cluster-network-operator/pkg/render"
)

// TestOVNKubernetesNodeModeTemplates tests that both managed and self-hosted templates
// correctly handle different OVN_NODE_MODE values for container inclusion/exclusion and YAML validity
func TestOVNKubernetesNodeModeTemplates(t *testing.T) {

	templates := []struct {
		name         string
		templatePath string
	}{
		{
			name:         "managed",
			templatePath: "../../bindata/network/ovn-kubernetes/managed/ovnkube-node.yaml",
		},
		{
			name:         "self-hosted",
			templatePath: "../../bindata/network/ovn-kubernetes/self-hosted/ovnkube-node.yaml",
		},
	}

	modes := []struct {
		name               string
		ovnNodeMode        string
		expectedContainers []string
		expectedDaemonSet  string
	}{
		{
			name:        "full mode",
			ovnNodeMode: "full",
			expectedContainers: []string{
				"ovn-controller",
				"ovn-acl-logging",
				"kube-rbac-proxy-node",
				"kube-rbac-proxy-ovn-metrics",
				"northd",
				"nbdb",
				"sbdb",
				"ovnkube-controller",
			},
			expectedDaemonSet: "ovnkube-node",
		},
		{
			name:        "smart-nic mode",
			ovnNodeMode: "smart-nic",
			expectedContainers: []string{
				"ovn-controller",
				"ovn-acl-logging",
				"kube-rbac-proxy-node",
				"kube-rbac-proxy-ovn-metrics",
				"northd",
				"nbdb",
				"sbdb",
				"ovnkube-controller",
			},
			expectedDaemonSet: "ovnkube-node-smart-nic",
		},
		{
			name:        "dpu-host mode",
			ovnNodeMode: "dpu-host",
			expectedContainers: []string{
				"kube-rbac-proxy-node",
				"ovnkube-controller",
			},
			expectedDaemonSet: "ovnkube-node-dpu-host",
		},
	}

	for _, template := range templates {
		for _, mode := range modes {
			testName := template.name + "_" + mode.name
			t.Run(testName, func(t *testing.T) {
				g := NewGomegaWithT(t)

				// Create render data
				data := createTestRenderData(mode.ovnNodeMode)

				// Render the template
				objs, err := render.RenderTemplate(template.templatePath, &data)
				g.Expect(err).NotTo(HaveOccurred(), "Template rendering should succeed for %s %s", template.name, mode.name)
				g.Expect(objs).To(HaveLen(1), "Should render exactly one object")

				// Verify it's a DaemonSet with correct name
				obj := objs[0]
				g.Expect(obj.GetKind()).To(Equal("DaemonSet"))
				g.Expect(obj.GetName()).To(Equal(mode.expectedDaemonSet))
				g.Expect(obj.GetNamespace()).To(Equal("openshift-ovn-kubernetes"))

				// Extract container names
				containers, found, err := uns.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(found).To(BeTrue())

				var containerNames []string
				for _, container := range containers {
					cmap := container.(map[string]interface{})
					name, found, err := uns.NestedString(cmap, "name")
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(found).To(BeTrue())
					containerNames = append(containerNames, name)
				}

				// Verify container list exactly matches expected containers
				expectedContainersInterface := make([]interface{}, len(mode.expectedContainers))
				for i, container := range mode.expectedContainers {
					expectedContainersInterface[i] = container
				}
				g.Expect(containerNames).To(ConsistOf(expectedContainersInterface...),
					"Container list for %s %s should exactly match expected containers", template.name, mode.name)

				// Verify YAML validity - the object should be valid YAML
				yamlBytes, err := yaml.Marshal(obj)
				g.Expect(err).NotTo(HaveOccurred(), "Object should be valid YAML for %s %s", template.name, mode.name)
				g.Expect(yamlBytes).NotTo(BeEmpty(), "YAML should not be empty for %s %s", template.name, mode.name)

				// Verify it can be unmarshaled back to a DaemonSet
				ds := &appsv1.DaemonSet{}
				err = yaml.Unmarshal(yamlBytes, ds)
				g.Expect(err).NotTo(HaveOccurred(), "Should be able to unmarshal to DaemonSet for %s %s", template.name, mode.name)
				g.Expect(ds.Kind).To(Equal("DaemonSet"))
			})
		}
	}
}

// createTestRenderData creates a standard render data structure with all required template variables
func createTestRenderData(ovnNodeMode string) render.RenderData {
	data := render.MakeRenderData()
	data.Data["OVN_NODE_MODE"] = ovnNodeMode

	// Set required template variables to avoid rendering errors
	data.Data["OvnImage"] = "registry.redhat.io/openshift4/ose-ovn-kubernetes:latest"
	data.Data["KubeRBACProxyImage"] = "registry.redhat.io/openshift4/ose-kube-rbac-proxy:latest"
	data.Data["ReleaseVersion"] = "4.14.0"
	data.Data["KUBERNETES_SERVICE_PORT"] = "443"
	data.Data["KUBERNETES_SERVICE_HOST"] = "kubernetes.default.svc"
	data.Data["OVN_CONTROLLER_INACTIVITY_PROBE"] = "30000"
	data.Data["OVN_NORTHD_PROBE_INTERVAL"] = "30000"
	data.Data["CNIBinDir"] = "/var/lib/cni/bin"
	data.Data["CNIConfDir"] = "/etc/cni/net.d"
	data.Data["IsSNO"] = false
	data.Data["OVNPlatformAzure"] = false
	data.Data["NETWORK_NODE_IDENTITY_ENABLE"] = false
	data.Data["OVN_NETWORK_SEGMENTATION_ENABLE"] = false
	data.Data["DefaultMasqueradeNetworkCIDRs"] = ""
	data.Data["OVNIPsecEnable"] = false
	data.Data["DpuHostModeLabel"] = ""
	data.Data["SmartNicModeLabel"] = ""
	data.Data["DpuModeLabel"] = ""
	data.Data["MgmtPortResourceName"] = ""
	data.Data["HTTP_PROXY"] = ""
	data.Data["HTTPS_PROXY"] = ""
	data.Data["NO_PROXY"] = ""
	data.Data["NetFlowCollectors"] = ""
	data.Data["SFlowCollectors"] = ""
	data.Data["IPFIXCollectors"] = ""
	data.Data["IPFIXCacheMaxFlows"] = ""
	data.Data["IPFIXCacheActiveTimeout"] = ""
	data.Data["IPFIXSampling"] = ""
	data.Data["K8S_APISERVER"] = "https://test:8443"
	data.Data["OVNKubeConfigHash"] = "test-hash"

	// Additional variables for self-hosted template
	data.Data["IsNetworkTypeLiveMigration"] = false
	data.Data["V4MasqueradeSubnet"] = ""
	data.Data["V6MasqueradeSubnet"] = ""
	data.Data["V4JoinSubnet"] = ""
	data.Data["V6JoinSubnet"] = ""
	data.Data["V4TransitSwitchSubnet"] = ""
	data.Data["V6TransitSwitchSubnet"] = ""
	data.Data["NodeIdentityCertDuration"] = "24h"

	return data
}
