package signer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/client/fake"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/library-go/pkg/crypto"
	certificatev1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var (
	csrName  = "ipsec-csr"
	nodeName = "testnode"
	coName   = "testing"
)

//nolint:errcheck
func init() {
	certificatev1.AddToScheme(scheme.Scheme)
	corev1.AddToScheme(scheme.Scheme)
}

func TestSigner_reconciler(t *testing.T) {
	g := NewGomegaWithT(t)
	client := fake.NewFakeClient()
	status := statusmanager.New(client, coName, names.StandAloneClusterName)
	signer := ReconcileCSR{client: client, status: status}

	co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: coName}}
	setCO(t, client, co)
	no := &operv1.Network{ObjectMeta: metav1.ObjectMeta{Name: names.OPERATOR_CONFIG}}
	setOC(t, client, no)

	csr, err := generateCSR()
	g.Expect(err).NotTo(HaveOccurred())
	csrObj := &certificatev1.CertificateSigningRequest{}
	csrObj.Name = csrName
	csrObj.Spec.Request = []byte(csr)
	csrObj.Spec.SignerName = signerName
	csrObj.Spec.Usages = []certificatev1.KeyUsage{"ipsec tunnel"}
	csrObj.Spec.Username = fmt.Sprintf("system:ovn-node:%s", nodeName)
	csrObj.Status.Conditions = append(csrObj.Status.Conditions, certificatev1.CertificateSigningRequestCondition{
		Type:    certificatev1.CertificateApproved,
		Status:  "True",
		Reason:  "AutoApproved",
		Message: "Automatically approved by " + signerName})

	err = client.Default().CRClient().Create(context.TODO(), csrObj)
	g.Expect(err).NotTo(HaveOccurred())
	_, err = client.Default().Kubernetes().CertificatesV1().CertificateSigningRequests().Create(context.TODO(), csrObj, metav1.CreateOptions{})
	g.Expect(err).NotTo(HaveOccurred())

	node := &corev1.Node{}
	node.Name = nodeName
	_, err = client.Default().Kubernetes().CoreV1().Nodes().Create(context.TODO(), node, metav1.CreateOptions{})
	g.Expect(err).NotTo(HaveOccurred())

	ca, err := crypto.MakeSelfSignedCAConfigForDuration(signerName, 10*time.Minute)
	g.Expect(err).NotTo(HaveOccurred())
	certBytes := &bytes.Buffer{}
	keyBytes := &bytes.Buffer{}
	err = ca.WriteCertConfig(certBytes, keyBytes)
	g.Expect(err).NotTo(HaveOccurred())
	caSecret := &corev1.Secret{}
	caSecret.Name = "signer-ca"
	caSecret.Namespace = "openshift-ovn-kubernetes"
	caSecret.Data = make(map[string][]byte)
	caSecret.Data["tls.crt"] = certBytes.Bytes()
	caSecret.Data["tls.key"] = keyBytes.Bytes()
	err = client.Default().CRClient().Create(context.TODO(), caSecret)
	g.Expect(err).NotTo(HaveOccurred())

	_, err = signer.Reconcile(context.TODO(),
		reconcile.Request{NamespacedName: types.NamespacedName{Name: csrName}})
	g.Expect(err).NotTo(HaveOccurred())

	err = client.Default().CRClient().Get(context.TODO(), types.NamespacedName{Name: csrName}, csrObj)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(csrObj.Status.Certificate).ShouldNot(BeEmpty())

	co, _, err = getStatuses(client, "testing")
	if err != nil {
		t.Fatalf("error getting network.operator: %v", err)
	}
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
		{
			Type:   configv1.OperatorDegraded,
			Status: configv1.ConditionFalse,
		},
	})).To(BeTrue())
	g.Expect(conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
		{
			Type:   configv1.OperatorUpgradeable,
			Status: configv1.ConditionTrue,
		},
	})).To(BeTrue())
}

