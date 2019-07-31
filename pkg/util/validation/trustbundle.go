package validation

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"

	"github.com/openshift/cluster-network-operator/pkg/names"

	corev1 "k8s.io/api/core/v1"
)

const (
	// certPEMBlock is the type taken from the preamble of a PEM-encoded structure.
	certPEMBlock = "CERTIFICATE"
)

// TrustBundleConfigMap validates that ConfigMap contains a
// trust bundle named "ca-bundle.crt" and that "ca-bundle.crt"
// contains one or more valid PEM encoded certificates, returning
// a byte slice of "ca-bundle.crt" contents upon success.
func TrustBundleConfigMap(cfgMap *corev1.ConfigMap) ([]*x509.Certificate, []byte, error) {
	if _, ok := cfgMap.Data[names.TRUST_BUNDLE_CONFIGMAP_KEY]; !ok {
		return nil, nil, fmt.Errorf("ConfigMap %q is missing %q", cfgMap.Name, names.TRUST_BUNDLE_CONFIGMAP_KEY)
	}
	trustBundleData := []byte(cfgMap.Data[names.TRUST_BUNDLE_CONFIGMAP_KEY])
	if len(trustBundleData) == 0 {
		return nil, nil, fmt.Errorf("data key %q is empty from ConfigMap %q", names.TRUST_BUNDLE_CONFIGMAP_KEY, cfgMap.Name)
	}
	certBundle, _, err := CertificateData(trustBundleData)
	if err != nil {
		return nil, nil, fmt.Errorf("failed parsing certificate data from ConfigMap %q: %v", cfgMap.Name, err)
	}

	return certBundle, trustBundleData, nil
}

// CertificateData decodes certData, ensuring each PEM block is type
// "CERTIFICATE" and the block can be parsed as an x509 certificate,
// returning slices of parsed certificates and parsed certificate data.
func CertificateData(certData []byte) ([]*x509.Certificate, []byte, error) {
	var block *pem.Block
	certBundle := []*x509.Certificate{}
	for len(certData) != 0 {
		block, certData = pem.Decode(certData)
		if block == nil {
			return nil, nil, fmt.Errorf("failed to parse certificate PEM")
		}
		if block.Type != certPEMBlock {
			return nil, nil, fmt.Errorf("invalid certificate PEM, must be of type %q", certPEMBlock)

		}

		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to parse certificate: %v", err)
		}
		certBundle = append(certBundle, cert)
	}

	return certBundle, certData, nil
}
