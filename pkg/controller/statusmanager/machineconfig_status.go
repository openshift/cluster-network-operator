package statusmanager

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/cluster-network-operator/pkg/names"
	mcutil "github.com/openshift/cluster-network-operator/pkg/util/machineconfig"
	mcomcfgv1 "github.com/openshift/machine-config-operator/pkg/apihelpers"
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

// SetMachineConfigs takes up newly rendered machine configs and updates status manager and annotation caches
// accordingly. It also invokes SetFromMachineConfigPool function to update network status for newly rendered
// (or) removed machine configs.
func (status *StatusManager) SetMachineConfigs(ctx context.Context, newRenderedMachineConfigs []mcfgv1.MachineConfig) error {
	// Create a map from newRenderedMachineConfigs.
	newlyRenderedMachineConfigMap := make(map[string]sets.Set[string])
	for _, mc := range newRenderedMachineConfigs {
		role, exists := mc.Labels[names.MachineConfigLabelRoleKey]
		if !exists {
			klog.Warningf("machine config %s doesn't have %s label, skipping it", mc.Name, names.MachineConfigLabelRoleKey)
			continue
		}
		if _, ok := newlyRenderedMachineConfigMap[role]; !ok {
			newlyRenderedMachineConfigMap[role] = sets.Set[string]{}
		}
		newlyRenderedMachineConfigMap[role].Insert(mc.Name)
	}
	needsReconcile, err := func() (bool, error) {
		status.Lock()
		defer status.Unlock()
		// When new and existing rendered machine configs are same, then no changes. so return it now.
		if reflect.DeepEqual(newlyRenderedMachineConfigMap, status.renderedMachineConfigs) {
			return false, nil
		}
		// Find out if any newly deleted machine configs and update status.machineConfigsBeingRemoved cache.
		machineConfigsBeingRemoved := make(map[string]sets.Set[string])
		for role, renderedMachineConfigs := range status.renderedMachineConfigs {
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
			if _, ok := status.renderedMachineConfigs[role]; !ok {
				status.renderedMachineConfigs[role] = sets.Set[string]{}
				status.renderedMachineConfigs[role].Insert(newlyRenderedMachineConfigs.UnsortedList()...)
				annotateUpdate = true
			} else {
				new := newlyRenderedMachineConfigs.Difference(status.renderedMachineConfigs[role])
				if len(new) == 0 {
					continue
				}
				status.renderedMachineConfigs[role].Insert(new.UnsortedList()...)
				annotateUpdate = true
			}
		}
		if annotateUpdate {
			if err := status.setLastRenderedMachineConfigState(status.renderedMachineConfigs); err != nil {
				return false, fmt.Errorf("failed to set rendered machine config state: %v", err)
			}
		}
		return true, nil
	}()
	// When reconcile is not needed (or) an error returned from above
	// inline function, return it now.
	if !needsReconcile || err != nil {
		return err
	}
	mcPools := &mcfgv1.MachineConfigPoolList{}
	err = status.client.ClientFor("").CRClient().List(ctx, mcPools)
	if err != nil {
		return fmt.Errorf("failed to retrieve machine config pools: %v", err)
	}
	return status.SetFromMachineConfigPool(mcPools.Items)
}

