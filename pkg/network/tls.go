package network

import (
	"context"
	"fmt"
	"strings"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/hypershift"
	openshifttls "github.com/openshift/controller-runtime-common/pkg/tls"
	"github.com/openshift/library-go/pkg/crypto"
	"k8s.io/apimachinery/pkg/types"
)

const (
	// UseTLSProfileKey is the template data key indicating whether to use the cluster TLS profile
	UseTLSProfileKey = "UseTLSProfile"
	// TLSMinVersionKey is the template data key for the minimum TLS version
	TLSMinVersionKey = "TLSMinVersion"
	// TLSCipherSuitesKey is the template data key for the comma-separated cipher suites
	TLSCipherSuitesKey = "TLSCipherSuites"
	// NginxTLSProtocolsKey is the template data key for NGINX ssl_protocols directive
	NginxTLSProtocolsKey = "NginxTLSProtocols"
	// NginxTLSCiphersKey is the template data key for NGINX ssl_ciphers directive
	NginxTLSCiphersKey = "NginxTLSCiphers"
)

// addTLSInfoToRenderData adds TLS-related template data to the render data.
// It converts OpenSSL cipher names (from TLSProfile.Spec.Ciphers) to IANA format for Go components,
// and also adds NGINX-specific parameters using the original OpenSSL names.
func addTLSInfoToRenderData(data map[string]interface{}, bootstrapResult *bootstrap.BootstrapResult, respectAdherence bool) {
	if respectAdherence && !crypto.ShouldHonorClusterTLSProfile(bootstrapResult.TLSProfile.Adherence) {
		data[UseTLSProfileKey] = false
		return
	}

	// Convert OpenSSL cipher names to IANA names for Go components (kube-rbac-proxy, controller-runtime, etc.)
	var ianaCiphers []string
	for _, cipher := range bootstrapResult.TLSProfile.Spec.Ciphers {
		// First try as IANA name directly (in case of custom profiles with IANA names)
		if _, err := crypto.CipherSuite(cipher); err == nil {
			ianaCiphers = append(ianaCiphers, cipher)
			continue
		}

		// Try converting from OpenSSL name to IANA name
		converted := crypto.OpenSSLToIANACipherSuites([]string{cipher})
		if len(converted) > 0 {
			ianaCiphers = append(ianaCiphers, converted...)
		} else {
			ianaCiphers = append(ianaCiphers, cipher)
		}
	}

	// Add Go-style TLS parameters (IANA cipher names, comma-separated)
	data[TLSMinVersionKey] = bootstrapResult.TLSProfile.Spec.MinTLSVersion
	data[TLSCipherSuitesKey] = strings.Join(ianaCiphers, ",")

	// Add NGINX-style TLS parameters (OpenSSL cipher names, colon-separated)
	data[NginxTLSProtocolsKey] = convertTLSVersionToNginx(bootstrapResult.TLSProfile.Spec.MinTLSVersion)
	data[NginxTLSCiphersKey] = strings.Join(bootstrapResult.TLSProfile.Spec.Ciphers, ":")

	data[UseTLSProfileKey] = true
}

// GetTLSProfile fetches the TLS profile from either the APIServer (standalone) or HostedControlPlane (HyperShift)
func GetTLSProfile(client cnoclient.Client, hcp *hypershift.HostedControlPlane) (bootstrap.TLSProfile, error) {
	// For HyperShift, read TLS profile from the already-parsed HostedControlPlane
	if hcp != nil {
		if hcp.APIServerSpec == nil {
			// No APIServer spec, use defaults
			return toTLSProfile(&configv1.APIServerSpec{})
		}
		return toTLSProfile(hcp.APIServerSpec)
	}

	// For non-HyperShift, read from APIServer CR in the default cluster
	apiServer := &configv1.APIServer{}
	if err := client.Default().CRClient().Get(context.TODO(), types.NamespacedName{Name: openshifttls.APIServerName}, apiServer); err != nil {
		return bootstrap.TLSProfile{}, fmt.Errorf("failed to fetch apiserver.config.openshift.io/%s: %w", openshifttls.APIServerName, err)
	}

	return toTLSProfile(&apiServer.Spec)
}

func toTLSProfile(apiServerSpec *configv1.APIServerSpec) (bootstrap.TLSProfile, error) {
	profileSpec, err := openshifttls.GetTLSProfileSpec(apiServerSpec.TLSSecurityProfile)
	if err != nil {
		return bootstrap.TLSProfile{}, fmt.Errorf("failed to get TLS profile spec: %w", err)
	}

	// TLS 1.3 cipher suites are not configurable in Go - all TLS 1.3 ciphers are always enabled.
	// Clear the cipher list for TLS 1.3 as some components are strict and fail if ciphers are provided with min version 1.3.
	if profileSpec.MinTLSVersion == configv1.VersionTLS13 {
		profileSpec.Ciphers = nil
	}

	return bootstrap.TLSProfile{
		Spec:      profileSpec,
		Adherence: apiServerSpec.TLSAdherence,
	}, nil
}

// convertTLSVersionToNginx converts OpenShift TLS version to NGINX ssl_protocols format
func convertTLSVersionToNginx(version configv1.TLSProtocolVersion) string {
	switch version {
	case configv1.VersionTLS10:
		return "TLSv1 TLSv1.1 TLSv1.2 TLSv1.3"
	case configv1.VersionTLS11:
		return "TLSv1.1 TLSv1.2 TLSv1.3"
	case configv1.VersionTLS12:
		return "TLSv1.2 TLSv1.3"
	case configv1.VersionTLS13:
		return "TLSv1.3"
	default:
		// Default to TLS 1.2 and 1.3 for unknown versions
		return "TLSv1.2 TLSv1.3"
	}
}
