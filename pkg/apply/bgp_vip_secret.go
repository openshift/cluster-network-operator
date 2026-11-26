package apply

import (
	"bytes"
	"context"
	"fmt"

	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/names"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// IsBGPVIPPasswordCopy identifies the desired Secret that requires guarded writes rather than forced apply.
func IsBGPVIPPasswordCopy(obj *uns.Unstructured) bool {
	return obj.GetAPIVersion() == "v1" && obj.GetKind() == "Secret" && obj.GetNamespace() == names.FRRK8sNamespace &&
		obj.GetAnnotations()[names.BGPVIPPasswordSource] == "openshift-config/"+obj.GetName()
}

func ownedBGPVIPPasswordCopy(secret *corev1.Secret, ownerUID types.UID) bool {
	ref := metav1.GetControllerOf(secret)
	return secret.Type == corev1.SecretTypeBasicAuth && ownerUID != "" && ref != nil && ref.UID == ownerUID && ref.APIVersion == operv1.GroupVersion.String() &&
		ref.Kind == "Network" && ref.Name == names.OperatorConfig &&
		secret.Annotations[names.BGPVIPPasswordSource] == "openshift-config/"+secret.Name &&
		secret.Labels["app.kubernetes.io/managed-by"] == "cluster-network-operator"
}

// ReconcileBGPVIPPasswordCopy creates a new copy or rotates an owned copy.
// Create and resource-versioned Update leave a concurrent creator or replacement untouched.
func ReconcileBGPVIPPasswordCopy(ctx context.Context, client crclient.Client, obj *uns.Unstructured, ownerUID types.UID) error {
	var desired, current corev1.Secret
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &desired); err != nil {
		return fmt.Errorf("cannot decode rendered BGP password copy %s/%s", obj.GetNamespace(), obj.GetName())
	}
	key := crclient.ObjectKeyFromObject(&desired)
	if !ownedBGPVIPPasswordCopy(&desired, ownerUID) {
		return fmt.Errorf("missing BGP password copy ownership on %s", key)
	}
	err := client.Get(ctx, key, &current)
	if apierrors.IsNotFound(err) {
		return client.Create(ctx, &desired)
	}
	if err != nil {
		return err
	}
	if !ownedBGPVIPPasswordCopy(&current, ownerUID) {
		return fmt.Errorf("BGP peer password Secret collision at %s: not the expected CNO-managed copy", key)
	}
	if !current.DeletionTimestamp.IsZero() {
		return fmt.Errorf("BGP peer password Secret %s is being deleted", key)
	}
	if bytes.Equal(current.Data[corev1.BasicAuthPasswordKey], desired.Data[corev1.BasicAuthPasswordKey]) {
		return nil
	}
	if current.Data == nil {
		current.Data = map[string][]byte{}
	}
	current.Data[corev1.BasicAuthPasswordKey] = desired.Data[corev1.BasicAuthPasswordKey]
	return client.Update(ctx, &current)
}

// PruneBGPVIPPasswordCopy avoids deleting an unrelated Secret that reuses a previously tracked name.
func PruneBGPVIPPasswordCopy(ctx context.Context, client crclient.Client, key crclient.ObjectKey) error {
	var current corev1.Secret
	if err := client.Get(ctx, key, &current); err != nil {
		return crclient.IgnoreNotFound(err)
	}
	var owner operv1.Network
	if err := client.Get(ctx, crclient.ObjectKey{Name: names.OperatorConfig}, &owner); err != nil {
		return crclient.IgnoreNotFound(err)
	}
	if !ownedBGPVIPPasswordCopy(&current, owner.UID) {
		return nil
	}
	return client.Delete(ctx, &current, crclient.Preconditions{UID: &current.UID, ResourceVersion: &current.ResourceVersion})
}