// SetFromMachineConfigPool reconcile loop being executed when CNO rendering pipeline renders a new
// machine config and reconcile of MachineConfig and MachineConfigPool events.
// 1. For a newly rendered machine config on particular role, Ensure appropriate machine config pools
// are updated with the machine config.
// 2. When machine config is removed for a particular role, Ensure machine config are removed from the
// appropriate machine config pools.
// While checking (1) and (2), If any one of those machine config pool is in progressing or degraded state,
// reflect that into network status.
// Note that when machine config is removed, nodes are rebooted, network operator pod recreated on a different
// node, CNO rendering pipeline is no longer nothing to do with that deleted machine config earlier, so cache
// in the status manager are rebuilt from annotation cache, so delete machine config entry from annotation cache
// when machine config is actually removed from machine config pool(s). This makes the status manager cache
// always up to date.
func (status *StatusManager) SetFromMachineConfigPool(mcPools []mcfgv1.MachineConfigPool) error {
	status.Lock()
	defer status.Unlock()
	// The status.renderedMachineConfigs is a non-nil map at the time when SetFromMachineConfigPool method is invoked.
	for role, machineConfigs := range status.renderedMachineConfigs {
		pools, err := status.findMachineConfigPoolsForLabel(mcPools, map[string]string{names.MachineConfigLabelRoleKey: role})
		if err != nil {
			klog.Errorf("failed to get machine config pools for the role %s: %v", role, err)
		}
		degradedPool := status.isAnyMachineConfigPoolDegraded(pools)
		if degradedPool != "" {
			status.setDegraded(MachineConfig, "MachineConfig", fmt.Sprintf("%s machine config pool in degraded state", degradedPool))
			return nil
		}
		status.setNotDegraded(MachineConfig)

		progressingPool := status.isAnyMachineConfigPoolProgressing(pools)
		if progressingPool != "" {
			status.setProgressing(MachineConfig, "MachineConfig", fmt.Sprintf("%s machine config pool in progressing state", progressingPool))
			return nil
		}
		for _, pool := range pools {
			if pool.Spec.Paused {
				// When a machine config pool is in paused state, then it is expected that mco doesn't process any machine configs for the pool.
				// so if we report network status as progressing state then it blocks networking upgrade until machine config pool is changed
				// into unpaused state. so let's not consider the pool for reporting status.
				continue
			}
			for _, machineConfig := range machineConfigs.UnsortedList() {
				added := true
				removed := true
				mcSet := sets.Set[string]{}
				mcSet.Insert(machineConfig)
				if mcsBeingRemoved, ok := status.machineConfigsBeingRemoved[role]; ok && mcsBeingRemoved.Has(machineConfig) {
					removed = mcutil.AreMachineConfigsRemovedFromPool(pool.Status, mcSet)
					if removed {
						status.machineConfigsBeingRemoved[role].Delete(machineConfig)
						// Delete map entry from status cache if role doesn't have machine configs. By deleting the entry,
						// there won't be any unnecessary processing of pools in the reconcile loop when it's not dealing
						// with network operator machine configs anymore.
						if status.machineConfigsBeingRemoved[role].Len() == 0 {
							delete(status.machineConfigsBeingRemoved, role)
						}
						status.renderedMachineConfigs[role].Delete(machineConfig)
						if status.renderedMachineConfigs[role].Len() == 0 {
							delete(status.renderedMachineConfigs, role)
						}
						if err := status.setLastRenderedMachineConfigState(status.renderedMachineConfigs); err != nil {
							return fmt.Errorf("failed to update rendered machine config state: %v", err)
						}
					}
				} else {
					added = mcutil.AreMachineConfigsRenderedOnPool(pool.Status, mcSet)
				}
				if !added || !removed {
					status.setProgressing(MachineConfig, "MachineConfig",
						fmt.Sprintf("%s machine config pool is still processing %s machine config", pool.Name, machineConfig))
					return nil
				}
			}
		}
		status.unsetProgressing(MachineConfig)
	}
	return nil
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

func (status *StatusManager) isAnyMachineConfigPoolDegraded(pools []mcfgv1.MachineConfigPool) string {
	var degradedPool string
	for _, pool := range pools {
		if mcomcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolDegraded) {
			degradedPool = pool.Name
			break
		}
	}
	return degradedPool
}

func (status *StatusManager) isAnyMachineConfigPoolProgressing(pools []mcfgv1.MachineConfigPool) string {
	var progressingPool string
	for _, pool := range pools {
		if mcomcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolUpdating) {
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
