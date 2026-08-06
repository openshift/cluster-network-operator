package operconfig

import (
	"testing"

	operv1 "github.com/openshift/api/operator/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func TestSetControllerReferenceSkipsCRDs(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(operv1.Install(scheme))

	owner := &operv1.Network{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
			UID:  "test-uid",
		},
	}

	tests := []struct {
		name           string
		obj            *uns.Unstructured
		expectOwnerRef bool
	}{
		{
			name: "Deployment gets controller ownerReference",
			obj: &uns.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]interface{}{
						"name":      "test-deployment",
						"namespace": "openshift-network-operator",
					},
				},
			},
			expectOwnerRef: true,
		},
		{
			name: "CRD does not get controller ownerReference",
			obj: &uns.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "apiextensions.k8s.io/v1",
					"kind":       "CustomResourceDefinition",
					"metadata": map[string]interface{}{
						"name": "frrconfigurations.frrk8s.metallb.io",
					},
				},
			},
			expectOwnerRef: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !isCRD(tc.obj) {
				if err := controllerutil.SetControllerReference(owner, tc.obj, scheme); err != nil {
					t.Fatalf("SetControllerReference failed: %v", err)
				}
			}

			controllerRef := metav1.GetControllerOf(tc.obj)
			if tc.expectOwnerRef && controllerRef == nil {
				t.Error("expected controller ownerReference, got none")
			}
			if !tc.expectOwnerRef && controllerRef != nil {
				t.Errorf("expected no controller ownerReference, got %v", controllerRef)
			}
		})
	}
}

func TestIsCRD(t *testing.T) {
	tests := []struct {
		name       string
		apiVersion string
		kind       string
		expected   bool
	}{
		{
			name:       "apiextensions CRD",
			apiVersion: "apiextensions.k8s.io/v1",
			kind:       "CustomResourceDefinition",
			expected:   true,
		},
		{
			name:       "non-CRD resource with same Kind in different group",
			apiVersion: "example.com/v1",
			kind:       "CustomResourceDefinition",
			expected:   false,
		},
		{
			name:       "Deployment",
			apiVersion: "apps/v1",
			kind:       "Deployment",
			expected:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obj := &uns.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": tc.apiVersion,
					"kind":       tc.kind,
					"metadata":   map[string]interface{}{"name": "test"},
				},
			}
			if got := isCRD(obj); got != tc.expected {
				t.Errorf("isCRD() = %v, want %v", got, tc.expected)
			}
		})
	}
}
