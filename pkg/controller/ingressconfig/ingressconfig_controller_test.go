package ingressconfig

import (
	"testing"

	. "github.com/onsi/gomega"

	"github.com/openshift/cluster-network-operator/pkg/client/fake"
	"github.com/openshift/cluster-network-operator/pkg/hypershift"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestIsIngressCapabilityEnabled(t *testing.T) {
	testCases := []struct {
		name              string
		hypershiftEnabled bool
		crdExists         bool
		restMapperError   error
		expectedResult    bool
		expectedError     bool
	}{
		{
			name:              "Non-hypershift cluster should always return true (IngressController CRD doesn't exists)",
			hypershiftEnabled: false,
			crdExists:         false, // doesn't matter for non-hypershift
			expectedResult:    true,
			expectedError:     false,
		},
		{
			name:              "Non-hypershift cluster should always return true (even with IngressController CRD)",
			hypershiftEnabled: false,
			crdExists:         true, // doesn't matter for non-hypershift
			expectedResult:    true,
			expectedError:     false,
		},
		{
			name:              "Hypershift cluster with IngressController CRD should return true",
			hypershiftEnabled: true,
			crdExists:         true,
			expectedResult:    true,
			expectedError:     false,
		},
		{
			name:              "Hypershift cluster without IngressController CRD should return false",
			hypershiftEnabled: true,
			crdExists:         false,
			restMapperError: &meta.NoKindMatchError{
				GroupKind: schema.GroupKind{
					Group: "operator.openshift.io",
					Kind:  "IngressController",
				},
				SearchedVersions: []string{"v1"},
			},
			expectedResult: false,
			expectedError:  false,
		},
	}

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

			// Create hypershift config with the desired enabled state
			hcpCfg := &hypershift.HyperShiftConfig{
				Enabled: tc.hypershiftEnabled,
			}

			result, err := isIngressCapabilityEnabledWithConfig(mockClient, hcpCfg)

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
	hcpCfg := &hypershift.HyperShiftConfig{Enabled: false}
	result, err := isIngressCapabilityEnabledWithConfig(nil, hcpCfg)
	g.Expect(err).To(HaveOccurred())
	g.Expect(result).To(BeFalse())

	// Test REST mapper error handling (in non-hypershift environment)
	// This simulates what would happen if there were issues with the REST mapper
	restMapper := &mockRESTMapper{
		shouldReturnError: true,
		errorToReturn: &meta.NoKindMatchError{
			GroupKind: schema.GroupKind{
				Group: "operator.openshift.io",
				Kind:  "IngressController",
			},
			SearchedVersions: []string{"v1"},
		},
	}

	fakeClient := fake.NewFakeClient()
	mockClient := &mockClient{
		Client:     fakeClient.Default().CRClient(),
		restMapper: restMapper,
	}

	// In non-hypershift mode, this should still return true regardless of REST mapper errors
	result, err = isIngressCapabilityEnabledWithConfig(mockClient, hcpCfg)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(BeTrue()) // Non-hypershift always returns true
}
