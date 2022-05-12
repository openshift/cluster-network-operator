package operconfig

import (
	"context"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
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
			name:        "AWS on Hypershift, hardcoded value is used",
			infra:       &bootstrap.InfraStatus{PlatformType: configv1.AWSPlatformType, ExternalControlPlane: true},
			expectedMTU: 9001,
		},
		{
			name:  "AWS selfhosted, value from configmap is used",
			infra: &bootstrap.InfraStatus{PlatformType: configv1.AWSPlatformType},
			objects: []crclient.Object{&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: cmNamespace, Name: cmName},
				Data:       map[string]string{"mtu": "5000"},
			}},
			expectedMTU: 5000,
		},

		{
			name:  "Not aws on Hypershift, value from configmap is used",
			infra: &bootstrap.InfraStatus{ExternalControlPlane: true},
			objects: []crclient.Object{&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: cmNamespace, Name: cmName},
				Data:       map[string]string{"mtu": "5000"},
			}},
			expectedMTU: 5000,
		},
		{
			name:  "Not aws, value from configmap is used",
			infra: &bootstrap.InfraStatus{},
			objects: []crclient.Object{&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: cmNamespace, Name: cmName},
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

type fakeCNOClient struct {
	cnoclient.Client
	clusterClient cnoclient.ClusterClient
}

func (f *fakeCNOClient) Default() cnoclient.ClusterClient {
	return f.clusterClient
}

type fakeClusterClient struct {
	cnoclient.ClusterClient
	crclient crclient.Client
}

func (f *fakeClusterClient) CRClient() crclient.Client {
	return f.crclient
}
