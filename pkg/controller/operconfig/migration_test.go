package operconfig

import (
	"context"
	"fmt"
	"strings"
	"testing"

	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	v1 "github.com/openshift/api/network/v1"
	"github.com/openshift/cluster-network-operator/pkg/client/fake"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const testMigrationNamespace = "openshift-multus"
const testMigrationHost = "egressip-test-host"
const fakeMcName = "fakeMC"

func init() {
	utilruntime.Must(v1.AddToScheme(scheme.Scheme))
}

func TestEgressIpMigration(t *testing.T) {
	nnsEgressIpsStrArrSingle := []string{"10.0.128.5"}
	nnsEgressIpsIfcArrSingle := ConvertToUnstructuredInterface(nnsEgressIpsStrArrSingle)
	nnsEgressIpsStrArrMult := []string{"10.0.128.5", "10.0.128.6", "10.0.128.7"}
	nnsEgressIpsIfcArrMult := ConvertToUnstructuredInterface(nnsEgressIpsStrArrMult)

	testCases := []struct {
		name                 string
		objects              []crclient.Object
		expectedEgressIpList []string
	}{
		{
			name: "Hostsubnet has automatic config and netnamespace has one egressIP",
			objects: []crclient.Object{
				&v1.HostSubnet{
					ObjectMeta: metav1.ObjectMeta{
						Name: testMigrationHost,
					},
					EgressCIDRs: []v1.HostSubnetEgressCIDR{"10.0.128.0/17"},
					EgressIPs:   []v1.HostSubnetEgressIP{"10.0.128.5"},
					Host:        testMigrationHost,
				},
				&uns.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "network.openshift.io/v1",
						"kind":       "NetNamespace",
						"egressIPs":  nnsEgressIpsIfcArrSingle,
						"netname":    testMigrationNamespace,
						"metadata": map[string]interface{}{
							"name": testMigrationNamespace,
						},
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: testMigrationHost,
					},
				},
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: testMigrationNamespace,
					},
				},
			},
			expectedEgressIpList: nnsEgressIpsStrArrSingle,
		},
		{
			name: "Two hostsubnets have automatic config and netnamespace has one egressIP",
			objects: []crclient.Object{
				&v1.HostSubnet{
					ObjectMeta: metav1.ObjectMeta{
						Name: testMigrationHost,
					},
					EgressCIDRs: []v1.HostSubnetEgressCIDR{"10.0.128.0/17"},
					EgressIPs:   []v1.HostSubnetEgressIP{"10.0.128.5"},
					Host:        testMigrationHost,
				},
				&v1.HostSubnet{
					ObjectMeta: metav1.ObjectMeta{
						Name: "no-egressip-node",
					},
					EgressCIDRs: []v1.HostSubnetEgressCIDR{"10.0.128.0/17"},
					Host:        testMigrationHost,
				},
				&uns.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "network.openshift.io/v1",
						"kind":       "NetNamespace",
						"egressIPs":  nnsEgressIpsIfcArrSingle,
						"netname":    testMigrationNamespace,
						"metadata": map[string]interface{}{
							"name": testMigrationNamespace,
						},
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: testMigrationHost,
					},
				},
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: testMigrationNamespace,
					},
				},
			},
			expectedEgressIpList: nnsEgressIpsStrArrSingle,
		},
		{
			name: "Hostsubnet has automatic config and netnamespace has multiple egressIPs",
			objects: []crclient.Object{
				&v1.HostSubnet{
					ObjectMeta: metav1.ObjectMeta{
						Name: testMigrationHost,
					},
					EgressCIDRs: []v1.HostSubnetEgressCIDR{"10.0.128.0/17"},
					EgressIPs:   []v1.HostSubnetEgressIP{"10.0.128.5", "10.0.128.6", "10.0.128.7"},
					Host:        testMigrationHost,
				},
				&uns.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "network.openshift.io/v1",
						"kind":       "NetNamespace",
						"egressIPs":  nnsEgressIpsIfcArrMult,
						"netname":    testMigrationNamespace,
						"metadata": map[string]interface{}{
							"name": testMigrationNamespace,
						},
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: testMigrationHost,
					},
				},
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: testMigrationNamespace,
					},
				},
			},
			expectedEgressIpList: nnsEgressIpsStrArrMult,
		},
		{
			name: "Hostsubnet has manual config and netnamespace has one egressIP",
			objects: []crclient.Object{
				&v1.HostSubnet{
					ObjectMeta: metav1.ObjectMeta{
						Name: testMigrationHost,
					},
					EgressIPs: []v1.HostSubnetEgressIP{"10.0.128.5"},
					Host:      testMigrationHost,
				},
				&uns.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "network.openshift.io/v1",
						"kind":       "NetNamespace",
						"egressIPs":  nnsEgressIpsIfcArrSingle,
						"netname":    testMigrationNamespace,
						"metadata": map[string]interface{}{
							"name": testMigrationNamespace,
						},
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: testMigrationHost,
					},
				},
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: testMigrationNamespace,
					},
				},
			},
			expectedEgressIpList: nnsEgressIpsStrArrSingle,
		},
		{
			name: "Hostsubnet has manual config and netnamespace has multiple egressIPs",
			objects: []crclient.Object{
				&v1.HostSubnet{
					ObjectMeta: metav1.ObjectMeta{
						Name: testMigrationHost,
					},
					EgressIPs: []v1.HostSubnetEgressIP{"10.0.128.5", "10.0.128.6", "10.0.128.7"},
					Host:      testMigrationHost,
				},
				&uns.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "network.openshift.io/v1",
						"kind":       "NetNamespace",
						"egressIPs":  nnsEgressIpsIfcArrMult,
						"netname":    testMigrationNamespace,
						"metadata": map[string]interface{}{
							"name": testMigrationNamespace,
						},
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: testMigrationHost,
					},
				},
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: testMigrationNamespace,
					},
				},
			},
			expectedEgressIpList: nnsEgressIpsStrArrMult,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			client := fake.NewFakeClient(tc.objects...)

			egressIpList, _, err := convertSdnEgressIpToOvnEgressIp(context.Background(), client)
			if err != nil {
				t.Fatalf("convertSdnEgressIpToOvnEgressIp: %v", err)
			}
			// Collect all egressIP strings in a single slice
			var egressIpValueList []string
			for _, egressIp := range egressIpList {
				egressIpsSlice := egressIp.Object["spec"].(map[string]interface{})["egressIPs"].([]interface{})
				egressIpsStringSlice := make([]string, len(egressIpsSlice))
				for i := range egressIpsSlice {
					egressIpsStringSlice[i] = egressIpsSlice[i].(string)
				}
				egressIpValueList = append(egressIpValueList, egressIpsStringSlice...)
			}

			// Check if values match exactly
			expectedEgressIpList := ConvertToUnstructuredInterface(tc.expectedEgressIpList)
			g.Expect(egressIpValueList).To(ConsistOf(expectedEgressIpList...), "expected applied OVN egressIPs to have egressIP list matching input SDN config")

			// Check that node annotation has been added
			nodeObj, err := client.Default().Kubernetes().CoreV1().Nodes().Get(context.Background(), testMigrationHost, metav1.GetOptions{})
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			g.Expect(nodeObj.Labels).To(HaveKey(egressAssignable), "expected node to have egress-assignable label")
			if !strings.Contains(tc.name, "manual config") {
				g.Expect(nodeObj.Annotations).To(HaveKey(egressCIDRAnnotationName), "expected node to have migration annotation")
				g.Expect(nodeObj.Annotations[egressCIDRAnnotationName]).To(Equal("{\"EgressCIDRs\":[\"10.0.128.0/17\"]}"))
			}
		})
	}
}

