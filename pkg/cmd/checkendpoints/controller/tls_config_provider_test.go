package controller

import (
	"crypto/tls"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
)

// TestConvertTLSVersion verifies TLS version conversion
func TestConvertTLSVersion(t *testing.T) {
	tests := []struct {
		name     string
		version  string
		expected uint16
	}{
		{
			name:     "TLS 1.0",
			version:  string(configv1.VersionTLS10),
			expected: tls.VersionTLS10,
		},
		{
			name:     "TLS 1.1",
			version:  string(configv1.VersionTLS11),
			expected: tls.VersionTLS11,
		},
		{
			name:     "TLS 1.2",
			version:  string(configv1.VersionTLS12),
			expected: tls.VersionTLS12,
		},
		{
			name:     "TLS 1.3",
			version:  string(configv1.VersionTLS13),
			expected: tls.VersionTLS13,
		},
		{
			name:     "Unknown version defaults to TLS 1.2",
			version:  "UnknownVersion",
			expected: tls.VersionTLS12,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertTLSVersion(tt.version)
			if result != tt.expected {
				t.Errorf("convertTLSVersion(%s) = %v, want %v", tt.version, result, tt.expected)
			}
		})
	}
}

// TestConvertCipherSuites verifies cipher suite conversion
func TestConvertCipherSuites(t *testing.T) {
	tests := []struct {
		name     string
		ciphers  []string
		expected []uint16
	}{
		{
			name: "Common cipher suites",
			ciphers: []string{
				"TLS_AES_128_GCM_SHA256",
				"TLS_AES_256_GCM_SHA384",
				"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
			},
			expected: []uint16{
				tls.TLS_AES_128_GCM_SHA256,
				tls.TLS_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			},
		},
		{
			name:     "Empty cipher list",
			ciphers:  []string{},
			expected: []uint16{},
		},
		{
			name: "Unknown ciphers are skipped",
			ciphers: []string{
				"TLS_AES_128_GCM_SHA256",
				"UNKNOWN_CIPHER",
				"TLS_AES_256_GCM_SHA384",
			},
			expected: []uint16{
				tls.TLS_AES_128_GCM_SHA256,
				tls.TLS_AES_256_GCM_SHA384,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertCipherSuites(tt.ciphers)
			if len(result) != len(tt.expected) {
				t.Errorf("convertCipherSuites() returned %d ciphers, want %d", len(result), len(tt.expected))
				return
			}
			for i, cipher := range result {
				if cipher != tt.expected[i] {
					t.Errorf("convertCipherSuites()[%d] = %v, want %v", i, cipher, tt.expected[i])
				}
			}
		})
	}
}

// TestGetSecurityProfileCiphers verifies profile resolution
func TestGetSecurityProfileCiphers(t *testing.T) {
	tests := []struct {
		name               string
		profile            *configv1.TLSSecurityProfile
		expectedMinVersion string
		expectedCipherLen  int
	}{
		{
			name:               "Nil profile defaults to Intermediate",
			profile:            nil,
			expectedMinVersion: string(configv1.VersionTLS12),
			expectedCipherLen:  9, // After OpenSSL to IANA conversion, some ciphers may not be available in Go
		},
		{
			name: "Old profile",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileOldType,
			},
			expectedMinVersion: string(configv1.VersionTLS10),
			expectedCipherLen:  21, // After OpenSSL to IANA conversion
		},
		{
			name: "Intermediate profile",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileIntermediateType,
			},
			expectedMinVersion: string(configv1.VersionTLS12),
			expectedCipherLen:  9,
		},
		{
			name: "Modern profile",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileModernType,
			},
			expectedMinVersion: string(configv1.VersionTLS13),
			expectedCipherLen:  3, // Modern profile has 3 ciphers
		},
		{
			name: "Custom profile",
			profile: &configv1.TLSSecurityProfile{
				Type: configv1.TLSProfileCustomType,
				Custom: &configv1.CustomTLSProfile{
					TLSProfileSpec: configv1.TLSProfileSpec{
						Ciphers: []string{
							"ECDHE-ECDSA-CHACHA20-POLY1305",
							"ECDHE-RSA-CHACHA20-POLY1305",
						},
						MinTLSVersion: configv1.VersionTLS12,
					},
				},
			},
			expectedMinVersion: string(configv1.VersionTLS12),
			expectedCipherLen:  2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			minVersion, ciphers := getSecurityProfileCiphers(tt.profile)
			if minVersion != tt.expectedMinVersion {
				t.Errorf("getSecurityProfileCiphers() minVersion = %s, want %s", minVersion, tt.expectedMinVersion)
			}
			if len(ciphers) != tt.expectedCipherLen {
				t.Errorf("getSecurityProfileCiphers() returned %d ciphers, want %d", len(ciphers), tt.expectedCipherLen)
			}
		})
	}
}
