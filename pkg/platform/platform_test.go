package platform

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/client/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
)

func TestTopologyModeDetection(t *testing.T) {
	testCases := []struct {
		name                       string
		infrastructure             *configv1.Infrastructure
		expectExternalControlplane bool
	}{
		{
			name: "External controlplane toplogy",
			infrastructure: &configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Status: configv1.InfrastructureStatus{
					PlatformStatus:       &configv1.PlatformStatus{},
					ControlPlaneTopology: configv1.ExternalTopologyMode,
				},
			},
			expectExternalControlplane: true,
		},
		{
			name: "Not expectExternalControlplane",
			infrastructure: &configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Status: configv1.InfrastructureStatus{
					PlatformStatus:       &configv1.PlatformStatus{},
					ControlPlaneTopology: configv1.HighlyAvailableTopologyMode,
				},
			},
			expectExternalControlplane: false,
		},
	}

	if err := configv1.AddToScheme(scheme.Scheme); err != nil {
		t.Fatalf("failed to add configv1 to scheme: %v", err)
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			client := fake.NewFakeClient(tc.infrastructure, &configv1.Proxy{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}})

			bootstrapResult, err := InfraStatus(client)
			if err != nil {
				t.Fatalf("BootstrapInfra failed: %v", err)
			}

			if bootstrapResult.ExternalControlPlane != tc.expectExternalControlplane {
				t.Errorf("expected externalControlPlane to be %t, was %t", tc.expectExternalControlplane, bootstrapResult.ExternalControlPlane)
			}
		})
	}
}
