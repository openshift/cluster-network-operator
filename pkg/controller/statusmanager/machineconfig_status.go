package statusmanager

import (
	"fmt"

	"github.com/openshift/cluster-network-operator/pkg/platform"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
)

func (status *StatusManager) SetFromMachineConfigPool(mcPools []mcfgv1.MachineConfigPool) {
	status.Lock()
	defer status.Unlock()

	// When network operator owned machine configs exist for master or worker role, then check
	// its machine config pools and update network operator degraded and progressing conditions
	// accordingly.
	masterMCs := status.networkMachineConfigs(platform.MasterRoleMachineConfigLabelValue)
	workerMCs := status.networkMachineConfigs(platform.WorkerRoleMachineConfigLabelValue)
	if masterMCs != nil && masterMCs.Len() > 0 {
		done := status.updateNetworkStatus(mcPools, platform.MasterRoleMachineConfigLabelValue, masterMCs,
			platform.MasterRoleMachineConfigLabel, platform.AreMachineConfigsRenderedOnPool)
		if !done {
			return
		}
	}
	if workerMCs != nil && workerMCs.Len() > 0 {
		done := status.updateNetworkStatus(mcPools, platform.WorkerRoleMachineConfigLabelValue, workerMCs,
			platform.WorkerRoleMachineConfigLabel, platform.AreMachineConfigsRenderedOnPool)
		if !done {
			return
		}
	}

	// Now it's time to check for status of deleted machine configs.
	masterMCs = status.deletedNetworkMachineConfigs(platform.MasterRoleMachineConfigLabelValue)
	workerMCs = status.deletedNetworkMachineConfigs(platform.WorkerRoleMachineConfigLabelValue)
	// If no network MCs present on the deletedMachineConfigs map, then it's ok to
	// return now with success.
	if noNetworkMachineConfigs(masterMCs, workerMCs) {
		status.setNotDegraded(MachineConfig)
		status.unsetProgressing(MachineConfig)
		return
	}
	// When Network operator owned machine configs are still getting removed from the pool, then update
	// network operator degraded and progressing conditions accordingly.
	if masterMCs != nil && masterMCs.Len() > 0 {
		done := status.updateNetworkStatus(mcPools, platform.MasterRoleMachineConfigLabelValue, masterMCs,
			platform.MasterRoleMachineConfigLabel, platform.AreMachineConfigsRemovedFromPool)
		if !done {
			return
		}
		delete(status.deletedMachineConfigs, platform.MasterRoleMachineConfigLabelValue)
	}
	if workerMCs != nil && workerMCs.Len() > 0 {
		done := status.updateNetworkStatus(mcPools, platform.WorkerRoleMachineConfigLabelValue, workerMCs,
			platform.WorkerRoleMachineConfigLabel, platform.AreMachineConfigsRemovedFromPool)
		if !done {
			return
		}
		delete(status.deletedMachineConfigs, platform.WorkerRoleMachineConfigLabelValue)
	}
}

func (status *StatusManager) updateNetworkStatus(mcPools []mcfgv1.MachineConfigPool, mcRole string, machineConfigs sets.Set[string],
	mcLabel labels.Set, test func(status mcfgv1.MachineConfigPoolStatus, machineConfigs sets.Set[string]) bool) (done bool) {
	pools, err := status.findMachineConfigPoolsForLabel(mcPools, mcLabel)
	if err != nil {
		klog.Errorf("failed to get machine config pools for the label %s: %v", mcLabel, err)
	}
	degraded := status.isMachineConfigPoolDegraded(pools)
	if degraded {
		status.setDegraded(MachineConfig, "MachineConfig", fmt.Sprintf("%s role machine config pool in degraded state", mcRole))
		return
	}
	status.setNotDegraded(MachineConfig)

	progressing := status.isMachineConfigPoolProgressing(pools)
	if progressing {
		status.setProgressing(MachineConfig, "MachineConfig", fmt.Sprintf("%s role machine config pool in progressing state", mcRole))
		return
	}
	for _, pool := range pools {
		rendered := test(pool.Status, machineConfigs)
		if !rendered {
			status.setProgressing(MachineConfig, "MachineConfig",
				fmt.Sprintf("%s role machine config pool is still processing with network machine config", mcRole))
			return
		}
	}
	done = true
	status.unsetProgressing(MachineConfig)
	return
}

