package signer

import (
	"context"
	"fmt"
	"log"
	"time"

	features "github.com/openshift/api/features"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/library-go/pkg/crypto"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	csrv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const signerName = "network.openshift.io/signer"

// Add controller and start it when the Manager is started.
func Add(mgr manager.Manager, status *statusmanager.StatusManager, client cnoclient.Client, featureGates featuregates.FeatureGate) error {
	reconciler, err := newReconciler(client, mgr, status, featureGates)
	if err != nil {
		return err
	}
	return add(mgr, reconciler)
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(client cnoclient.Client, mgr manager.Manager, status *statusmanager.StatusManager, featureGates featuregates.FeatureGate) (reconcile.Reconciler, error) {
	certDuration := 5 * 365 * 24 * time.Hour
	if featureGates.Enabled(features.FeatureShortCertRotation) {
		certDuration = 3 * time.Hour
	}
	return &ReconcileCSR{client: client, scheme: mgr.GetScheme(), status: status, certDuration: certDuration}, nil

}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("signer-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to CetificateSigningRequest resource
	err = c.Watch(source.Kind[crclient.Object](mgr.GetCache(), &csrv1.CertificateSigningRequest{}, &handler.EnqueueRequestForObject{}))
	if err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &ReconcileCSR{}

// ReconcileCSR reconciles a cluster CertificateSigningRequest object. This
// will watch for changes to CertificateSigningRequest resources with
// SignerName == signerName. It will automatically approve these requests for
// signing. This assumes that the cluster has been configured in a way that
// no bad actors can make certificate signing requests. In future, we may decide
// to implement a scheme that would use a one-time token to validate a request.
//
// All requests will be signed using a CA, that is currently generated by
// the OperatorPKI, and the signed certificate will be returned in the status.
//
// This allows clients to get a signed certificate while maintaining
// private key confidentiality.
type ReconcileCSR struct {
	// This client, initialized using mgr.GetClient() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client       cnoclient.Client
	scheme       *runtime.Scheme
	status       *statusmanager.StatusManager
	certDuration time.Duration
}

// Reconcile CSR
func (r *ReconcileCSR) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	defer utilruntime.HandleCrash(r.status.SetDegradedOnPanicAndCrash)
	csr := &csrv1.CertificateSigningRequest{}
	err := r.client.Default().CRClient().Get(ctx, request.NamespacedName, csr)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Return and don't requeue as the CSR has been deleted.
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Println(err)

		return reconcile.Result{}, err
	}

	// Only handle CSRs for this signer
	if csr.Spec.SignerName != signerName {
		// Don't handle a CSR for another signerName. We don't need to log this as
		// we will pollute the logs. We also don't need to requeue it.
		return reconcile.Result{}, nil
	}

	isValid, err := r.isValidUserName(ctx, csr.Spec.Username)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("error occurred while validating CSR %s: %w", csr.Name, err)
	}
	if !isValid {
		// Update CSR status condition with Failed condition.
		updateCSRStatusConditions(r, csr, "CSRInvalidUser",
			"Certificate Signing Request is set with invalid user name, can't sign it")
		return reconcile.Result{}, nil
	}

	if len(csr.Status.Certificate) != 0 {
		// Request already has a certificate. There is nothing
		// to do as we will, currently, not re-certify or handle any updates to
		// CSRs.
		return reconcile.Result{}, nil
	}

	// We will make the assumption that anyone with permission to issue a
	// certificate signing request to this signer is automatically approved. This
	// is somewhat protected by permissions on the CSR resource.
	// TODO: We may need a more robust way to do this later
	if !isCertificateRequestApproved(csr) {
		csr.Status.Conditions = append(csr.Status.Conditions, csrv1.CertificateSigningRequestCondition{
			Type:    csrv1.CertificateApproved,
			Status:  "True",
			Reason:  "AutoApproved",
			Message: "Automatically approved by " + signerName})
		// Update status to "Approved"
		_, err = r.client.Default().Kubernetes().CertificatesV1().CertificateSigningRequests().UpdateApproval(ctx, request.Name, csr, metav1.UpdateOptions{})
		if err != nil {
			log.Printf("Unable to approve certificate for %v and signer %v: %v", request.Name, signerName, err)
			return reconcile.Result{}, err
		}

		// As the update from UpdateApproval() will get reconciled, we
		// no longer need to deal with this request
		return reconcile.Result{}, nil
	}

	// From this, point we are dealing with an approved CSR

	// Get our CA that was created by the operatorpki.
	caSecret := &corev1.Secret{}
	err = r.client.Default().CRClient().Get(ctx, types.NamespacedName{Namespace: "openshift-ovn-kubernetes", Name: "signer-ca"}, caSecret)
	if err != nil {
		signerFailure(r, csr, "CAFailure",
			fmt.Sprintf("Could not get CA certificate and key: %v", err))
		return reconcile.Result{}, err
	}

	// Decode the certificate request from PEM format.
	certReq, err := decodeCertificateRequest(csr.Spec.Request)
	if err != nil {
		// We dont degrade the status of the controller as this is due to a
		// malformed CSR rather than an issue with the controller.
		updateCSRStatusConditions(r, csr, "CSRDecodeFailure",
			fmt.Sprintf("Could not decode Certificate Request: %v", err))
		return reconcile.Result{}, nil
	}

	// Decode the CA certificate from PEM format.
	caCert, err := decodeCertificate(caSecret.Data["tls.crt"])
	if err != nil {
		signerFailure(r, csr, "CorruptCACert",
			fmt.Sprintf("Unable to decode CA certificate for %v: %v", signerName, err))
		return reconcile.Result{}, nil
	}

	// Decode the CA key from PEM format.
	caKey, err := decodePrivateKey(caSecret.Data["tls.key"])
	if err != nil {
		signerFailure(r, csr, "CorruptCAKey",
			fmt.Sprintf("Unable to decode CA private key for %v: %v", signerName, err))
		return reconcile.Result{}, nil
	}

	// Create a new certificate using the certificate template and certificate.
	// We can then sign this using the CA.
	signedCert, err := signCSR(newCertificateTemplate(certReq, r.certDuration), certReq.PublicKey, caCert, caKey)
	if err != nil {
		signerFailure(r, csr, "SigningFailure",
			fmt.Sprintf("Unable to sign certificate for %v and signer %v: %v", request.Name, signerName, err))
		return reconcile.Result{}, nil
	}

	// Encode the certificate into PEM format and add to the status of the CSR
	csr.Status.Certificate, err = crypto.EncodeCertificates(signedCert)
	if err != nil {
		signerFailure(r, csr, "EncodeFailure",
			fmt.Sprintf("Could not encode certificate: %v", err))
		return reconcile.Result{}, nil
	}

	err = r.client.Default().CRClient().Status().Update(ctx, csr)
	if err != nil {
		log.Printf("Unable to update signed certificate for %v and signer %v: %v", request.Name, signerName, err)
		return reconcile.Result{}, err
	}

	log.Printf("Certificate signed, issued and approved for %s by %s", request.Name, signerName)
	r.status.SetNotDegraded(statusmanager.CertificateSigner)
	return reconcile.Result{}, nil
}

