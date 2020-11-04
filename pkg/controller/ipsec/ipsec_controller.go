package ipsec

import (
	"context"
	c "crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"log"
	"math/big"
	"time"

	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/library-go/pkg/crypto"
	csrv1 "k8s.io/api/certificates/v1"

	"k8s.io/apimachinery/pkg/runtime"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const ipsecSignerName = "network.openshift.io/ipsec"

var ca *crypto.TLSCertificateConfig

// Add and Start it when the Manager is Started.
func Add(mgr manager.Manager, status *statusmanager.StatusManager) error {
	return add(mgr, newReconciler(mgr, status))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager, status *statusmanager.StatusManager) reconcile.Reconciler {
	return &ReconcileCSR{client: mgr.GetClient(), scheme: mgr.GetScheme(), status: status}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("ipsec-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to CetificateSigningRequest resource
	err = c.Watch(&source.Kind{Type: &csrv1.CertificateSigningRequest{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	// Make an ephemeral (in-memory) CA.
	// TODO: This is temporary as it should use the Operator PKI ovn-ca secret
	ca, _ = crypto.MakeSelfSignedCAConfig("ipsec-ca", 365)

	return nil
}

var _ reconcile.Reconciler = &ReconcileCSR{}

// ReconcileCSR reconciles a cluster CertificateSigningRequest object
type ReconcileCSR struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client client.Client
	scheme *runtime.Scheme
	status *statusmanager.StatusManager
}

// Reconcile
func (r *ReconcileCSR) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	csr := &csrv1.CertificateSigningRequest{}
	err := r.client.Get(context.TODO(), request.NamespacedName, csr)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Return and don't requeue
			log.Println("Object seems to have been deleted")
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Println(err)

		return reconcile.Result{}, err
	}

	// Only handle CSRs for this signer
	if csr.Spec.SignerName != ipsecSignerName {
		return reconcile.Result{}, nil
	}

	cert, err := extractCertificateRequest(csr.Spec.Request)
	if err != nil {
		log.Println("Could extract certificate from request")
		return reconcile.Result{}, nil
	}
	cacert, err := extractCert(ca)
	if err != nil {
		log.Println("Could not extract CA certificate")
		return reconcile.Result{}, nil
	}

	signedCert, err := signCertificate(newCertificateTemplate(), cert.PublicKey, cacert, ca.Key)
	if err != nil {
		log.Println("Could not sign request")
		return reconcile.Result{}, nil
	}

	csr.Status.Certificate, _ = crypto.EncodeCertificates(signedCert)
	err = r.client.Status().Update(context.TODO(), csr)
	if err != nil {
		log.Printf("error updating signature for csr: %v", err)
		return reconcile.Result{}, nil
	}
	return reconcile.Result{}, nil
}

func newCertificateTemplate() *x509.Certificate {
	subject := pkix.Name{CommonName: "template"}

	template := &x509.Certificate{
		Subject: subject,

		SignatureAlgorithm: x509.SHA256WithRSA,

		NotBefore:    time.Now().Add(-1 * time.Second),
		NotAfter:     time.Now().Add(24 * time.Hour),
		SerialNumber: big.NewInt(1),

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}

	return template
}

func extractCert(c *crypto.TLSCertificateConfig) (*x509.Certificate, error) {
	certBytes, _, err := c.GetPEMBytes()
	if err != nil {
		log.Println("Error getting PEM bytes")
		return nil, err
	}

	block, _ := pem.Decode(certBytes)
	if block == nil {
		err := errors.New("Error decoding PEM bytes")
		log.Println(err)
		return nil, err
	}

	return x509.ParseCertificate(block.Bytes)
}

func extractCertificateRequest(pemBytes []byte) (*x509.CertificateRequest, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		err := errors.New("PEM block type must be CERTIFICATE_REQUEST")
		log.Println(err)
		return nil, err
	}

	return x509.ParseCertificateRequest(block.Bytes)
}

func signCertificate(template *x509.Certificate, requestKey c.PublicKey, issuer *x509.Certificate, issuerKey c.PrivateKey) (*x509.Certificate, error) {
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
