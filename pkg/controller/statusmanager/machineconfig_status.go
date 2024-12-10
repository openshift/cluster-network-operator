package statusmanager

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/platform"
	mcutil "github.com/openshift/cluster-network-operator/pkg/util/machineconfig"
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
	for role, machineConfigs := range status.renderedMachineConfigs {
		progressingOrDegraded := status.updateNetworkStatus(mcPools, machineConfigs,
			map[string]string{platform.MachineConfigLabelRoleKey: role},
			mcutil.AreMachineConfigsRenderedOnPool)
		if progressingOrDegraded {
			return
		}
	}
	// Now it's time to check for status of deleted machine configs.
	deletedMachineConfigs, err := status.getLastDeletedMachineConfigState()
	if err != nil {
		klog.Errorf("failed to get deleted machine config state: %v", err)
		return
	}
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
		progressingOrDegraded := status.updateNetworkStatus(mcPools, machineConfigs,
			map[string]string{platform.MachineConfigLabelRoleKey: role},
			mcutil.AreMachineConfigsRemovedFromPool)
		if progressingOrDegraded {
			return
		}
		delete(deletedMachineConfigs, role)
	}
	if err := status.setLastDeletedMachineConfigState(deletedMachineConfigs); err != nil {
		klog.Errorf("failed to remove deleted machine config state: %v", err)
	}
}

func (status *StatusManager) updateNetworkStatus(mcPools []mcfgv1.MachineConfigPool, machineConfigs sets.Set[string],
	mcLabel labels.Set, test func(status mcfgv1.MachineConfigPoolStatus, machineConfigs sets.Set[string]) bool) bool {
	pools, err := status.findMachineConfigPoolsForLabel(mcPools, mcLabel)
	if err != nil {
		klog.Errorf("failed to get machine config pools for the label %s: %v", mcLabel, err)
	}
	degradedPool := status.isMachineConfigPoolDegraded(pools)
	if degradedPool != "" {
		status.setDegraded(MachineConfig, "MachineConfig", fmt.Sprintf("%s machine config pool in degraded state", degradedPool))
		return true
	}
	status.setNotDegraded(MachineConfig)

	progressingPool := status.isMachineConfigPoolProgressing(pools)
	if progressingPool != "" {
		status.setProgressing(MachineConfig, "MachineConfig", fmt.Sprintf("%s machine config pool in progressing state", progressingPool))
		return true
	}
	for _, pool := range pools {
		if pool.Spec.Paused {
			// When a machine config pool is in paused state, then it is expected that mco doesn't process any machine configs for the pool.
			// so if we report network status as progressing state then it blocks networking upgrade until machine config pool is changed
			// into unpaused state. so let's not consider the pool for reporting status.
			continue
		}
		rendered := test(pool.Status, machineConfigs)
		if !rendered {
			status.setProgressing(MachineConfig, "MachineConfig",
				fmt.Sprintf("%s machine config pool is still processing with network machine config", pool.Name))
			return true
		}
	}
	status.unsetProgressing(MachineConfig)
	return false
}

func (status *StatusManager) SetMachineConfigs(newRenderedMachineConfigs []mcfgv1.MachineConfig) {
	status.Lock()
	defer status.Unlock()
	// Create a map from newRenderedMachineConfigs.
	newlyRenderedMachineConfigMap := make(map[string]sets.Set[string])
	for _, mc := range newRenderedMachineConfigs {
		role, exists := mc.Labels[platform.MachineConfigLabelRoleKey]
		if !exists {
			klog.Warningf("machine config %s doesn't have %s label, skipping it", mc.Name, platform.MachineConfigLabelRoleKey)
			continue
		}
		if _, ok := newlyRenderedMachineConfigMap[role]; !ok {
			newlyRenderedMachineConfigMap[role] = sets.Set[string]{}
		}
		newlyRenderedMachineConfigMap[role].Insert(mc.Name)
	}
	// When new and existing rendered machine configs are same, then no changes. so return it now.
	if reflect.DeepEqual(newlyRenderedMachineConfigMap, status.renderedMachineConfigs) {
		return
	}
	// There are changes to rendered machine configs, so set network status to progressing state.
	status.setProgressing(MachineConfig, "MachineConfig", fmt.Sprintf("network operator machine config rendering in progress"))
	deletedMachineConfigMap, err := status.getLastDeletedMachineConfigState()
	if err != nil {
		klog.Errorf("failed to get deleted machine config state: %v", err)
		return
	}
	var needStateUpdate bool
	// If machine configs are not rendered now, then remove it from deleted machine configs.
	for role, renderedMachineConfigs := range status.renderedMachineConfigs {
		newlyDeletedMachineConfigs := renderedMachineConfigs.Difference(newlyRenderedMachineConfigMap[role])
		if len(newlyDeletedMachineConfigs) > 0 {
			needStateUpdate = true
			if _, ok := deletedMachineConfigMap[role]; !ok {
				deletedMachineConfigMap[role] = sets.Set[string]{}
			}
			deletedMachineConfigMap[role].Insert(newlyDeletedMachineConfigs.UnsortedList()...)
		}
	}
	// Replace existing rendered machine configs with newly rendered machine configs now.
	status.renderedMachineConfigs = newlyRenderedMachineConfigMap
	// When the same machine config is recreated even before it's deleted from pool, then remove it from
	// deleted machine configs.
	for role, deletedMachineConfigs := range deletedMachineConfigMap {
		recreatedMachineConfigs := status.renderedMachineConfigs[role].Intersection(deletedMachineConfigs)
		if len(recreatedMachineConfigs) > 0 {
			needStateUpdate = true
			deletedMachineConfigMap[role].Delete(recreatedMachineConfigs.UnsortedList()...)
		}
	}
	if !needStateUpdate {
		return
	}
	if err := status.setLastDeletedMachineConfigState(deletedMachineConfigMap); err != nil {
		klog.Errorf("failed to set deleted machine config state: %v", err)
	}
}

