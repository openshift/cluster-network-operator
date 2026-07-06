package network

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
)

func TestAddTLSInfoToRenderData(t *testing.T) {
	t.Run("when adherence policy is StrictAllComponents", func(t *testing.T) {
		testCases := []struct {
			name             string
			respectAdherence bool
		}{
			{"and respecting adherence", true},
			{"and not respecting adherence", false},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				data := make(map[string]interface{})
				bootstrapResult := &bootstrap.BootstrapResult{
					TLSProfile: bootstrap.TLSProfile{
						Spec: configv1.TLSProfileSpec{
							MinTLSVersion: configv1.VersionTLS12,
							Ciphers:       []string{"TLS_AES_128_GCM_SHA256", "TLS_AES_256_GCM_SHA384"},
						},
						Adherence: configv1.TLSAdherencePolicyStrictAllComponents,
					},
				}

				addTLSInfoToRenderData(data, bootstrapResult, tc.respectAdherence)

				// Should always use TLS profile when adherence is StrictAllComponents
				if v, ok := data[UseTLSProfileKey]; !ok || v != true {
					t.Errorf("Expected %s to be true, got %v", UseTLSProfileKey, v)
				}
				if v, ok := data[TLSMinVersionKey]; !ok || v != configv1.VersionTLS12 {
					t.Errorf("Expected %s to be %v, got %v", TLSMinVersionKey, configv1.VersionTLS12, v)
				}
				expectedCiphers := "TLS_AES_128_GCM_SHA256,TLS_AES_256_GCM_SHA384"
				if v, ok := data[TLSCipherSuitesKey]; !ok || v != expectedCiphers {
					t.Errorf("Expected %s to be %v, got %v", TLSCipherSuitesKey, expectedCiphers, v)
				}
			})
		}
	})

	t.Run("adherence policy is", func(t *testing.T) {
		adherencePolicies := []struct {
			name      string
			adherence configv1.TLSAdherencePolicy
		}{
			{"LegacyAdheringComponentsOnly", configv1.TLSAdherencePolicyLegacyAdheringComponentsOnly},
			{"NoOpinion (empty)", configv1.TLSAdherencePolicyNoOpinion},
		}

		for _, policy := range adherencePolicies {
			t.Run(policy.name, func(t *testing.T) {
				t.Run("and respecting adherence", func(t *testing.T) {
					data := make(map[string]interface{})
					bootstrapResult := &bootstrap.BootstrapResult{
						TLSProfile: bootstrap.TLSProfile{
							Spec: configv1.TLSProfileSpec{
								MinTLSVersion: configv1.VersionTLS13,
								Ciphers:       []string{"TLS_AES_128_GCM_SHA256"},
							},
							Adherence: policy.adherence,
						},
					}

					addTLSInfoToRenderData(data, bootstrapResult, true)

					// Should NOT use TLS profile when respecting adherence for these policies
					if v, ok := data[UseTLSProfileKey]; !ok || v != false {
						t.Errorf("Expected %s to be false, got %v", UseTLSProfileKey, v)
					}
					if _, ok := data[TLSMinVersionKey]; ok {
						t.Errorf("Expected %s to not be present, but it was: %v", TLSMinVersionKey, data[TLSMinVersionKey])
					}
					if _, ok := data[TLSCipherSuitesKey]; ok {
						t.Errorf("Expected %s to not be present, but it was: %v", TLSCipherSuitesKey, data[TLSCipherSuitesKey])
					}
				})

				t.Run("and not respecting adherence", func(t *testing.T) {
					data := make(map[string]interface{})
					bootstrapResult := &bootstrap.BootstrapResult{
						TLSProfile: bootstrap.TLSProfile{
							Spec: configv1.TLSProfileSpec{
								MinTLSVersion: configv1.VersionTLS13,
								Ciphers:       []string{"TLS_AES_128_GCM_SHA256"},
							},
							Adherence: policy.adherence,
						},
					}

					addTLSInfoToRenderData(data, bootstrapResult, false)

					// Should use TLS profile when not respecting adherence
					if v, ok := data[UseTLSProfileKey]; !ok || v != true {
						t.Errorf("Expected %s to be true, got %v", UseTLSProfileKey, v)
					}
					if v, ok := data[TLSMinVersionKey]; !ok || v != configv1.VersionTLS13 {
						t.Errorf("Expected %s to be %v, got %v", TLSMinVersionKey, configv1.VersionTLS13, v)
					}
					expectedCiphers := "TLS_AES_128_GCM_SHA256"
					if v, ok := data[TLSCipherSuitesKey]; !ok || v != expectedCiphers {
						t.Errorf("Expected %s to be %v, got %v", TLSCipherSuitesKey, expectedCiphers, v)
					}
				})
			})
		}
	})

	t.Run("with nil cipher list", func(t *testing.T) {
		data := make(map[string]interface{})
		bootstrapResult := &bootstrap.BootstrapResult{
			TLSProfile: bootstrap.TLSProfile{
				Spec: configv1.TLSProfileSpec{
					MinTLSVersion: configv1.VersionTLS12,
					// Ciphers is nil
				},
			},
		}

		addTLSInfoToRenderData(data, bootstrapResult, false)

		if v, ok := data[UseTLSProfileKey]; !ok || v != true {
			t.Errorf("Expected %s to be true, got %v", UseTLSProfileKey, v)
		}
		if v, ok := data[TLSMinVersionKey]; !ok || v != configv1.VersionTLS12 {
			t.Errorf("Expected %s to be %v, got %v", TLSMinVersionKey, configv1.VersionTLS12, v)
		}
		if v, ok := data[TLSCipherSuitesKey]; !ok || v != "" {
			t.Errorf("Expected %s to be empty string, got %v", TLSCipherSuitesKey, v)
		}
	})
}
