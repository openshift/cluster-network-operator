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
	// renderedMachineConfigAnnotation - the annotation where we stash our rendered machine config state
	renderedMachineConfigAnnotation = "network.operator.openshift.io/rendered-machineconfig-state"
)

// machineConfigState contains network operator managed machine configs states.
type machineConfigState struct {
	RenderedMachineConfig []machineConfigInfo
}

// machineConfigInfo contains machine config info.
type machineConfigInfo struct {
	Name string
	Role string
}

func (status *StatusManager) SetMachineConfigs(ctx context.Context, newRenderedMachineConfigs []mcfgv1.MachineConfig) {
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
	status.Lock()
	var err error
	if status.renderedMachineConfigs == nil {
		status.renderedMachineConfigs, err = status.getLastRenderedMachineConfigState()
		if err != nil {
			status.Unlock()
			klog.Errorf("failed to get rendered machine config state: %v", err)
			return
		}
	}
	// When new and existing rendered machine configs are same, then no changes. so return it now.
	if reflect.DeepEqual(newlyRenderedMachineConfigMap, status.renderedMachineConfigs) {
		status.Unlock()
		return
	}
	renderedMachineConfigMap, err := status.getLastRenderedMachineConfigState()
	if err != nil {
		status.Unlock()
		klog.Errorf("failed to get rendered machine config state: %v", err)
		return
	}
	// Find out if any newly deleted machine configs and update status.machineConfigsBeingRemoved cache.
	machineConfigsBeingRemoved := make(map[string]sets.Set[string])
	for role, renderedMachineConfigs := range renderedMachineConfigMap {
		if _, ok := newlyRenderedMachineConfigMap[role]; !ok {
			machineConfigsBeingRemoved[role] = sets.Set[string]{}
			machineConfigsBeingRemoved[role].Insert(renderedMachineConfigs.UnsortedList()...)
		} else {
			deleted := renderedMachineConfigs.Difference(newlyRenderedMachineConfigMap[role])
			machineConfigsBeingRemoved[role].Insert(deleted.UnsortedList()...)
		}
	}
	if !reflect.DeepEqual(machineConfigsBeingRemoved, status.machineConfigsBeingRemoved) {
		status.machineConfigsBeingRemoved = machineConfigsBeingRemoved
	}
	var annotateUpdate bool
	// When there are new rendered machine configs, update the annotation cache.
	for role, newlyRenderedMachineConfigs := range newlyRenderedMachineConfigMap {
		if _, ok := renderedMachineConfigMap[role]; !ok {
			renderedMachineConfigMap[role] = sets.Set[string]{}
			renderedMachineConfigMap[role].Insert(newlyRenderedMachineConfigs.UnsortedList()...)
			annotateUpdate = true
		} else {
			new := newlyRenderedMachineConfigs.Difference(renderedMachineConfigMap[role])
			if len(new) == 0 {
				continue
			}
			renderedMachineConfigMap[role].Insert(new.UnsortedList()...)
			annotateUpdate = true
		}
	}
	if annotateUpdate {
		status.renderedMachineConfigs = renderedMachineConfigMap
		if err := status.setLastRenderedMachineConfigState(renderedMachineConfigMap); err != nil {
			klog.Errorf("failed to set rendered machine config state: %v", err)
		}
	}
	status.Unlock()
	mcPools := &mcfgv1.MachineConfigPoolList{}
	err = status.client.ClientFor("").CRClient().List(ctx, mcPools)
	if err != nil {
		klog.Errorf("failed to retrieve machine config pools: %v", err)
		return
	}
	status.SetFromMachineConfigPool(mcPools.Items)
}

