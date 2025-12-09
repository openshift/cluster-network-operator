package network

import (
	"testing"

	. "github.com/onsi/gomega"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	cnofake "github.com/openshift/cluster-network-operator/pkg/client/fake"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestIsFRRWebhookReady(t *testing.T) {
	tests := []struct {
		name          string
		endpointSlice *discoveryv1.EndpointSlice
		expected      bool
	}{
		{
			name:          "no endpoint slice object",
			endpointSlice: nil,
			expected:      false,
		},
		{
			name: "endpoint slice with no endpoints",
			endpointSlice: &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      frrK8sWebhookService + "-abc",
					Namespace: frrK8sNamespace,
					Labels: map[string]string{
						"kubernetes.io/service-name": frrK8sWebhookService,
					},
				},
				Endpoints: []discoveryv1.Endpoint{},
			},
			expected: false,
		},
		{
			name: "endpoint slice with endpoint but not ready",
			endpointSlice: &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      frrK8sWebhookService + "-abc",
					Namespace: frrK8sNamespace,
					Labels: map[string]string{
						"kubernetes.io/service-name": frrK8sWebhookService,
					},
				},
				Endpoints: []discoveryv1.Endpoint{
					{
						Addresses: []string{"10.0.0.1"},
						Conditions: discoveryv1.EndpointConditions{
							Ready: ptr.To(false),
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "endpoint slice with endpoint but Ready is nil",
			endpointSlice: &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      frrK8sWebhookService + "-abc",
					Namespace: frrK8sNamespace,
					Labels: map[string]string{
						"kubernetes.io/service-name": frrK8sWebhookService,
					},
				},
				Endpoints: []discoveryv1.Endpoint{
					{
						Addresses:  []string{"10.0.0.1"},
						Conditions: discoveryv1.EndpointConditions{},
					},
				},
			},
			expected: false,
		},
		{
			name: "endpoint slice with ready endpoint",
			endpointSlice: &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      frrK8sWebhookService + "-abc",
					Namespace: frrK8sNamespace,
					Labels: map[string]string{
						"kubernetes.io/service-name": frrK8sWebhookService,
					},
				},
				Endpoints: []discoveryv1.Endpoint{
					{
						Addresses: []string{"10.0.0.1"},
						Conditions: discoveryv1.EndpointConditions{
							Ready: ptr.To(true),
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "endpoint slice with multiple ready endpoints",
			endpointSlice: &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      frrK8sWebhookService + "-abc",
					Namespace: frrK8sNamespace,
					Labels: map[string]string{
						"kubernetes.io/service-name": frrK8sWebhookService,
					},
				},
				Endpoints: []discoveryv1.Endpoint{
					{
						Addresses: []string{"10.0.0.1"},
						Conditions: discoveryv1.EndpointConditions{
							Ready: ptr.To(true),
						},
					},
					{
						Addresses: []string{"10.0.0.2"},
						Conditions: discoveryv1.EndpointConditions{
							Ready: ptr.To(true),
						},
					},
					{
						Addresses: []string{"10.0.0.3"},
						Conditions: discoveryv1.EndpointConditions{
							Ready: ptr.To(true),
						},
					},
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			var fakeClient cnoclient.Client
			if tt.endpointSlice != nil {
				fakeClient = cnofake.NewFakeClient(tt.endpointSlice)
			} else {
				fakeClient = cnofake.NewFakeClient()
			}

			result := isFRRWebhookReady(fakeClient)
			g.Expect(result).To(Equal(tt.expected))
		})
	}
}

