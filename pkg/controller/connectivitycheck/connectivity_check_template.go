package connectivitycheck

import (
	"strings"

	v1 "github.com/openshift/api/config/v1"
	applyconfigv1alpha1 "github.com/openshift/client-go/operatorcontrolplane/applyconfigurations/operatorcontrolplane/v1alpha1"
)

// new PodNetworkConnectivityCheck whose name is '$(SOURCE)-to-$(TARGET)'.
// Use the WithSource and WithTarget option funcs to replace the '$(SOURCE)' and '$(TARGET)' tokens.
func NewPodNetworkConnectivityCheckTemplate(address, namespace string, options ...func(*applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration)) *applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration {
	check := applyconfigv1alpha1.PodNetworkConnectivityCheck("$(SOURCE)-to-$(TARGET)", namespace)
	check.Spec = &applyconfigv1alpha1.PodNetworkConnectivityCheckSpecApplyConfiguration{TargetEndpoint: &address}
	for _, option := range options {
		option(check)
	}
	return check
}

// WithTLSClientCert option specifies the name of the secret in the check namespace that
// contains a tls client certificate (and key) to use when performing the check.
func WithTLSClientCert(secretName string) func(*applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration) {
	return func(check *applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration) {
		if len(secretName) > 0 {
			check.Spec.TLSClientCert = &v1.SecretNameReference{Name: secretName}
		}
	}
}

// WithSource option replaces the $(SOURCE) token in the name.
func WithSource(source string) func(*applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration) {
	return func(check *applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration) {
		if check.Name == nil {
			return
		}
		name := strings.ReplaceAll(*check.Name, "$(SOURCE)", source)
		check.Name = &name
	}
}

// WithTarget option replaces the $(TARGET) token in the name.
func WithTarget(target string) func(*applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration) {
	return func(check *applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration) {
		if check.Name == nil {
			return
		}
		name := strings.ReplaceAll(*check.Name, "$(TARGET)", target)
		check.Name = &name
	}
}

// copySpecFields returns copy of given check object copying its name, namespace and its .Spec fields.
// This function is needed explicitly here because PodNetworkConnectivityCheckApplyConfiguration doesn't
// have DeepCopy method.
func copySpecFields(check *applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration) *applyconfigv1alpha1.PodNetworkConnectivityCheckApplyConfiguration {
	if check == nil || check.Name == nil || check.Namespace == nil {
		return nil
	}
	checkCopy := applyconfigv1alpha1.PodNetworkConnectivityCheck(*check.Name, *check.Namespace)
	if check.Spec != nil {
		checkCopy.Spec = &applyconfigv1alpha1.PodNetworkConnectivityCheckSpecApplyConfiguration{}
		if check.Spec.TargetEndpoint != nil {
			targetEndPoint := *check.Spec.TargetEndpoint
			checkCopy.Spec.TargetEndpoint = &targetEndPoint
		}
		if check.Spec.TLSClientCert != nil {
			checkCopy.Spec.TLSClientCert = check.Spec.TLSClientCert.DeepCopy()
		}
		if check.Spec.SourcePod != nil {
			sourcePod := *check.Spec.SourcePod
			checkCopy.Spec.SourcePod = &sourcePod
		}
	}
	return checkCopy
}
