package hypershift

import (
	"testing"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func TestParseHostedControlPlane(t *testing.T) {
	testCases := []struct {
		name                     string
		inputUnstructuredContent string
		expectedOutput           *HostedControlPlane
	}{
		{
			name: "Picks up expected IBMCloud HCP fields with apiserver networking set",
			expectedOutput: &HostedControlPlane{
				ClusterID:                    "31df7fa9-b1a7-4a66-98ef-c6920bf213d8",
				ControllerAvailabilityPolicy: HighlyAvailable,
				NodeSelector:                 nil,
				AdvertiseAddress:             "172.20.0.1",
				AdvertisePort:                2040,
			},
			inputUnstructuredContent: `
apiVersion: hypershift.openshift.io/v1beta1
kind: HostedControlPlane
spec:
  autoscaling: {}
  clusterID: 31df7fa9-b1a7-4a66-98ef-c6920bf213d8
  controllerAvailabilityPolicy: HighlyAvailable
  dns:
    baseDomain: tf71faa489656c98b18e2-a383e1dc466c308d41a756a1a66c2b6a-c000.us-south.satellite.test.appdomain.cloud
  infrastructureAvailabilityPolicy: HighlyAvailable
  issuerURL: https://kubernetes.default.svc
  networking:
    apiServer:
      advertiseAddress: 172.20.0.1
      port: 2040
`,
		},
		{
			name: "Picks up defaults appropriately",
			expectedOutput: &HostedControlPlane{
				ClusterID:                    "31df7fa9-b1a7-4a66-98ef-c6920bf213d8",
				ControllerAvailabilityPolicy: HighlyAvailable,
				NodeSelector:                 nil,
				AdvertiseAddress:             HostedClusterDefaultAdvertiseAddressIPV4,
				AdvertisePort:                int(HostedClusterDefaultAdvertisePort),
			},
			inputUnstructuredContent: `
apiVersion: hypershift.openshift.io/v1beta1
kind: HostedControlPlane
spec:
  autoscaling: {}
  clusterID: 31df7fa9-b1a7-4a66-98ef-c6920bf213d8
  controllerAvailabilityPolicy: HighlyAvailable
  dns:
    baseDomain: tf71faa489656c98b18e2-a383e1dc466c308d41a756a1a66c2b6a-c000.us-south.satellite.test.appdomain.cloud
  infrastructureAvailabilityPolicy: HighlyAvailable
  issuerURL: https://kubernetes.default.svc
`,
		},
		{
			name: "Picks up default ipv6 address",
			expectedOutput: &HostedControlPlane{
				ClusterID:                    "31df7fa9-b1a7-4a66-98ef-c6920bf213d8",
				ControllerAvailabilityPolicy: HighlyAvailable,
				NodeSelector:                 nil,
				AdvertiseAddress:             HostedClusterDefaultAdvertiseAddressIPV6,
				AdvertisePort:                2040,
			},
			inputUnstructuredContent: `
apiVersion: hypershift.openshift.io/v1beta1
kind: HostedControlPlane
spec:
  autoscaling: {}
  clusterID: 31df7fa9-b1a7-4a66-98ef-c6920bf213d8
  controllerAvailabilityPolicy: HighlyAvailable
  dns:
    baseDomain: tf71faa489656c98b18e2-a383e1dc466c308d41a756a1a66c2b6a-c000.us-south.satellite.test.appdomain.cloud
  infrastructureAvailabilityPolicy: HighlyAvailable
  issuerURL: https://kubernetes.default.svc
  networking:
    serviceNetwork:
    - cidr: "2001::/16"
    apiServer:
      port: 2040
`,
		},
	}
	g := NewGomegaWithT(t)
	for _, tc := range testCases {
		rawHostedControlPlane, err := yaml.ToJSON([]byte(tc.inputUnstructuredContent))
		g.Expect(err).NotTo(HaveOccurred())
		object, err := runtime.Decode(unstructured.UnstructuredJSONScheme, rawHostedControlPlane)
		g.Expect(err).NotTo(HaveOccurred())
		hcpUnstructured, ok := object.(*unstructured.Unstructured)
		g.Expect(ok).To(BeTrue())
		actualOutput, err := ParseHostedControlPlane(hcpUnstructured)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(actualOutput).To(Equal(tc.expectedOutput))
	}
}

func TestTolerationsToStringSliceYaml(t *testing.T) {
	g := NewGomegaWithT(t)

	testCases := []struct {
		name        string
		tolerations []corev1.Toleration
		expected    []string
	}{
		{
			name: "operator Exists with no key or value should generate valid YAML",
			tolerations: []corev1.Toleration{
				{
					Operator: corev1.TolerationOpExists,
				},
			},
			expected: []string{
				"- operator: Exists",
			},
		},
		{
			name: "operator Equal with key and value should include all fields",
			tolerations: []corev1.Toleration{
				{
					Key:      "node-role.kubernetes.io/master",
					Operator: corev1.TolerationOpEqual,
					Value:    "true",
					Effect:   corev1.TaintEffectNoSchedule,
				},
			},
			expected: []string{
				"- effect: NoSchedule",
				"  key: node-role.kubernetes.io/master",
				"  operator: Equal",
				"  value: \"true\"",
			},
		},
		{
			name: "empty string values should be filtered out",
			tolerations: []corev1.Toleration{
				{
					Key:      "test-key",
					Operator: corev1.TolerationOpEqual,
					Value:    "",
					Effect:   corev1.TaintEffectNoSchedule,
				},
			},
			expected: []string{
				"- effect: NoSchedule",
				"  key: test-key",
				"  operator: Equal",
			},
		},
		{
			name: "operator Exists with key should not include null value",
			tolerations: []corev1.Toleration{
				{
					Key:      "node.kubernetes.io/unreachable",
					Operator: corev1.TolerationOpExists,
					Effect:   corev1.TaintEffectNoExecute,
				},
			},
			expected: []string{
				"- effect: NoExecute",
				"  key: node.kubernetes.io/unreachable",
				"  operator: Exists",
			},
		},
		{
			name: "multiple tolerations should generate proper YAML array",
			tolerations: []corev1.Toleration{
				{
					Key:      "node-role.kubernetes.io/master",
					Operator: corev1.TolerationOpEqual,
					Value:    "true",
					Effect:   corev1.TaintEffectNoSchedule,
				},
				{
					Operator: corev1.TolerationOpExists,
				},
				{
					Key:      "node.kubernetes.io/unreachable",
					Operator: corev1.TolerationOpExists,
					Effect:   corev1.TaintEffectNoExecute,
				},
			},
			expected: []string{
				"- effect: NoSchedule",
				"  key: node-role.kubernetes.io/master",
				"  operator: Equal",
				"  value: \"true\"",
				"- operator: Exists",
				"- effect: NoExecute",
				"  key: node.kubernetes.io/unreachable",
				"  operator: Exists",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tolerationsToStringSliceYaml(tc.tolerations)
			g.Expect(err).NotTo(HaveOccurred())
			if result[len(result)-1] == "" {
				result = result[:len(result)-1]
			}
			g.Expect(result).To(Equal(tc.expected), "Expected exact YAML format match")
		})
	}
}