func (status *StatusManager) processCreatedMachineConfig(mc mcfgv1.MachineConfig) {
	// store machine config names into status.availableMachineConfigs.
	var mcRole string
	if role, exists := mc.Labels[platform.MachineConfigLabelRoleKey]; exists {
		mcRole = role
	} else {
		klog.Warningf("machine config %s doesn't have %s label, skipping it", mc.Name, platform.MachineConfigLabelRoleKey)
		return
	}
	status.Lock()
	defer status.Unlock()
	if _, ok := status.renderedMachineConfigs[mcRole]; !ok {
		status.renderedMachineConfigs[mcRole] = sets.Set[string]{}
	}
	status.renderedMachineConfigs[mcRole].Insert(mc.Name)

	// When the same machine config is recreated even before it's deleted from pool, then remove it from
	// deleted machine configs.
	deletedMachineConfigs, err := status.getLastDeletedMachineConfigState()
	if err != nil {
		klog.Errorf("failed to get deleted machine config state: %v", err)
		return
	}
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
	deletedMachineConfigs, err := status.getLastDeletedMachineConfigState()
	if err != nil {
		klog.Errorf("failed to get deleted machine config state: %v", err)
		return
	}
	var needStateUpdate bool
	for role, mcs := range status.renderedMachineConfigs {
		if !mcs.Has(mcName) {
			continue
		}
		status.renderedMachineConfigs[role].Delete(mcName)
		if status.renderedMachineConfigs[role].Len() == 0 {
			delete(status.renderedMachineConfigs, role)
		}
		if _, ok := deletedMachineConfigs[role]; !ok {
			deletedMachineConfigs[role] = sets.Set[string]{}
		}
		if !deletedMachineConfigs[role].Has(mcName) {
			deletedMachineConfigs[role].Insert(mcName)
			needStateUpdate = true
		}
	}
	if !needStateUpdate {
		return
	}
	if err := status.setLastDeletedMachineConfigState(deletedMachineConfigs); err != nil {
		klog.Errorf("failed to set deleted machine config state: %v", err)
	}
}

func (status *StatusManager) getLastDeletedMachineConfigState() (map[string]sets.Set[string], error) {
	deleteMachineConfigs := map[string]sets.Set[string]{}
	co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: status.name}}
	err := status.client.ClientFor("").CRClient().Get(context.TODO(), types.NamespacedName{Name: status.name}, co)
	if err != nil {
		return nil, err
	}
	lsbytes := co.Annotations[lastSeenMachineConfigAnnotation]
	if lsbytes == "" {
		return deleteMachineConfigs, nil
	}
	out := machineConfigState{}
	err = json.Unmarshal([]byte(lsbytes), &out)
	if err != nil {
		return nil, err
	}
	for _, mc := range out.MachineConfigDeleteProgressing {
		if _, ok := deleteMachineConfigs[mc.Role]; !ok {
			deleteMachineConfigs[mc.Role] = sets.Set[string]{}
		}
		deleteMachineConfigs[mc.Role].Insert(mc.Name)
	}
	return deleteMachineConfigs, nil
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

func (status *StatusManager) isMachineConfigPoolDegraded(pools []mcfgv1.MachineConfigPool) string {
	var degradedPool string
	for _, pool := range pools {
		if mcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolDegraded) {
			degradedPool = pool.Name
			break
		}
	}
	return degradedPool
}

func (status *StatusManager) isMachineConfigPoolProgressing(pools []mcfgv1.MachineConfigPool) string {
	var progressingPool string
	for _, pool := range pools {
		if mcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolUpdating) {
			progressingPool = pool.Name
			break
		}
	}
	return progressingPool
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
