package connectivitycheckcontroller

import (
	"strings"

	v1 "github.com/openshift/api/config/v1"
	"github.com/openshift/api/operatorcontrolplane/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// new PodNetworkConnectivityCheck whose name is '$(SOURCE)-to-$(TARGET)'.
// Use the WithSource and WithTarget option funcs to replace the '$(SOURCE)' and '$(TARGET)' tokens.
func NewPodNetworkConnectivityCheckTemplate(address, namespace string, options ...func(*v1alpha1.PodNetworkConnectivityCheck)) *v1alpha1.PodNetworkConnectivityCheck {
	check := &v1alpha1.PodNetworkConnectivityCheck{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "$(SOURCE)-to-$(TARGET)",
			Namespace: namespace,
		},
		Spec: v1alpha1.PodNetworkConnectivityCheckSpec{
			TargetEndpoint: address,
		},
	}
	for _, option := range options {
		option(check)
	}
	return check
}

// WithTlsClientCert option specifies the name of the secret in the check namespace that
// contains a tls client certificate (and key) to use when performing the check.
func WithTlsClientCert(secretName string) func(*v1alpha1.PodNetworkConnectivityCheck) {
	return func(check *v1alpha1.PodNetworkConnectivityCheck) {
		if len(secretName) > 0 {
			check.Spec.TLSClientCert = v1.SecretNameReference{Name: secretName}
		}
	}
}

// WithSource option replaces the $(SOURCE) token in the name.
func WithSource(source string) func(*v1alpha1.PodNetworkConnectivityCheck) {
	return func(check *v1alpha1.PodNetworkConnectivityCheck) {
		check.Name = strings.Replace(check.Name, "$(SOURCE)", source, -1)
	}
}

// WithTarget option replaces the $(TARGET) token in the name.
func WithTarget(target string) func(*v1alpha1.PodNetworkConnectivityCheck) {
	return func(check *v1alpha1.PodNetworkConnectivityCheck) {
		check.Name = strings.Replace(check.Name, "$(TARGET)", target, -1)
	}
}
