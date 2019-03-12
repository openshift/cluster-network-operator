package clusterconfig

import (
	"context"
	"reflect"

	"github.com/pkg/errors"

	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/network"
	k8sutil "github.com/openshift/cluster-network-operator/pkg/util/k8s"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// UpdateOperatorConfig merges the cluster network configuration in to the
// operator configuration.
// The operator's CRD is necessarily much more complicated, and 99% of users
// will not need to create or touch it. So, they can touch the Network config.
// Any changes in the cluster config will be noticed by the operator and merged
// in to the operator CRD.
// This returns nil if no changes have been detected.
func (r *ReconcileClusterConfig) UpdateOperatorConfig(ctx context.Context, clusterConfig configv1.Network) (*uns.Unstructured, error) {
	operConfig := &operv1.Network{
		TypeMeta:   metav1.TypeMeta{APIVersion: operv1.GroupVersion.String(), Kind: "Network"},
		ObjectMeta: metav1.ObjectMeta{Name: names.OPERATOR_CONFIG},
	}

	err := r.client.Get(ctx, types.NamespacedName{Name: names.OPERATOR_CONFIG}, operConfig)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, errors.Wrapf(err, "could not retrieve Network.operator.openshift.io/v1 %s", names.OPERATOR_CONFIG)
	}

	newOperConfig := operConfig.DeepCopy()
	network.MergeClusterConfig(&newOperConfig.Spec, clusterConfig.Spec)
	if reflect.DeepEqual(newOperConfig.Spec, operConfig.Spec) {
		return nil, nil
	}

	return k8sutil.ToUnstructured(newOperConfig)
}
