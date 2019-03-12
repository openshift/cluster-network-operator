package operconfig

import (
	"context"
	"log"
	"reflect"

	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/apply"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/network"
	k8sutil "github.com/openshift/cluster-network-operator/pkg/util/k8s"
	"github.com/pkg/errors"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// MergeClusterConfig merges in the existing cluster config in to the
// operator config, overwriting any changes to the managed fields.
func (r *ReconcileOperConfig) MergeClusterConfig(ctx context.Context, operConfig *operv1.Network) error {
	// fetch the cluster config
	clusterConfig := &configv1.Network{}
	err := r.client.Get(context.TODO(), types.NamespacedName{Name: names.CLUSTER_CONFIG}, clusterConfig)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	// Validate cluster config
	// If invalid just warn and proceed.
	if err := network.ValidateClusterConfig(clusterConfig.Spec); err != nil {
		log.Printf("WARNING: ignoring Network.config.openshift.io/v1/cluster - failed validation: %v", err)
		return nil
	}

	oldOperConfig := operConfig.DeepCopy()

	// Merge the cluster config in to the downstream CRD
	network.MergeClusterConfig(&operConfig.Spec, clusterConfig.Spec)
	if reflect.DeepEqual(operConfig.Spec, oldOperConfig.Spec) {
		return nil
	}

	// If there are changes to the "downstream" networkconfig, commit it back
	// to the apiserver
	log.Println("WARNING: Network.operator.openshift.io has fields being overwritten by Network.config.openshift.io configuration")
	// Have to restore the typemeta due to some weird shared cache bug --cdc
	operConfig.TypeMeta = metav1.TypeMeta{APIVersion: operv1.GroupVersion.String(), Kind: "Network"}
	us, err := k8sutil.ToUnstructured(operConfig)
	if err != nil {
		return errors.Wrapf(err, "failed to transmute operator config")
	}
	if err = apply.ApplyObject(context.TODO(), r.client, us); err != nil {
		return errors.Wrapf(err, "could not apply (%s) %s/%s", operConfig.GroupVersionKind(), operConfig.GetNamespace(), operConfig.GetName())
	}
	return nil
}

// ClusterNetworkStatus generates the cluster config Status based on the operator
// config.
func (r *ReconcileOperConfig) ClusterNetworkStatus(ctx context.Context, operConfig *operv1.Network) (*uns.Unstructured, error) {
	// retrieve the existing cluster config object
	clusterConfig := &configv1.Network{
		TypeMeta:   metav1.TypeMeta{APIVersion: configv1.GroupVersion.String(), Kind: "Network"},
		ObjectMeta: metav1.ObjectMeta{Name: names.CLUSTER_CONFIG},
	}

	err := r.client.Get(ctx, types.NamespacedName{
		Name: names.CLUSTER_CONFIG,
	}, clusterConfig)
	if err != nil && apierrors.IsNotFound(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	// Update the cluster config status
	status := network.StatusFromOperatorConfig(&operConfig.Spec)
	if reflect.DeepEqual(status, clusterConfig.Status) {
		return nil, nil
	}
	clusterConfig.Status = status
	clusterConfig.TypeMeta = metav1.TypeMeta{APIVersion: configv1.GroupVersion.String(), Kind: "Network"}

	return k8sutil.ToUnstructured(clusterConfig)
}
