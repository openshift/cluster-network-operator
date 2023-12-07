package operconfig

import (
	"context"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"github.com/openshift/cluster-network-operator/pkg/hypershift"
	"github.com/openshift/cluster-network-operator/pkg/util"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestProbeMTU(t *testing.T) {
	testCases := []struct {
		name    string
		infra   *bootstrap.InfraStatus
		objects []crclient.Object

		expectedMTU int
	}{
		{
			name: "AWS on Hypershift, hardcoded value is used",
			infra: &bootstrap.InfraStatus{
				PlatformType:         configv1.AWSPlatformType,
				ControlPlaneTopology: configv1.ExternalTopologyMode,
				HostedControlPlane:   &hypershift.HostedControlPlane{},
			},
			expectedMTU: 9001,
		},
		{
			name:  "AWS selfhosted, value from configmap is used",
			infra: &bootstrap.InfraStatus{PlatformType: configv1.AWSPlatformType},
			objects: []crclient.Object{&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: util.MTU_CM_NAMESPACE, Name: util.MTU_CM_NAME},
				Data:       map[string]string{"mtu": "5000"},
			}},
			expectedMTU: 5000,
		},
		{
			name: "Azure on hypershift, hardcoded value is used",
			infra: &bootstrap.InfraStatus{
				PlatformType:         configv1.AzurePlatformType,
				ControlPlaneTopology: configv1.ExternalTopologyMode,
				HostedControlPlane:   &hypershift.HostedControlPlane{},
			},
			expectedMTU: 1500,
		},
		{
			name:  "Azure selfhosted, value from configmap is used",
			infra: &bootstrap.InfraStatus{PlatformType: configv1.AzurePlatformType},
			objects: []crclient.Object{&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: util.MTU_CM_NAMESPACE, Name: util.MTU_CM_NAME},
				Data:       map[string]string{"mtu": "5000"},
			}},
			expectedMTU: 5000,
		},
		{
			name:  "Unknown platform on Hypershift, value from configmap is used",
			infra: &bootstrap.InfraStatus{ControlPlaneTopology: configv1.ExternalTopologyMode, HostedControlPlane: &hypershift.HostedControlPlane{}},
			objects: []crclient.Object{&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: util.MTU_CM_NAMESPACE, Name: util.MTU_CM_NAME},
				Data:       map[string]string{"mtu": "5000"},
			}},
			expectedMTU: 5000,
		},
		{
			name:  "Unknown platform, value from configmap is used",
			infra: &bootstrap.InfraStatus{},
			objects: []crclient.Object{&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: util.MTU_CM_NAMESPACE, Name: util.MTU_CM_NAME},
				Data:       map[string]string{"mtu": "5000"},
			}},
			expectedMTU: 5000,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			r := &ReconcileOperConfig{
				client: &fakeCNOClient{
					clusterClient: &fakeClusterClient{
						crclient: fake.NewClientBuilder().WithObjects(tc.objects...).Build(),
					},
				},
			}
			actual, err := r.probeMTU(context.Background(), &operv1.Network{}, tc.infra)
			if err != nil {
				t.Fatalf("probeMTU: %v", err)
			}
			if actual != tc.expectedMTU {
				t.Errorf("expected mtu of %d, got %d", tc.expectedMTU, actual)
			}
		})
	}
}