func TestShouldSkipOVNKUntilFRRReady(t *testing.T) {
	tests := []struct {
		name            string
		conf            *operv1.NetworkSpec
		bootstrapResult *bootstrap.BootstrapResult
		endpointSlice   *discoveryv1.EndpointSlice
		expectedSkip    bool
	}{
		{
			name: "OVNK already running - should not skip",
			conf: &operv1.NetworkSpec{
				DefaultNetwork: operv1.DefaultNetworkDefinition{
					Type: operv1.NetworkTypeOVNKubernetes,
					OVNKubernetesConfig: &operv1.OVNKubernetesConfig{
						RouteAdvertisements: operv1.RouteAdvertisementsEnabled,
					},
				},
				AdditionalRoutingCapabilities: &operv1.AdditionalRoutingCapabilities{
					Providers: []operv1.RoutingCapabilitiesProvider{operv1.RoutingCapabilitiesProviderFRR},
				},
			},
			bootstrapResult: &bootstrap.BootstrapResult{
				OVN: bootstrap.OVNBootstrapResult{
					NodeUpdateStatus: &bootstrap.OVNUpdateStatus{
						Name: "ovnkube-node",
					},
				},
			},
			endpointSlice: nil,
			expectedSkip:  false,
		},
		{
			name: "No FRR provider - should not skip",
			conf: &operv1.NetworkSpec{
				DefaultNetwork: operv1.DefaultNetworkDefinition{
					Type: operv1.NetworkTypeOVNKubernetes,
					OVNKubernetesConfig: &operv1.OVNKubernetesConfig{
						RouteAdvertisements: operv1.RouteAdvertisementsEnabled,
					},
				},
				AdditionalRoutingCapabilities: nil,
			},
			bootstrapResult: &bootstrap.BootstrapResult{
				OVN: bootstrap.OVNBootstrapResult{
					NodeUpdateStatus: nil,
				},
			},
			endpointSlice: nil,
			expectedSkip:  false,
		},
		{
			name: "RouteAdvertisements not enabled - should not skip",
			conf: &operv1.NetworkSpec{
				DefaultNetwork: operv1.DefaultNetworkDefinition{
					Type: operv1.NetworkTypeOVNKubernetes,
					OVNKubernetesConfig: &operv1.OVNKubernetesConfig{
						RouteAdvertisements: operv1.RouteAdvertisementsDisabled,
					},
				},
				AdditionalRoutingCapabilities: &operv1.AdditionalRoutingCapabilities{
					Providers: []operv1.RoutingCapabilitiesProvider{operv1.RoutingCapabilitiesProviderFRR},
				},
			},
			bootstrapResult: &bootstrap.BootstrapResult{
				OVN: bootstrap.OVNBootstrapResult{
					NodeUpdateStatus: nil,
				},
			},
			endpointSlice: nil,
			expectedSkip:  false,
		},
		{
			name: "No OVNKubernetesConfig - should not skip",
			conf: &operv1.NetworkSpec{
				DefaultNetwork: operv1.DefaultNetworkDefinition{
					Type:                operv1.NetworkTypeOVNKubernetes,
					OVNKubernetesConfig: nil,
				},
				AdditionalRoutingCapabilities: &operv1.AdditionalRoutingCapabilities{
					Providers: []operv1.RoutingCapabilitiesProvider{operv1.RoutingCapabilitiesProviderFRR},
				},
			},
			bootstrapResult: &bootstrap.BootstrapResult{
				OVN: bootstrap.OVNBootstrapResult{
					NodeUpdateStatus: nil,
				},
			},
			endpointSlice: nil,
			expectedSkip:  false,
		},
		{
			name: "All conditions met but FRR ready - should not skip",
			conf: &operv1.NetworkSpec{
				DefaultNetwork: operv1.DefaultNetworkDefinition{
					Type: operv1.NetworkTypeOVNKubernetes,
					OVNKubernetesConfig: &operv1.OVNKubernetesConfig{
						RouteAdvertisements: operv1.RouteAdvertisementsEnabled,
					},
				},
				AdditionalRoutingCapabilities: &operv1.AdditionalRoutingCapabilities{
					Providers: []operv1.RoutingCapabilitiesProvider{operv1.RoutingCapabilitiesProviderFRR},
				},
			},
			bootstrapResult: &bootstrap.BootstrapResult{
				OVN: bootstrap.OVNBootstrapResult{
					NodeUpdateStatus: nil,
				},
			},
			endpointSlice: &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      frrK8sWebhookService + "-abc",
					Namespace: frrK8sNamespace,
					Labels: map[string]string{
						"kubernetes.io/service-name": frrK8sWebhookService,
					},
				},
				Endpoints: []discoveryv1.Endpoint{
					{
						Addresses: []string{"10.0.0.1"},
						Conditions: discoveryv1.EndpointConditions{
							Ready: ptr.To(true),
						},
					},
				},
			},
			expectedSkip: false,
		},
		{
			name: "All conditions met and FRR not ready - should skip",
			conf: &operv1.NetworkSpec{
				DefaultNetwork: operv1.DefaultNetworkDefinition{
					Type: operv1.NetworkTypeOVNKubernetes,
					OVNKubernetesConfig: &operv1.OVNKubernetesConfig{
						RouteAdvertisements: operv1.RouteAdvertisementsEnabled,
					},
				},
				AdditionalRoutingCapabilities: &operv1.AdditionalRoutingCapabilities{
					Providers: []operv1.RoutingCapabilitiesProvider{operv1.RoutingCapabilitiesProviderFRR},
				},
			},
			bootstrapResult: &bootstrap.BootstrapResult{
				OVN: bootstrap.OVNBootstrapResult{
					NodeUpdateStatus: nil,
				},
			},
			endpointSlice: nil,
			expectedSkip:  true,
		},
		{
			name: "FRR not ready with empty endpoints - should skip",
			conf: &operv1.NetworkSpec{
				DefaultNetwork: operv1.DefaultNetworkDefinition{
					Type: operv1.NetworkTypeOVNKubernetes,
					OVNKubernetesConfig: &operv1.OVNKubernetesConfig{
						RouteAdvertisements: operv1.RouteAdvertisementsEnabled,
					},
				},
				AdditionalRoutingCapabilities: &operv1.AdditionalRoutingCapabilities{
					Providers: []operv1.RoutingCapabilitiesProvider{operv1.RoutingCapabilitiesProviderFRR},
				},
			},
			bootstrapResult: &bootstrap.BootstrapResult{
				OVN: bootstrap.OVNBootstrapResult{
					NodeUpdateStatus: nil,
				},
			},
			endpointSlice: &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      frrK8sWebhookService + "-abc",
					Namespace: frrK8sNamespace,
					Labels: map[string]string{
						"kubernetes.io/service-name": frrK8sWebhookService,
					},
				},
				Endpoints: []discoveryv1.Endpoint{},
			},
			expectedSkip: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			var fakeClient cnoclient.Client
			if tt.endpointSlice != nil {
				fakeClient = cnofake.NewFakeClient(tt.endpointSlice)
			} else {
				fakeClient = cnofake.NewFakeClient()
			}

			result := shouldSkipOVNKUntilFRRReady(tt.conf, tt.bootstrapResult, fakeClient)
			g.Expect(result).To(Equal(tt.expectedSkip))
		})
	}
}