func (status *StatusManager) processDeletedMachineConfig(mcName string) {
	status.Lock()
	defer status.Unlock()

	for role, mcs := range status.availableMachineConfigs {
		if !mcs.Has(mcName) {
			continue
		}
		status.availableMachineConfigs[role].Delete(mcName)
		if status.availableMachineConfigs[role].Len() == 0 {
			delete(status.availableMachineConfigs, role)
		}
		if _, ok := status.deletedMachineConfigs[role]; !ok {
			status.deletedMachineConfigs[role] = sets.Set[string]{}
		}
		status.deletedMachineConfigs[role].Insert(mcName)
		return
	}
}

func (status *StatusManager) processCreatedMachineConfig(mc mcfgv1.MachineConfig) {
	// store machine config names into status.availableMachineConfigs.
	var mcRole string
	if role, exists := mc.Labels[platform.MachineConfigLabelRoleKey]; exists {
		mcRole = role
	} else {
		klog.Errorf("machine config %s doesn't have %s label, skipping it", mc.Name, platform.MachineConfigLabelRoleKey)
		return
	}
	status.Lock()
	defer status.Unlock()
	if _, ok := status.availableMachineConfigs[mcRole]; !ok {
		status.availableMachineConfigs[mcRole] = sets.Set[string]{}
	}
	status.availableMachineConfigs[mcRole].Insert(mc.Name)
}

func (status *StatusManager) isMachineConfigPoolDegraded(pools []mcfgv1.MachineConfigPool) bool {
	var degraded bool
	for _, pool := range pools {
		if pool.Spec.Paused {
			// Ignore pool from status reporting if it is in paused state.
			continue
		}
		if mcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolDegraded) {
			degraded = true
			break
		}
	}
	return degraded
}

func (status *StatusManager) isMachineConfigPoolProgressing(pools []mcfgv1.MachineConfigPool) bool {
	var progressing bool
	for _, pool := range pools {
		if pool.Spec.Paused {
			// Ignore pool from status reporting if it is in paused state.
			continue
		}
		if mcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolUpdating) {
			progressing = true
			break
		}
	}
	return progressing
}

func (status *StatusManager) findMachineConfigPoolsForLabel(mcPools []mcfgv1.MachineConfigPool, mcLabel labels.Set) ([]mcfgv1.MachineConfigPool, error) {
	var mcps []mcfgv1.MachineConfigPool
	for _, mcPool := range mcPools {
		mcSelector, err := metav1.LabelSelectorAsSelector(mcPool.Spec.MachineConfigSelector)
		if err != nil {
			return nil, fmt.Errorf("invalid machine config label selector in %s pool", mcPool.Name)
		}
		if mcSelector.Matches(mcLabel) {
			mcps = append(mcps, mcPool)
		}
	}
	return mcps, nil
}

func (status *StatusManager) networkMachineConfigs(role string) sets.Set[string] {
	if mcs, ok := status.availableMachineConfigs[role]; ok {
		return mcs
	}
	return nil
}

func (status *StatusManager) deletedNetworkMachineConfigs(role string) sets.Set[string] {
	if mcs, ok := status.deletedMachineConfigs[role]; ok {
		return mcs
	}
	return nil
}

func noNetworkMachineConfigs(masterMCs sets.Set[string], workerMCs sets.Set[string]) bool {
	return (masterMCs == nil || masterMCs.Len() == 0) && (workerMCs == nil || workerMCs.Len() == 0)
}