func (status *StatusManager) SetFromMachineConfigPool(mcPools []mcfgv1.MachineConfigPool) {
	status.Lock()
	defer status.Unlock()
	// The status.renderedMachineConfigs is a non-nil map at the time when SetFromMachineConfigPool method is invoked.
	for role, machineConfigs := range status.renderedMachineConfigs {
		pools, err := status.findMachineConfigPoolsForLabel(mcPools, map[string]string{platform.MachineConfigLabelRoleKey: role})
		if err != nil {
			klog.Errorf("failed to get machine config pools for the role %s: %v", role, err)
		}
		degradedPool := status.isMachineConfigPoolDegraded(pools)
		if degradedPool != "" {
			status.setDegraded(MachineConfig, "MachineConfig", fmt.Sprintf("%s machine config pool in degraded state", degradedPool))
			return
		}
		status.setNotDegraded(MachineConfig)

		progressingPool := status.isMachineConfigPoolProgressing(pools)
		if progressingPool != "" {
			status.setProgressing(MachineConfig, "MachineConfig", fmt.Sprintf("%s machine config pool in progressing state", progressingPool))
			return
		}
		for _, pool := range pools {
			if pool.Spec.Paused {
				// When a machine config pool is in paused state, then it is expected that mco doesn't process any machine configs for the pool.
				// so if we report network status as progressing state then it blocks networking upgrade until machine config pool is changed
				// into unpaused state. so let's not consider the pool for reporting status.
				continue
			}
			for _, machineConfig := range machineConfigs.UnsortedList() {
				var rendered bool
				mcSet := sets.Set[string]{}
				mcSet.Insert(machineConfig)
				if mcsBeingRemoved, ok := status.machineConfigsBeingRemoved[role]; ok && mcsBeingRemoved.Has(machineConfig) {
					rendered = mcutil.AreMachineConfigsRemovedFromPool(pool.Status, mcSet)
					if rendered {
						status.machineConfigsBeingRemoved[role].Delete(machineConfig)
						status.renderedMachineConfigs[role].Delete(machineConfig)
						if err := status.setLastRenderedMachineConfigState(status.renderedMachineConfigs); err != nil {
							klog.Errorf("failed to update rendered machine config state: %v", err)
						}
					}
				} else {
					rendered = mcutil.AreMachineConfigsRenderedOnPool(pool.Status, mcSet)
				}
				if !rendered {
					status.setProgressing(MachineConfig, "MachineConfig",
						fmt.Sprintf("%s machine config pool is still processing %s machine config", pool.Name, machineConfig))
					return
				}
			}
		}
		status.unsetProgressing(MachineConfig)
	}
}

func (status *StatusManager) getLastRenderedMachineConfigState() (map[string]sets.Set[string], error) {
	renderedMachineConfigs := map[string]sets.Set[string]{}
	co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: status.name}}
	err := status.client.ClientFor("").CRClient().Get(context.TODO(), types.NamespacedName{Name: status.name}, co)
	if err != nil {
		return nil, err
	}
	lsbytes := co.Annotations[renderedMachineConfigAnnotation]
	if lsbytes == "" {
		return renderedMachineConfigs, nil
	}
	out := machineConfigState{}
	err = json.Unmarshal([]byte(lsbytes), &out)
	if err != nil {
		return nil, err
	}
	for _, mc := range out.RenderedMachineConfig {
		if _, ok := renderedMachineConfigs[mc.Role]; !ok {
			renderedMachineConfigs[mc.Role] = sets.Set[string]{}
		}
		renderedMachineConfigs[mc.Role].Insert(mc.Name)
	}
	return renderedMachineConfigs, nil
}

func (status *StatusManager) setLastRenderedMachineConfigState(renderedMachineConfigs map[string]sets.Set[string]) error {
	machineConfigState := machineConfigState{}
	for role, mcs := range renderedMachineConfigs {
		for _, mc := range mcs.UnsortedList() {
			machineConfigState.RenderedMachineConfig = append(machineConfigState.RenderedMachineConfig,
				machineConfigInfo{Name: mc, Role: role})
		}
	}
	lsbytes, err := json.Marshal(machineConfigState)
	if err != nil {
		return err
	}
	co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: status.name}}
	anno := string(lsbytes)
	return status.setAnnotation(context.TODO(), co, renderedMachineConfigAnnotation, &anno)
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
