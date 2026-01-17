package controller

import (
	"crypto/tls"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/library-go/pkg/crypto"
)

// TLSConfigProvider provides TLS configuration for network connectivity checks
type TLSConfigProvider interface {
	// GetTLSConfig returns a tls.Config based on the observed APIServer TLS security profile
	GetTLSConfig(serverName string, clientCerts []tls.Certificate) *tls.Config
}

// tlsConfigProvider implements TLSConfigProvider
type tlsConfigProvider struct {
	configObserver *APIServerConfigObserverController
}

// NewTLSConfigProvider creates a new TLS config provider
func NewTLSConfigProvider(configObserver *APIServerConfigObserverController) TLSConfigProvider {
	return &tlsConfigProvider{
		configObserver: configObserver,
	}
}

// GetTLSConfig returns a tls.Config configured according to the APIServer's TLS security profile
func (p *tlsConfigProvider) GetTLSConfig(serverName string, clientCerts []tls.Certificate) *tls.Config {
	config := &tls.Config{
		ServerName:   serverName,
		Certificates: clientCerts,
		// InsecureSkipVerify is set to true because we're testing connectivity,
		// not validating certificates
		InsecureSkipVerify: true,
	}

	// Get the observed TLS security profile from the APIServer CR
	profile, err := p.configObserver.GetObservedTLSSecurityProfile()
	if err != nil {
		// If we can't get the profile, use defaults (no min version, no cipher restrictions)
		// This maintains backward compatibility
		return config
	}

	// Apply the TLS security profile settings
	if profile != nil {
		minTLSVersion, cipherSuites := getSecurityProfileCiphers(profile)

		// Set minimum TLS version
		config.MinVersion = convertTLSVersion(minTLSVersion)

		// Set cipher suites (already converted to IANA names)
		if len(cipherSuites) > 0 {
			config.CipherSuites = convertCipherSuites(cipherSuites)
		}
	}

	return config
}

// getSecurityProfileCiphers extracts the TLS version and cipher suites from the profile
func getSecurityProfileCiphers(profile *configv1.TLSSecurityProfile) (string, []string) {
	var profileType configv1.TLSProfileType
	if profile == nil {
		profileType = configv1.TLSProfileIntermediateType
	} else {
		profileType = profile.Type
	}

	var profileSpec *configv1.TLSProfileSpec
	if profileType == configv1.TLSProfileCustomType {
		if profile.Custom != nil {
			profileSpec = &profile.Custom.TLSProfileSpec
		}
	} else {
		profileSpec = configv1.TLSProfiles[profileType]
	}

	// Fallback to intermediate if no spec found
	if profileSpec == nil {
		profileSpec = configv1.TLSProfiles[configv1.TLSProfileIntermediateType]
	}

	// Convert OpenSSL cipher names to IANA names
	return string(profileSpec.MinTLSVersion), crypto.OpenSSLToIANACipherSuites(profileSpec.Ciphers)
}

// convertTLSVersion converts the OpenShift TLS version string to the Go tls package constant
func convertTLSVersion(version string) uint16 {
	switch configv1.TLSProtocolVersion(version) {
	case configv1.VersionTLS10:
		return tls.VersionTLS10
	case configv1.VersionTLS11:
		return tls.VersionTLS11
	case configv1.VersionTLS12:
		return tls.VersionTLS12
	case configv1.VersionTLS13:
		return tls.VersionTLS13
	default:
		// Default to TLS 1.2 for safety
		return tls.VersionTLS12
	}
}

// convertCipherSuites converts IANA cipher suite names to Go tls package constants
func convertCipherSuites(cipherNames []string) []uint16 {
	var cipherSuites []uint16

	// Map of IANA cipher names to Go constants
	cipherMap := map[string]uint16{
		"TLS_RSA_WITH_AES_128_CBC_SHA":                      tls.TLS_RSA_WITH_AES_128_CBC_SHA,
		"TLS_RSA_WITH_AES_256_CBC_SHA":                      tls.TLS_RSA_WITH_AES_256_CBC_SHA,
		"TLS_RSA_WITH_AES_128_GCM_SHA256":                   tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
		"TLS_RSA_WITH_AES_256_GCM_SHA384":                   tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
		"TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA":              tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
		"TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA":              tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
		"TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA":                tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
		"TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA":                tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
		"TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256":           tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
		"TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384":           tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
		"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256":             tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		"TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384":             tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		"TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256":       tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
		"TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256":     tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
		"TLS_AES_128_GCM_SHA256":                            tls.TLS_AES_128_GCM_SHA256,
		"TLS_AES_256_GCM_SHA384":                            tls.TLS_AES_256_GCM_SHA384,
		"TLS_CHACHA20_POLY1305_SHA256":                      tls.TLS_CHACHA20_POLY1305_SHA256,
	}

	for _, name := range cipherNames {
		if cipherID, ok := cipherMap[name]; ok {
			cipherSuites = append(cipherSuites, cipherID)
		}
	}

	return cipherSuites
}
