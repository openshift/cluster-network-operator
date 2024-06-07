package operconfig

import (
	"context"
	"fmt"
	"log"
	"reflect"

	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/apply"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/network"
	k8sutil "github.com/openshift/cluster-network-operator/pkg/util/k8s"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// MergeClusterConfig merges in the existing cluster config in to the
// operator config, overwriting any changes to the managed fields.
func (r *ReconcileOperConfig) MergeClusterConfig(ctx context.Context, operConfig *operv1.Network, clusterConfig *configv1.Network) error {
	if _, ok := clusterConfig.Annotations[names.NetworkTypeMigrationAnnotation]; ok && r.featureGates.Enabled(configv1.FeatureGateNetworkLiveMigration) {
		// During network type live migration, all the update to network.operator shall only be handled by the clusterconfig controller
		return nil
	}
	// Validate cluster config
	// If invalid just warn and proceed.
	if err := network.ValidateClusterConfig(clusterConfig, r.client); err != nil {
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
	return r.UpdateOperConfig(ctx, operConfig)
}

func (r *ReconcileOperConfig) UpdateOperConfig(ctx context.Context, operConfig *operv1.Network) error {
	config := operConfig.DeepCopy()
	// Since ApplyObject uses server side apply operconfig controller
	// takes ownership of all fields set in operConfig.
	// It shouldn't own .Spec.Migration as it is not modifying it anywhere.
	// Setting the value to nil will ensure that the value of that field will stay unchanged (including the fieldManager).
	config.Spec.Migration = nil

	config.TypeMeta = metav1.TypeMeta{APIVersion: operv1.GroupVersion.String(), Kind: "Network"}
	us, err := k8sutil.ToUnstructured(config)
	if err != nil {
		return fmt.Errorf("failed to transmute operator config, err: %v", err)
	}
	if err = apply.ApplyObject(ctx, r.client, us, "operconfig"); err != nil {
		return fmt.Errorf("could not apply (%s) %s/%s, err: %w", operConfig.GroupVersionKind(), operConfig.GetNamespace(), operConfig.GetName(), err)
	}
	return nil
}

// ClusterNetworkStatus generates the cluster config Status based on the operator
// config.
func (r *ReconcileOperConfig) ClusterNetworkStatus(ctx context.Context, operConfig *operv1.Network, bootstrapResult *bootstrap.BootstrapResult) (*uns.Unstructured, error) {
	// retrieve the existing cluster config object
	clusterConfig := &configv1.Network{
		TypeMeta:   metav1.TypeMeta{APIVersion: configv1.GroupVersion.String(), Kind: "Network"},
		ObjectMeta: metav1.ObjectMeta{Name: names.CLUSTER_CONFIG},
	}

	err := r.client.Default().CRClient().Get(ctx, types.NamespacedName{
		Name: names.CLUSTER_CONFIG,
	}, clusterConfig)
	if err != nil && apierrors.IsNotFound(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	// Update the cluster config status
	status := network.StatusFromOperatorConfig(&operConfig.Spec, &clusterConfig.Status)
	if status == nil {
		return nil, nil
	}
	// Sync status.conditions when live migration is processing
	clusterConfigWithConditions := clusterConfig.DeepCopy()
	nowTimestamp := metav1.Now()
	if _, ok := clusterConfig.Annotations[names.NetworkTypeMigrationAnnotation]; ok && r.featureGates.Enabled(configv1.FeatureGateNetworkLiveMigration) {
		if meta.IsStatusConditionPresentAndEqual(clusterConfig.Status.Conditions, names.NetworkTypeMigrationInProgress, metav1.ConditionTrue) {
			err = r.syncNetworkTypeMigrationConditions(ctx, operConfig, clusterConfigWithConditions)
			if err != nil {
				return nil, err
			}
		} else if clusterConfig.Spec.NetworkType != clusterConfig.Status.NetworkType {
			// Do not initialize live migration if it is not valid
			if err := network.ValidateLiveMigration(clusterConfig, &bootstrapResult.Infra, r.client); err != nil {
				return nil, err
			}
			initMigrationConditions(&clusterConfigWithConditions.Status.Conditions, nowTimestamp)
		} else {
			resetMigrationConditions(&clusterConfigWithConditions.Status.Conditions, nowTimestamp)
		}
		syncLiveMigrationConditionMetric(clusterConfigWithConditions.Status.Conditions)
	}

	status.Conditions = clusterConfigWithConditions.Status.Conditions
	if reflect.DeepEqual(*status, clusterConfig.Status) {
		return nil, nil
	}

	clusterConfig.Status = *status
	clusterConfig.TypeMeta = metav1.TypeMeta{APIVersion: configv1.GroupVersion.String(), Kind: "Network"}

	obj, err := k8sutil.ToUnstructured(clusterConfig)
	// TODO(kyrtapz) Use apply configurations instead
	// Remove the networkDiagnostics as this field is a optional non-pointer(always gets marshaled in json)
	// but it might not exist in the API and should only be set by the user
	uns.RemoveNestedField(obj.Object, "spec", "networkDiagnostics")
	return obj, err
}