func TestEgressIpRollbackMigration(t *testing.T) {
	egressIpsStrArrSingle := []string{"10.0.128.5"}
	egressIpsIfcArrSingle := ConvertToUnstructuredInterface(egressIpsStrArrSingle)
	egressIpsStrArrMult := []string{"10.0.128.5", "10.0.128.6", "10.0.128.7"}
	egressIpsIfcArrMult := ConvertToUnstructuredInterface(egressIpsStrArrMult)

	egressCIDRsStrArr := []string{"10.0.128.0/17"}
	nodeAnnotationMap := make(map[string]string, 0)
	nodeAnnotationMap[egressIPNodeConfig] = "[{\"interface\":\"nic0\",\"ifaddr\":{\"ipv4\":\"10.0.128.0/17\"},\"capacity\":{\"ip\":10}}]"

	egressCIDRsStrArrWithEgressCidrAnnotation := []string{"10.0.128.0/18"}
	nodeAnnotationMapWithEgressCidrAnnotation := make(map[string]string, 0)
	nodeAnnotationMapWithEgressCidrAnnotation[egressIPNodeConfig] = "[{\"interface\":\"nic0\",\"ifaddr\":{\"ipv4\":\"10.0.128.0/17\"},\"capacity\":{\"ip\":10}}]"
	nodeAnnotationMapWithEgressCidrAnnotation[egressCIDRAnnotationName] = "{\"EgressCIDRs\":[\"10.0.128.0/18\"]}"
	nodeAnnotationMapWithoutEgressCidrAnnotation := make(map[string]string, 0)
	nodeAnnotationMapWithoutEgressCidrAnnotation[ovnAnnotationNodeIfAddr] = "{\"ipv4\":\"10.0.128.17/17\"}"

	nodeLabelMap := make(map[string]string)
	nodeLabelMap[egressAssignable] = ""

	testCases := []struct {
		name                    string
		objects                 []crclient.Object
		expectedEgressIpsList   []string
		expectedEgressCIDRsList []string
	}{
		{
			name: "[reverse migration] egressIP has one IP listed",
			objects: []crclient.Object{
				&v1.HostSubnet{
					ObjectMeta: metav1.ObjectMeta{
						Name: testMigrationHost,
					},
					Host: testMigrationHost,
				},
				&uns.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "network.openshift.io/v1",
						"kind":       "NetNamespace",
						"netname":    testMigrationNamespace,
						"metadata": map[string]interface{}{
							"name": testMigrationNamespace,
						},
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name:        testMigrationHost,
						Annotations: nodeAnnotationMap,
						Labels:      nodeLabelMap,
					},
				},
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: testMigrationNamespace,
					},
				},
				&uns.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "k8s.ovn.org/v1",
						"kind":       "EgressIP",
						"metadata": map[string]interface{}{
							"name": "egress-group1",
						},
						"spec": map[string]interface{}{
							"egressIPs": egressIpsIfcArrSingle,
							"namespaceSelector": map[string]interface{}{
								"matchLabels": map[string]interface{}{
									"kubernetes.io/metadata.name": testMigrationNamespace,
								},
							},
						},
					},
				},
			},
			expectedEgressIpsList:   egressIpsStrArrSingle,
			expectedEgressCIDRsList: egressCIDRsStrArr,
		},
		{
			name: "[reverse migration] egressIP has multiple IPs listed",
			objects: []crclient.Object{
				&v1.HostSubnet{
					ObjectMeta: metav1.ObjectMeta{
						Name: testMigrationHost,
					},
					Host: testMigrationHost,
				},
				&uns.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "network.openshift.io/v1",
						"kind":       "NetNamespace",
						"netname":    testMigrationNamespace,
						"metadata": map[string]interface{}{
							"name": testMigrationNamespace,
						},
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name:        testMigrationHost,
						Annotations: nodeAnnotationMap,
						Labels:      nodeLabelMap,
					},
				},
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: testMigrationNamespace,
					},
				},
				&uns.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "k8s.ovn.org/v1",
						"kind":       "EgressIP",
						"metadata": map[string]interface{}{
							"name": "egress-group1",
						},
						"spec": map[string]interface{}{
							"egressIPs": egressIpsIfcArrMult,
							"namespaceSelector": map[string]interface{}{
								"matchLabels": map[string]interface{}{
									"kubernetes.io/metadata.name": testMigrationNamespace,
								},
							},
						},
					},
				},
			},
			expectedEgressIpsList:   egressIpsStrArrMult,
			expectedEgressCIDRsList: egressCIDRsStrArr,
		},
		{
			name: "[reverse migration] egressIP has one IP listed, with 2nd node with egressIP allocable on a non-cloud platform",
			objects: []crclient.Object{
				&v1.HostSubnet{
					ObjectMeta: metav1.ObjectMeta{
						Name: testMigrationHost,
					},
					Host: testMigrationHost,
				},
				&v1.HostSubnet{
					ObjectMeta: metav1.ObjectMeta{
						Name: "no-egressip-node",
					},
					Host: "no-egressip-node",
				},
				&uns.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "network.openshift.io/v1",
						"kind":       "NetNamespace",
						"netname":    testMigrationNamespace,
						"metadata": map[string]interface{}{
							"name": testMigrationNamespace,
						},
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name:        testMigrationHost,
						Annotations: nodeAnnotationMapWithEgressCidrAnnotation,
						Labels:      nodeLabelMap,
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "no-egressip-node",
						Annotations: nodeAnnotationMapWithoutEgressCidrAnnotation,
						Labels:      nodeLabelMap,
					},
				},
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: testMigrationNamespace,
					},
				},
				&uns.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "k8s.ovn.org/v1",
						"kind":       "EgressIP",
						"metadata": map[string]interface{}{
							"name": "egress-group1",
						},
						"spec": map[string]interface{}{
							"egressIPs": egressIpsIfcArrSingle,
							"namespaceSelector": map[string]interface{}{
								"matchLabels": map[string]interface{}{
									"kubernetes.io/metadata.name": testMigrationNamespace,
								},
							},
						},
					},
				},
			},
			expectedEgressIpsList:   egressIpsStrArrSingle,
			expectedEgressCIDRsList: egressCIDRsStrArrWithEgressCidrAnnotation,
		},
		{
			name: "[rollback] egressIP has one IP listed and node has egressCIDR rollback annotation",
			objects: []crclient.Object{
				&v1.HostSubnet{
					ObjectMeta: metav1.ObjectMeta{
						Name: testMigrationHost,
					},
					Host: testMigrationHost,
				},
				&uns.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "network.openshift.io/v1",
						"kind":       "NetNamespace",
						"netname":    testMigrationNamespace,
						"metadata": map[string]interface{}{
							"name": testMigrationNamespace,
						},
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name:        testMigrationHost,
						Annotations: nodeAnnotationMapWithEgressCidrAnnotation,
						Labels:      nodeLabelMap,
					},
				},
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: testMigrationNamespace,
					},
				},
				&uns.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "k8s.ovn.org/v1",
						"kind":       "EgressIP",
						"metadata": map[string]interface{}{
							"name": "egress-group1",
						},
						"spec": map[string]interface{}{
							"egressIPs": egressIpsIfcArrSingle,
							"namespaceSelector": map[string]interface{}{
								"matchLabels": map[string]interface{}{
									"kubernetes.io/metadata.name": testMigrationNamespace,
								},
							},
						},
					},
				},
			},
			expectedEgressIpsList:   egressIpsStrArrSingle,
			expectedEgressCIDRsList: egressCIDRsStrArrWithEgressCidrAnnotation,
		},
		{
			name: "[rollback] egressIP has multiple IPs listed and node has egressCIDR rollback annotation",
			objects: []crclient.Object{
				&v1.HostSubnet{
					ObjectMeta: metav1.ObjectMeta{
						Name: testMigrationHost,
					},
					Host: testMigrationHost,
				},
				&uns.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "network.openshift.io/v1",
						"kind":       "NetNamespace",
						"netname":    testMigrationNamespace,
						"metadata": map[string]interface{}{
							"name": testMigrationNamespace,
						},
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name:        testMigrationHost,
						Annotations: nodeAnnotationMapWithEgressCidrAnnotation,
						Labels:      nodeLabelMap,
					},
				},
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: testMigrationNamespace,
					},
				},
				&uns.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "k8s.ovn.org/v1",
						"kind":       "EgressIP",
						"metadata": map[string]interface{}{
							"name": "egress-group1",
						},
						"spec": map[string]interface{}{
							"egressIPs": egressIpsIfcArrMult,
							"namespaceSelector": map[string]interface{}{
								"matchLabels": map[string]interface{}{
									"kubernetes.io/metadata.name": testMigrationNamespace,
								},
							},
						},
					},
				},
			},
			expectedEgressIpsList:   egressIpsStrArrMult,
			expectedEgressCIDRsList: egressCIDRsStrArrWithEgressCidrAnnotation,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			client := fake.NewFakeClient(tc.objects...)

			err := convertOvnEgressIpToSdnEgressIp(context.Background(), client)
			if err != nil {
				t.Fatalf("convertOvnEgressIpToSdnEgressIp: %v", err)
			}

			// Get the Hostsubnet and Netnamespace that should be updated
			hsn, err := client.Default().Dynamic().Resource(gvrHostSubnet).Get(context.Background(), testMigrationHost, metav1.GetOptions{})
			if err != nil {
				t.Fatalf("failed to get hostsubnet: %v", err)
			}
			nns, err := client.Default().Dynamic().Resource(gvrNetnamespace).Get(context.Background(), testMigrationNamespace, metav1.GetOptions{})
			if err != nil {
				t.Fatalf("failed to get netnamespace: %v", err)
			}

			// Check if Hostsubnets has correct egressCIDRs field
			hsnEgressCIDRs, found, err := uns.NestedStringSlice(hsn.Object, "egressCIDRs")
			if !found || err != nil {
				t.Fatalf("failed to find egressCIDRs for hostsubnet %s, probable cause: %v", hsn.GetName(), err)
			}
			expectedEgressCIDRsList := ConvertToUnstructuredInterface(tc.expectedEgressCIDRsList)
			g.Expect(hsnEgressCIDRs).To(ConsistOf(expectedEgressCIDRsList...), "expected applied SDN hostsubnet to have egressCIDRs matching input OVN config")

			// Check if Netnamespaces has correct egressIPs field
			nnsEgressIPs, found, err := uns.NestedStringSlice(nns.Object, "egressIPs")
			if !found || err != nil {
				t.Fatalf("failed to find egressIPs for netnamespace %s, probable cause: %v", nns.GetName(), err)
			}
			expectedEgressIpsList := ConvertToUnstructuredInterface(tc.expectedEgressIpsList)
			g.Expect(nnsEgressIPs).To(ConsistOf(expectedEgressIpsList...), "expected applied SDN netnamespace to have egressIPs matching input OVN config")
		})
	}
}

