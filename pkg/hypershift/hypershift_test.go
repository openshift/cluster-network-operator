package hypershift

import (
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	"testing"
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
