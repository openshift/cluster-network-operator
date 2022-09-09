package operconfig

import (
	"context"
	"fmt"
	"testing"

	// . "github.com/onsi/gomega"
	v1 "github.com/openshift/api/network/v1"
	"github.com/openshift/cluster-network-operator/pkg/client/fake"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const testMigrationNamespace = "openshift-multus"

func init() {
	utilruntime.Must(v1.AddToScheme(scheme.Scheme))
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
			// g := gomega.NewWithT(t)
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
			// g := gomega.NewWithT(t)
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