func TestSigner_reconciler_withInvalidUserName(t *testing.T) {
	g := NewGomegaWithT(t)
	client := fake.NewFakeClient()
	status := statusmanager.New(client, coName, names.StandAloneClusterName)
	signer := ReconcileCSR{client: client, status: status}

	co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: coName}}
	setCO(t, client, co)
	no := &operv1.Network{ObjectMeta: metav1.ObjectMeta{Name: names.OPERATOR_CONFIG}}
	setOC(t, client, no)

	csr, err := generateCSR()
	g.Expect(err).NotTo(HaveOccurred())
	csrObj := &certificatev1.CertificateSigningRequest{}
	csrObj.Name = csrName
	csrObj.Spec.Request = []byte(csr)
	csrObj.Spec.SignerName = signerName
	csrObj.Spec.Usages = []certificatev1.KeyUsage{"ipsec tunnel"}
	csrObj.Spec.Username = fmt.Sprintf("system:ovn-node:%s", "suspicious-node")

	err = client.Default().CRClient().Create(context.TODO(), csrObj)
	g.Expect(err).NotTo(HaveOccurred())
	_, err = client.Default().Kubernetes().CertificatesV1().CertificateSigningRequests().Create(context.TODO(), csrObj, metav1.CreateOptions{})
	g.Expect(err).NotTo(HaveOccurred())

	node := &corev1.Node{}
	node.Name = nodeName
	_, err = client.Default().Kubernetes().CoreV1().Nodes().Create(context.TODO(), node, metav1.CreateOptions{})
	g.Expect(err).NotTo(HaveOccurred())

	_, err = signer.Reconcile(context.TODO(),
		reconcile.Request{NamespacedName: types.NamespacedName{Name: csrName}})
	g.Expect(err).NotTo(HaveOccurred())

	err = client.Default().CRClient().Get(context.TODO(), types.NamespacedName{Name: csrName}, csrObj)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(csrObj.Status.Certificate).Should(BeEmpty())
	csrConditions := csrObj.Status.Conditions
	g.Expect(len(csrConditions)).To(Equal(1))
	g.Expect(csrConditions[0].Reason).To(Equal("CSRInvalidUser"))
	g.Expect(csrConditions[0].Type).To(Equal(certificatev1.CertificateFailed))
}

func generateCSR() (string, error) {
	// Create private key.
	csrKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", fmt.Errorf("failed to generate private key: %v", err)
	}
	// Create CSR with private key.
	csrBytes, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, csrKey)
	if err != nil {
		return "", err
	}
	// Encode CSR in PEM format.
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrBytes})
	if csrPEM == nil {
		return "", fmt.Errorf("failed to encode CSR in PEM format")
	}
	return string(csrPEM), nil
}

func setOC(t *testing.T, client cnoclient.Client, oc *operv1.Network) {
	t.Helper()
	g := NewGomegaWithT(t)
	_, err := client.Default().OpenshiftOperatorClient().OperatorV1().Networks().Update(context.TODO(), oc, metav1.UpdateOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.Default().OpenshiftOperatorClient().OperatorV1().Networks().Create(context.TODO(), oc, metav1.CreateOptions{})
	}
	g.Expect(err).NotTo(HaveOccurred())
}

func setCO(t *testing.T, client cnoclient.Client, co *configv1.ClusterOperator) {
	t.Helper()
	g := NewGomegaWithT(t)
	err := client.Default().CRClient().Update(context.TODO(), co)
	if apierrors.IsNotFound(err) {
		err = client.Default().CRClient().Create(context.TODO(), co)
	}
	g.Expect(err).NotTo(HaveOccurred())
}

func getStatuses(client cnoclient.Client, name string) (*configv1.ClusterOperator, *operv1.Network, error) {
	co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: name}}
	err := client.ClientFor("").CRClient().Get(context.TODO(), types.NamespacedName{Name: name}, co)
	if err != nil {
		return nil, nil, err
	}
	oc, err := client.Default().OpenshiftOperatorClient().OperatorV1().Networks().Get(context.TODO(), names.OPERATOR_CONFIG, metav1.GetOptions{})
	return co, oc, err
}

// Tests that the parts of newConditions that are set match what's in oldConditions (but
// doesn't look at anything else in oldConditions)
func conditionsInclude(oldConditions, newConditions []configv1.ClusterOperatorStatusCondition) bool {
	for _, newCondition := range newConditions {
		foundMatchingCondition := false

		for _, oldCondition := range oldConditions {
			if newCondition.Type != oldCondition.Type || newCondition.Status != oldCondition.Status {
				continue
			}
			if newCondition.Reason != "" && newCondition.Reason != oldCondition.Reason {
				return false
			}
			if newCondition.Message != "" && newCondition.Message != oldCondition.Message {
				return false
			}
			foundMatchingCondition = true
			break
		}

		if !foundMatchingCondition {
			return false
		}
	}

	return true
}
