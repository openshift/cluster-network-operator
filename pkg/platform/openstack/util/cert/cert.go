package cert

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"time"

	"github.com/pkg/errors"
)

func GenerateCA(networkName string) ([]byte, []byte, error) {
	log.Print("Generating Webhook CA")
	caTemplate := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: fmt.Sprintf("%s-ca@%d", networkName, time.Now().Unix()),
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  true,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}

	caPrivKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to genarate CA private key")
	}
	caBytes, err := x509.CreateCertificate(rand.Reader, &caTemplate, &caTemplate, &caPrivKey.PublicKey, caPrivKey)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to create CA")
	}

	caPEM := bytes.Buffer{}
	if err := pem.Encode(&caPEM, &pem.Block{Type: "CERTIFICATE", Bytes: caBytes}); err != nil {
		return nil, nil, errors.Wrap(err, "failed to PEM encode CA")
	}

	caKeyPEM := bytes.Buffer{}
	if err := pem.Encode(&caKeyPEM, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(caPrivKey)}); err != nil {
		return nil, nil, errors.Wrap(err, "failed to PEM encode ca private key")
	}

	return caPEM.Bytes(), caKeyPEM.Bytes(), nil
}

func GenerateCertificate(networkName string, dnsNames []string, caPEM []byte, caPrivateKey []byte) ([]byte, []byte, error) {
	log.Print("Generating Webhook certificate")
	cert := x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject: pkix.Name{
			CommonName: fmt.Sprintf("%s@%d", networkName, time.Now().Unix()),
		},
		DNSNames:     dnsNames,
		NotBefore:    time.Now(),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		SubjectKeyId: []byte{1, 2, 3, 4, 6},
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}

	cablock, _ := pem.Decode([]byte(caPEM))
	if cablock == nil {
		return nil, nil, errors.Errorf("failed to decode certificate PEM")
	}
	ca, err := x509.ParseCertificate(cablock.Bytes)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to parse certificate PEM")
	}

	block, _ := pem.Decode([]byte(caPrivateKey))
	if block == nil {
		return nil, nil, errors.Errorf("failed to decode private key PEM")
	}
	privateKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to parse certificate PEM")
	}

	certPrivKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to genarate Cert private key")
	}
	certBytes, err := x509.CreateCertificate(rand.Reader, &cert, ca, &certPrivKey.PublicKey, privateKey)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to create Service certificate")
	}

	certKeyPEM := bytes.Buffer{}
	if err := pem.Encode(&certKeyPEM, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(certPrivKey)}); err != nil {
		return nil, nil, errors.Wrap(err, "failed to PEM encode cert private key")
	}
	certPEM := bytes.Buffer{}
	if err := pem.Encode(&certPEM, &pem.Block{Type: "CERTIFICATE", Bytes: certBytes}); err != nil {
		return nil, nil, errors.Wrap(err, "failed to PEM encode cert")
	}
	return certPEM.Bytes(), certKeyPEM.Bytes(), nil
}