func (r *ReconcileCSR) isValidUserName(ctx context.Context, csrUserName string) (bool, error) {
	nodeList, err := r.client.Default().Kubernetes().CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to list nodes: %v", err)
	}
	for _, node := range nodeList.Items {
		if fmt.Sprintf("system:ovn-node:%s", node.Name) == csrUserName {
			return true, nil
		}
	}
	return false, nil
}

// isCertificateRequestApproved returns true if a certificate request has the
// "Approved" condition and no "Denied" conditions; false otherwise.
func isCertificateRequestApproved(csr *csrv1.CertificateSigningRequest) bool {
	approved, denied := getCertApprovalCondition(&csr.Status)
	return approved && !denied
}

func getCertApprovalCondition(status *csrv1.CertificateSigningRequestStatus) (approved bool, denied bool) {
	for _, c := range status.Conditions {
		if c.Type == csrv1.CertificateApproved {
			approved = true
		}
		if c.Type == csrv1.CertificateDenied {
			denied = true
		}
	}
	return
}

// Something has gone wrong with the signer controller so we update the statusmanager, the csr
// and log.
func signerFailure(r *ReconcileCSR, csr *csrv1.CertificateSigningRequest, reason string, message string) {
	log.Printf("%s: %s", reason, message)
	updateCSRStatusConditions(r, csr, reason, message)
	r.status.SetDegraded(statusmanager.CertificateSigner, reason, message)
}

// Update the status conditions on the CSR object
func updateCSRStatusConditions(r *ReconcileCSR, csr *csrv1.CertificateSigningRequest, reason string, message string) {
	setCertificateSigningRequestCondition(&csr.Status.Conditions, csrv1.CertificateSigningRequestCondition{
		Type:    csrv1.CertificateFailed,
		Status:  "True",
		Reason:  reason,
		Message: message})

	err := r.client.Default().CRClient().Status().Update(context.TODO(), csr)
	if err != nil {
		log.Printf("Could not update CSR status: %v", err)
		r.status.SetDegraded(statusmanager.CertificateSigner, "UpdateFailure",
			fmt.Sprintf("Unable to update csr: %v", err))
	}
}

func setCertificateSigningRequestCondition(conditions *[]csrv1.CertificateSigningRequestCondition, newCondition csrv1.CertificateSigningRequestCondition) {
	if conditions == nil {
		conditions = &[]csrv1.CertificateSigningRequestCondition{}
	}
	var existingCondition *csrv1.CertificateSigningRequestCondition
	for i := range *conditions {
		if (*conditions)[i].Type == newCondition.Type {
			existingCondition = &(*conditions)[i]
		}
	}
	if existingCondition == nil {
		newCondition.LastTransitionTime = metav1.NewTime(time.Now())
		*conditions = append(*conditions, newCondition)
		return
	}
	existingCondition.Status = newCondition.Status
	existingCondition.Reason = newCondition.Reason
	existingCondition.Message = newCondition.Message
	existingCondition.LastTransitionTime = metav1.NewTime(time.Now())
}
