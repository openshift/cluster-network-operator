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

func TestParseHostedControlPlaneRestartDate(t *testing.T) {
	g := NewGomegaWithT(t)
	input := `
apiVersion: hypershift.openshift.io/v1beta1
kind: HostedControlPlane
metadata:
  annotations:
    hypershift.openshift.io/restart-date: "2024-01-15T10:30:00Z"
spec:
  clusterID: test-cluster-id
  controllerAvailabilityPolicy: SingleReplica
`
	rawHCP, err := yaml.ToJSON([]byte(input))
	g.Expect(err).NotTo(HaveOccurred())
	object, err := runtime.Decode(unstructured.UnstructuredJSONScheme, rawHCP)
	g.Expect(err).NotTo(HaveOccurred())
	hcpUnstructured, ok := object.(*unstructured.Unstructured)
	g.Expect(ok).To(BeTrue())
	result, err := ParseHostedControlPlane(hcpUnstructured)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result.RestartDate).To(Equal("2024-01-15T10:30:00Z"))
}

func TestSetRestartDateAnnotation(t *testing.T) {
	g := NewGomegaWithT(t)

	hcpNS := "clusters-test-hc"

	makeObj := func(apiVersion, kind, name, ns string) *unstructured.Unstructured {
		return &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": apiVersion,
				"kind":       kind,
				"metadata": map[string]interface{}{
					"name":      name,
					"namespace": ns,
				},
				"spec": map[string]interface{}{
					"template": map[string]interface{}{
						"metadata": map[string]interface{}{},
					},
				},
			},
		}
	}

	t.Run("sets annotation on Deployment pod template", func(t *testing.T) {
		obj := makeObj("apps/v1", "Deployment", "cloud-network-config-controller", hcpNS)
		err := SetRestartDateAnnotation([]*unstructured.Unstructured{obj}, hcpNS, "2024-01-15T10:30:00Z")
		g.Expect(err).NotTo(HaveOccurred())
		anno, _, err := unstructured.NestedStringMap(obj.Object, "spec", "template", "metadata", "annotations")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(anno).To(HaveKeyWithValue(RestartDateAnnotation, "2024-01-15T10:30:00Z"))
	})

	t.Run("sets annotation on DaemonSet pod template", func(t *testing.T) {
		obj := makeObj("apps/v1", "DaemonSet", "ovnkube-node", hcpNS)
		err := SetRestartDateAnnotation([]*unstructured.Unstructured{obj}, hcpNS, "2024-01-15T10:30:00Z")
		g.Expect(err).NotTo(HaveOccurred())
		anno, _, err := unstructured.NestedStringMap(obj.Object, "spec", "template", "metadata", "annotations")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(anno).To(HaveKeyWithValue(RestartDateAnnotation, "2024-01-15T10:30:00Z"))
	})

	t.Run("sets annotation on StatefulSet pod template", func(t *testing.T) {
		obj := makeObj("apps/v1", "StatefulSet", "test-statefulset", hcpNS)
		err := SetRestartDateAnnotation([]*unstructured.Unstructured{obj}, hcpNS, "2024-01-15T10:30:00Z")
		g.Expect(err).NotTo(HaveOccurred())
		anno, _, err := unstructured.NestedStringMap(obj.Object, "spec", "template", "metadata", "annotations")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(anno).To(HaveKeyWithValue(RestartDateAnnotation, "2024-01-15T10:30:00Z"))
	})

	t.Run("skips non-apps/v1 objects", func(t *testing.T) {
		obj := makeObj("v1", "ConfigMap", "test-cm", hcpNS)
		err := SetRestartDateAnnotation([]*unstructured.Unstructured{obj}, hcpNS, "2024-01-15T10:30:00Z")
		g.Expect(err).NotTo(HaveOccurred())
		_, found, err := unstructured.NestedStringMap(obj.Object, "spec", "template", "metadata", "annotations")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(found).To(BeFalse())
	})

	t.Run("skips objects outside HCP namespace", func(t *testing.T) {
		obj := makeObj("apps/v1", "DaemonSet", "ovnkube-node", "openshift-ovn-kubernetes")
		err := SetRestartDateAnnotation([]*unstructured.Unstructured{obj}, hcpNS, "2024-01-15T10:30:00Z")
		g.Expect(err).NotTo(HaveOccurred())
		_, found, err := unstructured.NestedStringMap(obj.Object, "spec", "template", "metadata", "annotations")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(found).To(BeFalse())
	})

	t.Run("preserves existing pod template annotations", func(t *testing.T) {
		obj := makeObj("apps/v1", "Deployment", "test-deploy", hcpNS)
		err := unstructured.SetNestedStringMap(obj.Object, map[string]string{"existing": "value"}, "spec", "template", "metadata", "annotations")
		g.Expect(err).NotTo(HaveOccurred())
		err = SetRestartDateAnnotation([]*unstructured.Unstructured{obj}, hcpNS, "2024-01-15T10:30:00Z")
		g.Expect(err).NotTo(HaveOccurred())
		anno, _, err := unstructured.NestedStringMap(obj.Object, "spec", "template", "metadata", "annotations")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(anno).To(HaveKeyWithValue("existing", "value"))
		g.Expect(anno).To(HaveKeyWithValue(RestartDateAnnotation, "2024-01-15T10:30:00Z"))
	})

	t.Run("handles multiple objects", func(t *testing.T) {
		deploy := makeObj("apps/v1", "Deployment", "cncc", hcpNS)
		ds := makeObj("apps/v1", "DaemonSet", "multus", hcpNS)
		cm := makeObj("v1", "ConfigMap", "config", hcpNS)
		objs := []*unstructured.Unstructured{deploy, ds, cm}
		err := SetRestartDateAnnotation(objs, hcpNS, "2024-01-15T10:30:00Z")
		g.Expect(err).NotTo(HaveOccurred())

		anno, _, err := unstructured.NestedStringMap(deploy.Object, "spec", "template", "metadata", "annotations")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(anno).To(HaveKeyWithValue(RestartDateAnnotation, "2024-01-15T10:30:00Z"))

		anno, _, err = unstructured.NestedStringMap(ds.Object, "spec", "template", "metadata", "annotations")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(anno).To(HaveKeyWithValue(RestartDateAnnotation, "2024-01-15T10:30:00Z"))

		_, found, err := unstructured.NestedStringMap(cm.Object, "spec", "template", "metadata", "annotations")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(found).To(BeFalse())
	})
}
