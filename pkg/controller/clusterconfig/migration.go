package clusterconfig

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/library-go/pkg/operator/v1helpers"

	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/util"
)

func (r *ReconcileClusterConfig) processNetworkTypeLiveMigration(ctx context.Context, request reconcile.Request, clusterConfig *configv1.Network, operConfig *operv1.Network) error {
	klog.Infof("process network type live migration to the target CNI: %s", clusterConfig.Spec.NetworkType)
	var err error
	currentOperConfig := &operv1.Network{}
	err = r.client.Default().CRClient().Get(ctx, types.NamespacedName{Name: names.OPERATOR_CONFIG}, currentOperConfig)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	// In live migration, preserve the network type change, we want to switch the DefaultNetwork.Type of network.operator when the prerequisite steps are completed.
	operConfig.Spec.DefaultNetwork.Type = currentOperConfig.Spec.DefaultNetwork.Type

	if meta.IsStatusConditionPresentAndEqual(clusterConfig.Status.Conditions, names.NetworkTypeMigrationInProgress, metav1.ConditionTrue) {
		if v1helpers.IsOperatorConditionTrue(operConfig.Status.Conditions, operv1.OperatorStatusTypeProgressing) {
			// Not update network.operator if the operator is processing
			return nil
		}
		operConfig.Spec.Migration = currentOperConfig.Spec.Migration
		return r.prepareOperatorConfigForNetworkTypeMigration(ctx, clusterConfig, operConfig)
	}
	return nil
}

// The prepareOperatorConfigForNetworkTypeMigration function prepares the operConfig object for network type migration based on the current state of the clusterConfig object.
// We don't modify the operator config when the MCPs are updating
// The migration will be conducted with the following steps:
// 1. deploy ovn-kubernetes pods
// 2. apply a MC with routable MTU configuration to each MCP
// 3. switch the cluster default CNI to ovn-kubernetes, it will trigger MCP update
// 4. purge the openshift-sdn CNI pods
//
// The rollbak will be conducted with the following steps:
// 1. deploy openshift-sdn pods
// 2. apply a MC with routable MTU to each MCP and switch the cluster default CNI to ovn-kubernetes, it will trigger MCP update
// 3. remove routable MTU configuration from each MCP
// 4. purge the openshift-sdn CNI pods
func (r *ReconcileClusterConfig) prepareOperatorConfigForNetworkTypeMigration(ctx context.Context, clusterConfig *configv1.Network, operConfig *operv1.Network) error {
	configConditions := clusterConfig.Status.Conditions
	if configConditions == nil {
		return fmt.Errorf("status.Conditions is not initialized")
	}

	mcpCondition := meta.FindStatusCondition(configConditions, names.NetworkTypeMigrationMTUReady)
	if mcpCondition == nil {
		return fmt.Errorf("condition %q not found", names.NetworkTypeMigrationMTUReady)
	}
	if mcpCondition.Reason == names.MachineConfigPoolDegraded {
		return fmt.Errorf("MCP is degraded, network type migration cannot proceed")
	}
	if mcpCondition.Reason == names.MachineConfigPoolsUpdating {
		klog.Infof("MCP is updating, so we don't modify the operator config")
		return nil
	}

	mtuApplied := meta.IsStatusConditionPresentAndEqual(configConditions, names.NetworkTypeMigrationMTUReady, metav1.ConditionTrue)
	cniReady := meta.IsStatusConditionPresentAndEqual(configConditions, names.NetworkTypeMigrationTargetCNIAvailable, metav1.ConditionTrue)
	mcApplied := meta.IsStatusConditionPresentAndEqual(configConditions, names.NetworkTypeMigrationTargetCNIInUse, metav1.ConditionTrue)

	var isRollback bool
	if clusterConfig.Spec.NetworkType == string(operv1.NetworkTypeOpenShiftSDN) {
		isRollback = true
	}

	if mcApplied {
		if cniReady {
			klog.Infof("step-4: purge the original CNI")
			operConfig.Spec.DefaultNetwork.Type = operv1.NetworkType(clusterConfig.Spec.NetworkType)
			operConfig.Spec.Migration = nil
		}
		return nil
	}

	if mtuApplied {
		klog.Infof("step-3: trigger MCO to apply the target MachineConfig")
		operConfig.Spec.DefaultNetwork.Type = operv1.NetworkType(clusterConfig.Spec.NetworkType)
		operConfig.Spec.Migration = &operv1.NetworkMigration{
			Mode:        operv1.LiveNetworkMigrationMode,
			NetworkType: clusterConfig.Spec.NetworkType,
		}
		return nil
	}

	if !cniReady && clusterConfig.Status.Migration == nil {
		klog.Infof("step-1: deploy target CNI: %s", clusterConfig.Spec.NetworkType)
		operConfig.Spec.Migration = &operv1.NetworkMigration{
			Mode:        operv1.LiveNetworkMigrationMode,
			NetworkType: clusterConfig.Spec.NetworkType,
		}
		return nil
	}
	if !mtuApplied && cniReady {
		if isRollback {
			klog.Infof("step-2: propagate the network type change to network.config")
			operConfig.Spec.DefaultNetwork.Type = operv1.NetworkType(clusterConfig.Spec.NetworkType)
		}
		mtuMigration, err := r.calculateRoutableMTU(ctx, clusterConfig, operConfig, clusterConfig.Status.NetworkType)
		if err != nil {
			return err
		}
		klog.Infof("step-2: apply routable MTU: %v", *mtuMigration.Network.To)
		operConfig.Spec.Migration = &operv1.NetworkMigration{
			Mode:        operv1.LiveNetworkMigrationMode,
			NetworkType: clusterConfig.Spec.NetworkType,
			MTU:         mtuMigration,
		}
	}
	return nil
}

func (r *ReconcileClusterConfig) calculateRoutableMTU(ctx context.Context, clusterConfig *configv1.Network, operConfig *operv1.Network, networkType string) (*operv1.MTUMigration, error) {
	if clusterConfig.Status.Migration != nil && clusterConfig.Status.Migration.MTU != nil {
		return &operv1.MTUMigration{
			Network: (*operv1.MTUMigrationValues)(clusterConfig.Status.Migration.MTU.Network),
			Machine: (*operv1.MTUMigrationValues)(clusterConfig.Status.Migration.MTU.Machine),
		}, nil
	}

	mtu, err := util.ReadMTUConfigMap(ctx, r.client)
	if err != nil {
		klog.Errorf("failed to read the MTU ConfigMap: %v", err)
		return nil, err
	}
	// keep machine MTU unchanged
	machineMTU := uint32(mtu)
	currentMTU := uint32(clusterConfig.Status.ClusterNetworkMTU)
	// 50 bytes is the default value of cluster MTU difference beteween OpenShiftSDN and OVNKubernetes
	routableMTU := currentMTU - 50
	if networkType == string(operv1.NetworkTypeOVNKubernetes) {
		routableMTU = currentMTU
	}

	return &operv1.MTUMigration{
		Network: &operv1.MTUMigrationValues{
			From: &currentMTU,
			To:   &routableMTU,
		},
		Machine: &operv1.MTUMigrationValues{
			To: &machineMTU,
		},
	}, nil
}