func TestMulticastMigration(t *testing.T) {

	testCases := []struct {
		name    string
		objects []crclient.Object
	}{
		{
			name: "Multicast annotation present on netnamespace object",
			objects: []crclient.Object{
				&uns.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "network.openshift.io/v1",
						"kind":       "NetNamespace",
						"netname":    testMigrationNamespace,
						"metadata": map[string]interface{}{
							"name": testMigrationNamespace,
							"annotations": map[string]interface{}{
								multicastEnabledSDN: "true",
							},
						},
					},
				},
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: testMigrationNamespace,
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			client := fake.NewFakeClient(tc.objects...)

			err := enableMulticastOVN(context.Background(), client)
			if err != nil {
				t.Fatalf("enableMulticastOVN: %v", err)
			}

			ns, err := client.Default().Kubernetes().CoreV1().Namespaces().Get(context.Background(), testMigrationNamespace, metav1.GetOptions{})
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if _, ok := ns.Annotations[multicastEnabledOVN]; !ok {
				t.Errorf("expect namespace to be marked with multicast-enabled annotation")
			}
			if ns.Annotations[multicastEnabledOVN] != "true" {
				t.Errorf("expected multicast-enabled annotation to be set to \"true\"")
			}
		})
	}
}

func TestMulticastMigrationRollback(t *testing.T) {
	namespaceAnnotation := map[string]string{
		multicastEnabledOVN: "true",
	}

	testCases := []struct {
		name    string
		objects []crclient.Object
	}{
		{
			name: "Multicast annotation present on netnamespace object",
			objects: []crclient.Object{
				&uns.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "network.openshift.io/v1",
						"kind":       "NetNamespace",
						"netname":    testMigrationNamespace,
						"metadata": map[string]interface{}{
							"name": testMigrationNamespace,
						},
					},
				},
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name:        testMigrationNamespace,
						Annotations: namespaceAnnotation,
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			client := fake.NewFakeClient(tc.objects...)

			err := enableMulticastSDN(context.Background(), client)
			if err != nil {
				t.Fatalf("enableMulticastOVN: %v", err)
			}

			nns, err := client.Default().Dynamic().Resource(gvrNetnamespace).Get(context.Background(), testMigrationNamespace, metav1.GetOptions{})
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			meme := nns.GetAnnotations()
			fmt.Println(meme)
			if _, ok := nns.GetAnnotations()[multicastEnabledSDN]; !ok {
				t.Errorf("expect netnamespace to be marked with multicast-enabled annotation")
			}
			if nns.GetAnnotations()[multicastEnabledSDN] != "true" {
				t.Errorf("expected multicast-enabled annotation to be set to \"true\"")
			}
		})
	}
}

