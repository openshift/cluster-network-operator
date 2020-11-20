package signer

import (
	c "crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	mathrand "math/rand"
	"time"
)

const (
	oneYear = 365 * 24 * time.Hour
)

func newCertificateTemplate(certReq *x509.CertificateRequest) *x509.Certificate {
	// Like in openshift/library-go/pkg/crypto/crypto.go, we will generate a random
	// serial number
	serialNumber := mathrand.New(mathrand.NewSource(time.Now().UTC().UnixNano())).Int63()

	template := &x509.Certificate{
		Subject: certReq.Subject,

		SignatureAlgorithm: x509.SHA512WithRSA,

		NotBefore:    time.Now().Add(-1 * time.Second),
		NotAfter:     time.Now().Add(5 * oneYear),
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
		return nil, errors.New("Expected a single certificate")
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

func decodePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "RSA PRIVATE KEY" {
		fmt.Println(block.Type)
		err := errors.New("PEM block type must be RSA PRIVATE KEY")
		return nil, err
	}

	return x509.ParsePKCS1PrivateKey(block.Bytes)
}
