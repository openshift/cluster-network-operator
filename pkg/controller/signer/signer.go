package signer

import (
	c "crypto"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	mathrand "math/rand"
	"time"
)

func newCertificateTemplate(certReq *x509.CertificateRequest, certDuration time.Duration) *x509.Certificate {
	// Like in openshift/library-go/pkg/crypto/crypto.go, we will generate a random
	// serial number
	serialNumber := mathrand.New(mathrand.NewSource(time.Now().UTC().UnixNano())).Int63()

	template := &x509.Certificate{
		Subject: certReq.Subject,

		NotBefore:    time.Now().Add(-1 * time.Second),
		NotAfter:     time.Now().Add(certDuration),
		SerialNumber: big.NewInt(serialNumber),

		DNSNames:              certReq.DNSNames,
		BasicConstraintsValid: true,
	}

	return template
}

func signCSR(template *x509.Certificate, requestKey c.PublicKey, issuer *x509.Certificate, issuerKey c.PrivateKey) (*x509.Certificate, error) {
	derBytes, err := x509.CreateCertificate(rand.Reader, template, issuer, requestKey, issuerKey)
	if err != nil {
		return nil, err
	}
	certs, err := x509.ParseCertificates(derBytes)
	if err != nil {
		return nil, err
	}
	if len(certs) != 1 {
		return nil, errors.New("expected a single certificate")
	}
	return certs[0], nil
}

func decodeCertificateRequest(pemBytes []byte) (*x509.CertificateRequest, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		err := errors.New("PEM block type must be CERTIFICATE_REQUEST")
		return nil, err
	}

	return x509.ParseCertificateRequest(block.Bytes)
}

func decodeCertificate(pemBytes []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		err := errors.New("PEM block type must be CERTIFICATE")
		return nil, err
	}

	return x509.ParseCertificate(block.Bytes)
}

func decodePrivateKey(pemBytes []byte) (c.Signer, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("no PEM block found in private key data")
	}

	switch block.Type {
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse PKCS8 private key: %w", err)
		}
		signer, ok := key.(c.Signer)
		if !ok {
			return nil, fmt.Errorf("parsed private key does not implement crypto.Signer")
		}
		return signer, nil
	case "EC PRIVATE KEY":
		return x509.ParseECPrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("unsupported PEM block type: %s", block.Type)
	}
}
