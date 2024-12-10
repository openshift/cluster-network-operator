package statusmanager

import (
	"context"
	"encoding/json"
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/platform"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
)

const (
	// lastSeenMachineConfigAnnotation - the annotation where we stash our machine config state
	lastSeenMachineConfigAnnotation = "network.operator.openshift.io/last-seen-machineconfig-state"
)

// machineConfigState contains network operator managed machine configs states.
type machineConfigState struct {
	MachineConfigDeleteProgressing []machineConfigInfo
}

// machineConfigInfo contains machine config info.
type machineConfigInfo struct {
	Name string
	Role string
}

func (status *StatusManager) SetFromMachineConfigPool(mcPools []mcfgv1.MachineConfigPool) {
	status.Lock()
	defer status.Unlock()
	// When network operator owned machine configs exist for master or worker role, then check
	// its machine config pools and update network operator degraded and progressing conditions
	// accordingly.
	for role, machineConfigs := range status.availableMachineConfigs {
		done := status.updateNetworkStatus(mcPools, role, machineConfigs,
			map[string]string{platform.MachineConfigLabelRoleKey: role},
			platform.AreMachineConfigsRenderedOnPool)
		if !done {
			return
		}
	}
	// Now it's time to check for status of deleted machine configs.
	deletedMachineConfigs := status.getLastDeletedMachineConfigState()
	// If no network MCs present in deletedMachineConfigs map, so it's ok to
	// return now with success.
	if len(deletedMachineConfigs) == 0 {
		status.setNotDegraded(MachineConfig)
		status.unsetProgressing(MachineConfig)
		return
	}
	// When Network operator owned machine configs are still getting removed from the pool,
	// then update network operator degraded and progressing conditions accordingly.
	for role, machineConfigs := range deletedMachineConfigs {
		done := status.updateNetworkStatus(mcPools, role, machineConfigs,
			map[string]string{platform.MachineConfigLabelRoleKey: role},
			platform.AreMachineConfigsRemovedFromPool)
		if !done {
			return
		}
		delete(deletedMachineConfigs, role)
	}
	if err := status.setLastDeletedMachineConfigState(deletedMachineConfigs); err != nil {
		klog.Errorf("failed to remove deleted machine config state: %v", err)
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

	// When the same machine config is recreated even before it's deleted from pool, then remove it from
	// deleted machine configs.
	deletedMachineConfigs := status.getLastDeletedMachineConfigState()
	if _, ok := deletedMachineConfigs[mcRole]; ok {
		if deletedMachineConfigs[mcRole].Has(mc.Name) {
			deletedMachineConfigs[mcRole].Delete(mc.Name)
			if err := status.setLastDeletedMachineConfigState(deletedMachineConfigs); err != nil {
				klog.Errorf("failed to update deleted machine config state: %v", err)
			}
		}
	}
}

func (status *StatusManager) processDeletedMachineConfig(mcName string) {
	status.Lock()
	defer status.Unlock()
	deletedMachineConfigs := status.getLastDeletedMachineConfigState()
	for role, mcs := range status.availableMachineConfigs {
		if !mcs.Has(mcName) {
			continue
		}
		status.availableMachineConfigs[role].Delete(mcName)
		if status.availableMachineConfigs[role].Len() == 0 {
			delete(status.availableMachineConfigs, role)
		}
		if _, ok := deletedMachineConfigs[role]; !ok {
			deletedMachineConfigs[role] = sets.Set[string]{}
		}
		deletedMachineConfigs[role].Insert(mcName)
	}
	if err := status.setLastDeletedMachineConfigState(deletedMachineConfigs); err != nil {
		klog.Errorf("failed to set deleted machine config state: %v", err)
	}
}

func (status *StatusManager) getLastDeletedMachineConfigState() map[string]sets.Set[string] {
	deleteMachineConfigs := map[string]sets.Set[string]{}
	co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: status.name}}
	err := status.client.ClientFor("").CRClient().Get(context.TODO(), types.NamespacedName{Name: status.name}, co)
	if err != nil {
		klog.Errorf("failed to get ClusterOperator: %v", err)
		return deleteMachineConfigs
	}
	lsbytes := co.Annotations[lastSeenMachineConfigAnnotation]
	if lsbytes == "" {
		return deleteMachineConfigs
	}
	out := machineConfigState{}
	err = json.Unmarshal([]byte(lsbytes), &out)
	if err != nil {
		// No need to return error; just move on
		klog.Errorf("failed to unmashal last-seen-mc-state: %v", err)
		return deleteMachineConfigs
	}
	for _, mc := range out.MachineConfigDeleteProgressing {
		if _, ok := deleteMachineConfigs[mc.Role]; !ok {
			deleteMachineConfigs[mc.Role] = sets.Set[string]{}
		}
		deleteMachineConfigs[mc.Role].Insert(mc.Name)
	}
	return deleteMachineConfigs
}

func (status *StatusManager) setLastDeletedMachineConfigState(deletedMachineConfigs map[string]sets.Set[string]) error {
	machineConfigState := machineConfigState{}
	for role, mcs := range deletedMachineConfigs {
		for _, mc := range mcs.UnsortedList() {
			machineConfigState.MachineConfigDeleteProgressing = append(machineConfigState.MachineConfigDeleteProgressing,
				machineConfigInfo{Name: mc, Role: role})
		}
	}
	lsbytes, err := json.Marshal(machineConfigState)
	if err != nil {
		return err
	}
	co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: status.name}}
	anno := string(lsbytes)
	return status.setAnnotation(context.TODO(), co, lastSeenMachineConfigAnnotation, &anno)
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
