package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// OperatorPKI is a simple certificate authority. It is not intended for external
// use - rather, it is internal to the network operator. The CNO creates a CA and
// a certificate signed by that CA. The certificate has both ClientAuth
// and ServerAuth extended usages enabled.
//
//  More specifically, given an OperatorPKI with <name>, the CNO will manage:
// - A Secret called <name>-ca with two data keys:
//   - tls.key - the private key
//   - tls.crt - the CA certificate
// - A ConfigMap called <name>-ca with a single data key:
//   - cabundle.crt - the CA certificate(s)
// - A Secret called <name>-cert with two data keys:
//   - tls.key - the private key
//   - tls.crt - the certificate, signed by the CA
//
// The CA certificate will have a validity of 10 years, rotated after 9.
// The target certificate will have a validity of 6 months, rotated after 3
//
// The CA certificate will have a CommonName of "<namespace>_<name>-ca@<timestamp>", where
// <timestamp> is the last rotation time.
//
// +k8s:openapi-gen=true
// +kubebuilder:resource:path=operatorpkis,scope=Namespaced
type OperatorPKI struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:validation:Required
	Spec OperatorPKISpec `json:"spec"`

	Status OperatorPKIStatus `json:"status,omitempty"`
}

// OperatorPKISpec is the PKI configuration.
// +k8s:openapi-gen=true
// +kubebuilder:validation:Required
type OperatorPKISpec struct {
	// targetCert configures the certificate signed by the CA. It will have
	// both ClientAuth and ServerAuth enabled
	TargetCert CertSpec `json:"targetCert"`
}

// CertSpec defines common certificate configuration.
type CertSpec struct {
	// commonName is the value in the certificate's CN
	//
	// +kubebuilder:validation:MinLength=1
	CommonName string `json:"commonName"`
}

// OperatorPKIStatus is not implemented.
type OperatorPKIStatus struct {
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// OperatorPKIList contains a list of OperatorPKI
type OperatorPKIList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OperatorPKI `json:"items"`
}
