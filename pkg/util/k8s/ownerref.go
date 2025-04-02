package k8s

import (
	operv1 "github.com/openshift/api/operator/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ContainsNetworkOwnerRef returns true if any one the given OwnerReference is owned
// by cluster network operator, otherwise returns false
func ContainsNetworkOwnerRef(ownerRefs []metav1.OwnerReference) bool {
	for _, ownerRef := range ownerRefs {
		if ownerRef.APIVersion == operv1.GroupVersion.String() && ownerRef.Kind == "Network" &&
			(ownerRef.Controller != nil && *ownerRef.Controller) && ownerRef.Name == "cluster" {
			return true
		}
	}
	return false
}
