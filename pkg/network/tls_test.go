package network

import (
	"crypto/tls"
	"fmt"
	"strconv"
	"strings"
	"testing"

	. "github.com/onsi/gomega"
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
				g := NewWithT(t)
				data := make(map[string]any)
				bootstrapResult := &bootstrap.BootstrapResult{
					TLSProfile: bootstrap.TLSProfile{
						Spec: configv1.TLSProfileSpec{
							MinTLSVersion: configv1.VersionTLS12,
							Ciphers:       []string{"TLS_AES_128_GCM_SHA256", "TLS_AES_256_GCM_SHA384"},
							Groups:        []configv1.TLSGroup{configv1.TLSGroupSecP256r1, configv1.TLSGroupX25519},
						},
						Adherence: configv1.TLSAdherencePolicyStrictAllComponents,
					},
				}

				addTLSInfoToRenderData(data, bootstrapResult, tc.respectAdherence)

				// Should always use TLS profile when adherence is StrictAllComponents
				g.Expect(data).To(HaveKeyWithValue(UseTLSProfileKey, true))
				g.Expect(data).To(HaveKeyWithValue(TLSMinVersionKey, configv1.VersionTLS12))
				g.Expect(data).To(HaveKeyWithValue(TLSCipherSuitesKey, "TLS_AES_128_GCM_SHA256,TLS_AES_256_GCM_SHA384"))
				g.Expect(data).To(HaveKeyWithValue(TLSCurvePreferencesKey, fmt.Sprintf("%d,%d", tls.CurveP256, tls.X25519)))

				// Check NGINX parameters
				g.Expect(data).To(HaveKeyWithValue(NginxTLSProtocolsKey, "TLSv1.2 TLSv1.3"))
				g.Expect(data).To(HaveKeyWithValue(NginxTLSCiphersKey, "TLS_AES_128_GCM_SHA256:TLS_AES_256_GCM_SHA384"))
				g.Expect(data).To(HaveKeyWithValue(NginxTLSCurvesKey, fmt.Sprintf("%s:%s", configv1.TLSGroupSecP256r1,
					configv1.TLSGroupX25519)))
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
					g := NewWithT(t)
					data := make(map[string]any)
					bootstrapResult := &bootstrap.BootstrapResult{
						TLSProfile: bootstrap.TLSProfile{
							Spec: configv1.TLSProfileSpec{
								MinTLSVersion: configv1.VersionTLS13,
								Ciphers:       []string{"TLS_AES_128_GCM_SHA256"},
								Groups:        []configv1.TLSGroup{configv1.TLSGroupSecP256r1},
							},
							Adherence: policy.adherence,
						},
					}

					addTLSInfoToRenderData(data, bootstrapResult, true)

					// Should NOT use TLS profile when respecting adherence for these policies
					g.Expect(data).To(HaveKeyWithValue(UseTLSProfileKey, false))
					g.Expect(data).NotTo(HaveKey(TLSMinVersionKey))
					g.Expect(data).NotTo(HaveKey(TLSCipherSuitesKey))
					g.Expect(data).NotTo(HaveKey(TLSCurvePreferencesKey))

					// NGINX parameters should also not be set
					g.Expect(data).NotTo(HaveKey(NginxTLSProtocolsKey))
					g.Expect(data).NotTo(HaveKey(NginxTLSCiphersKey))
					g.Expect(data).NotTo(HaveKey(NginxTLSCurvesKey))
				})

				t.Run("and not respecting adherence", func(t *testing.T) {
					g := NewWithT(t)
					data := make(map[string]any)
					bootstrapResult := &bootstrap.BootstrapResult{
						TLSProfile: bootstrap.TLSProfile{
							Spec: configv1.TLSProfileSpec{
								MinTLSVersion: configv1.VersionTLS13,
								Ciphers:       []string{"TLS_AES_128_GCM_SHA256"},
								Groups:        []configv1.TLSGroup{configv1.TLSGroupSecP256r1},
							},
							Adherence: policy.adherence,
						},
					}

					addTLSInfoToRenderData(data, bootstrapResult, false)

					// Should use TLS profile when not respecting adherence
					g.Expect(data).To(HaveKeyWithValue(UseTLSProfileKey, true))
					g.Expect(data).To(HaveKeyWithValue(TLSMinVersionKey, configv1.VersionTLS13))
					g.Expect(data).To(HaveKeyWithValue(TLSCipherSuitesKey, "TLS_AES_128_GCM_SHA256"))
					g.Expect(data).To(HaveKeyWithValue(TLSCurvePreferencesKey, strconv.Itoa(int(tls.CurveP256))))

					// Check NGINX parameters
					g.Expect(data).To(HaveKeyWithValue(NginxTLSProtocolsKey, "TLSv1.3"))
					g.Expect(data).To(HaveKeyWithValue(NginxTLSCiphersKey, "TLS_AES_128_GCM_SHA256"))
					g.Expect(data).To(HaveKeyWithValue(NginxTLSCurvesKey, string(configv1.TLSGroupSecP256r1)))
				})
			})
		}
	})

	t.Run("with nil cipher and groups lists", func(t *testing.T) {
		g := NewWithT(t)
		data := make(map[string]any)
		bootstrapResult := &bootstrap.BootstrapResult{
			TLSProfile: bootstrap.TLSProfile{
				Spec: configv1.TLSProfileSpec{
					MinTLSVersion: configv1.VersionTLS12,
				},
			},
		}

		addTLSInfoToRenderData(data, bootstrapResult, false)
		g.Expect(data).To(HaveKeyWithValue(UseTLSProfileKey, true))
		g.Expect(data).To(HaveKeyWithValue(TLSMinVersionKey, configv1.VersionTLS12))
		g.Expect(data).To(HaveKeyWithValue(TLSCipherSuitesKey, BeEmpty()))
		g.Expect(data).To(HaveKeyWithValue(TLSCurvePreferencesKey, BeEmpty()))

		// Check NGINX parameters
		g.Expect(data).To(HaveKeyWithValue(NginxTLSProtocolsKey, "TLSv1.2 TLSv1.3"))
		g.Expect(data).To(HaveKeyWithValue(NginxTLSCiphersKey, BeEmpty()))
		g.Expect(data).To(HaveKeyWithValue(NginxTLSCurvesKey, BeEmpty()))
	})

	t.Run("should filter out ciphers unsupported by Go crypto/tls", func(t *testing.T) {
		g := NewWithT(t)
		data := make(map[string]any)
		bootstrapResult := &bootstrap.BootstrapResult{
			TLSProfile: bootstrap.TLSProfile{
				Spec: configv1.TLSProfileSpec{
					MinTLSVersion: configv1.VersionTLS12,
					Ciphers: []string{
						// Supported ciphers
						"ECDHE-ECDSA-AES128-GCM-SHA256",
						"ECDHE-RSA-AES128-GCM-SHA256",
						// Unsupported ciphers (Go doesn't implement CBC mode for these)
						"ECDHE-ECDSA-AES256-SHA384",
						"ECDHE-RSA-AES256-SHA384",
						"AES256-SHA256",
						// More supported ciphers
						"ECDHE-ECDSA-AES256-GCM-SHA384",
					},
				},
				Adherence: configv1.TLSAdherencePolicyStrictAllComponents,
			},
		}

		addTLSInfoToRenderData(data, bootstrapResult, true)

		// Should use TLS profile
		g.Expect(data).To(HaveKeyWithValue(UseTLSProfileKey, true))

		// Only supported ciphers should be in the IANA list (unsupported ones filtered out)
		g.Expect(data).To(HaveKeyWithValue(TLSCipherSuitesKey,
			"TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384"))

		// NGINX should keep all ciphers (original OpenSSL names)
		g.Expect(data).To(HaveKeyWithValue(NginxTLSCiphersKey, strings.Join(bootstrapResult.TLSProfile.Spec.Ciphers, ":")))
	})
}
