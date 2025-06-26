package ingressconfig

import (
	"fmt"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/openshift/cluster-network-operator/pkg/client/fake"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// mockRESTMapper is a fake RESTMapper for testing
type mockRESTMapper struct {
	shouldReturnError bool
	errorToReturn     error
}

func (m *mockRESTMapper) KindFor(resource schema.GroupVersionResource) (schema.GroupVersionKind, error) {
	return schema.GroupVersionKind{}, nil
}

func (m *mockRESTMapper) KindsFor(resource schema.GroupVersionResource) ([]schema.GroupVersionKind, error) {
	return nil, nil
}

func (m *mockRESTMapper) ResourceFor(input schema.GroupVersionResource) (schema.GroupVersionResource, error) {
	return schema.GroupVersionResource{}, nil
}

func (m *mockRESTMapper) ResourcesFor(input schema.GroupVersionResource) ([]schema.GroupVersionResource, error) {
	return nil, nil
}

func (m *mockRESTMapper) RESTMapping(gk schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	if m.shouldReturnError {
		return nil, m.errorToReturn
	}
	// Return a successful mapping indicating CRD exists
	return &meta.RESTMapping{
		Resource: schema.GroupVersionResource{
			Group:    "operator.openshift.io",
			Version:  "v1",
			Resource: "ingresscontrollers",
		},
	}, nil
}

func (m *mockRESTMapper) RESTMappings(gk schema.GroupKind, versions ...string) ([]*meta.RESTMapping, error) {
	return nil, nil
}

func (m *mockRESTMapper) ResourceSingularizer(resource string) (singular string, err error) {
	return "", nil
}

// mockClient wraps the fake client with a custom RESTMapper
type mockClient struct {
	crclient.Client
	restMapper meta.RESTMapper
}

func (m *mockClient) RESTMapper() meta.RESTMapper {
	return m.restMapper
}

func TestIsIngressCapabilityEnabled(t *testing.T) {
	testCases := []struct {
		name            string
		crdExists       bool
		restMapperError error
		expectedResult  bool
		expectedError   bool
	}{
		{
			name:           "Non-hypershift cluster should always return true (CRD doesn't matter)",
			crdExists:      false, // doesn't matter for non-hypershift
			expectedResult: true,
			expectedError:  false,
		},
		{
			name:           "Non-hypershift cluster should always return true (even with CRD)",
			crdExists:      true, // doesn't matter for non-hypershift
			expectedResult: true,
			expectedError:  false,
		},
	}

	// Note: We're not testing HyperShift scenarios here because the hypershift detection
	// is based on package-level variables that are set at initialization time.
	// In a real hypershift environment, the HYPERSHIFT env var would be set before
	// the process starts, so the package would initialize correctly.

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			g := NewGomegaWithT(t)

			// Create mock REST mapper
			restMapper := &mockRESTMapper{
				shouldReturnError: !tc.crdExists,
				errorToReturn:     tc.restMapperError,
			}

			// Create mock client with the REST mapper
			fakeClient := fake.NewFakeClient()
			mockClient := &mockClient{
				Client:     fakeClient.Default().CRClient(),
				restMapper: restMapper,
			}

			result, err := isIngressCapabilityEnabled(mockClient)

			if tc.expectedError {
				g.Expect(err).To(HaveOccurred())
			} else {
				g.Expect(err).NotTo(HaveOccurred())
			}
			g.Expect(result).To(Equal(tc.expectedResult))
		})
	}
}

func TestIsIngressCapabilityEnabledEdgeCases(t *testing.T) {
	g := NewGomegaWithT(t)

	// Test with nil client - should handle gracefully
	result, err := isIngressCapabilityEnabled(nil)
	g.Expect(err).To(HaveOccurred())
	g.Expect(result).To(BeFalse())

	// Test REST mapper error handling (in non-hypershift environment)
	// This simulates what would happen if there were issues with the REST mapper
	restMapper := &mockRESTMapper{
		shouldReturnError: true,
		errorToReturn:     fmt.Errorf("some unexpected error"),
	}

	fakeClient := fake.NewFakeClient()
	mockClient := &mockClient{
		Client:     fakeClient.Default().CRClient(),
		restMapper: restMapper,
	}

	// In non-hypershift mode, this should still return true regardless of REST mapper errors
	result, err = isIngressCapabilityEnabled(mockClient)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(BeTrue()) // Non-hypershift always returns true
}

// TestIsIngressCapabilityEnabledIntegrationNote documents the limitation of unit testing
// hypershift scenarios and explains how this would work in practice
func TestIsIngressCapabilityEnabledIntegrationNote(t *testing.T) {
	t.Log("=== Integration Testing Note ===")
	t.Log("The hypershift detection logic cannot be fully unit tested because:")
	t.Log("1. Hypershift detection relies on package-level variables set at initialization")
	t.Log("2. The HYPERSHIFT env var is read once when the package is imported")
	t.Log("3. Setting env vars in tests doesn't affect already-initialized package variables")
	t.Log("")
	t.Log("In a real hypershift environment:")
	t.Log("- HYPERSHIFT=true is set before the CNO process starts")
	t.Log("- The hypershift package initializes with Enabled=true")
	t.Log("- isIngressCapabilityEnabled() checks CRD existence via REST mapper")
	t.Log("- If IngressController CRD missing -> returns false -> controller not created")
	t.Log("- If IngressController CRD present -> returns true -> controller created with watch")
	t.Log("")
	t.Log("For integration testing, run the CNO with HYPERSHIFT=true environment variable")
}
