package networkconfig

import (
	"context"
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"

	netv1 "github.com/openshift/cluster-network-operator/pkg/apis/networkoperator/v1"
)

const NAME_PREFIX = "applied-"

// The operator-sdk actually has the notion of an operator's namespace, but
// since the network configuration is not namespaced, we can't use it.
// This is hardcoded in 00_namespace.yaml anyways
const NAMESPACE = "openshift-cluster-network-operator"

// GetAppliedConfiguration retrieves the configuration we applied.
// Returns nil with no error if no previous configuration was observed.
func GetAppliedConfiguration(ctx context.Context, client k8sclient.Client, name string) (*netv1.NetworkConfigSpec, error) {
	cm := &corev1.ConfigMap{}
	err := client.Get(ctx, types.NamespacedName{Namespace: NAMESPACE, Name: NAME_PREFIX + name}, cm)
	if err != nil && apierrors.IsNotFound(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	spec := &netv1.NetworkConfigSpec{}
	err = json.Unmarshal([]byte(cm.Data["applied"]), spec)
	if err != nil {
		return nil, err
	}
	return spec, nil
}

// AppliedConfiguration renders the ConfigMap in which we store the configuration
// we've applied.
func AppliedConfiguration(applied *netv1.NetworkConfig) (*uns.Unstructured, error) {
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
			Namespace: NAMESPACE,
			Name:      NAME_PREFIX + applied.Name,
		},
		Data: map[string]string{
			"applied": string(app),
		},
	}

	// transmute to unstructured
	b, err := json.Marshal(cm)
	if err != nil {
		return nil, err
	}
	u := &uns.Unstructured{}
	if err := json.Unmarshal(b, &u); err != nil {
		return nil, err
	}
	return u, nil
}
