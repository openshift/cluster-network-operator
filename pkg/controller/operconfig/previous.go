package operconfig

import (
	"context"
	"encoding/json"

	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/names"
	k8sutil "github.com/openshift/cluster-network-operator/pkg/util/k8s"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// GetAppliedConfiguration retrieves the configuration we applied.
// Returns nil with no error if no previous configuration was observed.
func GetAppliedConfiguration(ctx context.Context, client k8sclient.Client, name string) (*operv1.NetworkSpec, error) {
	cm := &corev1.ConfigMap{}
	err := client.Get(ctx, types.NamespacedName{Namespace: names.APPLIED_NAMESPACE, Name: names.APPLIED_PREFIX + name}, cm)
	if err != nil && apierrors.IsNotFound(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	spec := &operv1.NetworkSpec{}
	err = json.Unmarshal([]byte(cm.Data["applied"]), spec)
	if err != nil {
		return nil, err
	}
	return spec, nil
}

// AppliedConfiguration renders the ConfigMap in which we store the configuration
// we've applied.
func AppliedConfiguration(applied *operv1.Network) (*uns.Unstructured, error) {
	app, err := json.Marshal(applied.Spec)
	if err != nil {
		return nil, err
	}
	cm := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: names.APPLIED_NAMESPACE,
			Name:      names.APPLIED_PREFIX + applied.Name,
		},
		Data: map[string]string{
			"applied": string(app),
		},
	}

	// transmute to unstructured
	return k8sutil.ToUnstructured(cm)
}