func TestEnsureMachineConfigPools(t *testing.T) {
	type expectedResult struct {
		isDesired, isStable bool
		err                 error
	}
	testCases := []struct {
		name    string
		isMatch func([]byte, string) bool
		result  expectedResult
		objects []crclient.Object
	}{
		{
			name:    "All machineconfigpools are stable and in desired state",
			isMatch: func([]byte, string) bool { return true },
			result:  expectedResult{isDesired: true, isStable: true, err: nil},
			objects: []crclient.Object{
				newMCP("master", corev1.ConditionFalse, corev1.ConditionFalse),
				newMCP("worker", corev1.ConditionFalse, corev1.ConditionFalse),
				&mcfgv1.MachineConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: "master" + fakeMcName,
					},
				},
				&mcfgv1.MachineConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: "worker" + fakeMcName,
					},
				},
			},
		},
		{
			name:    "All machineconfigpools are stable but one is not in desired state",
			isMatch: func([]byte, string) bool { return false },
			result:  expectedResult{isDesired: false, isStable: true, err: nil},
			objects: []crclient.Object{
				newMCP("master", corev1.ConditionFalse, corev1.ConditionFalse),
				newMCP("worker", corev1.ConditionFalse, corev1.ConditionFalse),
				&mcfgv1.MachineConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: "master" + fakeMcName,
					},
				},
				&mcfgv1.MachineConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: "worker" + fakeMcName,
					},
				},
			},
		},
		{
			name:    "One machineconfigpool is updating",
			isMatch: func([]byte, string) bool { return false },
			result:  expectedResult{isDesired: false, isStable: false, err: nil},
			objects: []crclient.Object{
				newMCP("master", corev1.ConditionFalse, corev1.ConditionFalse),
				newMCP("worker", corev1.ConditionTrue, corev1.ConditionFalse),
				&mcfgv1.MachineConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: "master" + fakeMcName,
					},
				},
				&mcfgv1.MachineConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: "worker" + fakeMcName,
					},
				},
			},
		},
		{
			name:    "One machineconfigpool is degraded",
			isMatch: func([]byte, string) bool { return false },
			result:  expectedResult{isDesired: false, isStable: false, err: nil},
			objects: []crclient.Object{
				newMCP("master", corev1.ConditionFalse, corev1.ConditionTrue),
				newMCP("worker", corev1.ConditionFalse, corev1.ConditionFalse),
				&mcfgv1.MachineConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: "master" + fakeMcName,
					},
				},
				&mcfgv1.MachineConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: "worker" + fakeMcName,
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			g := NewGomegaWithT(t)

			r := &ReconcileOperConfig{
				client: fake.NewFakeClient(tc.objects...),
			}
			clusterConfig := &configv1.Network{}
			condType := "sampleConditionType"
			nowTimestamp := metav1.Now()
			// call the ensureMachineConfigPools method
			isDesired, isStable, err := r.ensureMachineConfigPools(context.Background(), clusterConfig, condType, tc.isMatch, nowTimestamp)

			g.Expect(err).To(BeNil())
			g.Expect(isDesired).To(Equal(tc.result.isDesired))
			g.Expect(isStable).To(Equal(tc.result.isStable))
		})
	}

}

func newMCP(name string, updating, degraded corev1.ConditionStatus) *mcfgv1.MachineConfigPool {
	return &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Status: mcfgv1.MachineConfigPoolStatus{
			Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{
				ObjectReference: corev1.ObjectReference{
					Name: name + fakeMcName,
				},
			},
			Conditions: []mcfgv1.MachineConfigPoolCondition{
				{
					Type:   mcfgv1.MachineConfigPoolUpdating,
					Status: updating,
				},
				{
					Type:   mcfgv1.MachineConfigPoolDegraded,
					Status: degraded,
				},
			},
		},
	}
}

func ConvertToUnstructuredInterface(input []string) []interface{} {
	output := make([]interface{}, len(input))
	for i, v := range input {
		output[i] = v
	}
	return output
}
