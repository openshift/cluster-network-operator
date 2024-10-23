package statusmanager

import (
	"context"
	"fmt"
	"log"

	"github.com/openshift/cluster-network-operator/pkg/network"
	"github.com/openshift/cluster-network-operator/pkg/platform"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (status *StatusManager) SetFromMachineConfigs(ctx context.Context) {
	status.Lock()
	defer status.Unlock()

	masterMachineConfigs, err := status.networkMachineConfigs(ctx, platform.MasterRoleMachineConfigLabel)
	if err != nil {
		log.Printf("failed to retrieve machine configs for master role: %v", err)
	}
	workerMachineConfigs, err := status.networkMachineConfigs(ctx, platform.WorkerRoleMachineConfigLabel)
	if err != nil {
		log.Printf("failed to retrieve machine configs for worker role: %v", err)
	}
	// When both master and worker roles don't have any machine configs owned by network operator, then return now.
	if len(masterMachineConfigs) == 0 && len(workerMachineConfigs) == 0 {
		status.setNotDegraded(MachineConfig)
		status.unsetProgressing(MachineConfig)
		return
	}

	if len(masterMachineConfigs) > 0 {
		// Network operator owned machine configs exist for master role, check its machine config pool and update network
		// operator degraded and progressing conditions accordingly.
		pools, err := status.findMachineConfigPoolsForLabel(ctx, platform.MasterRoleMachineConfigLabel)
		if err != nil {
			log.Printf("failed to get machine config pools for the label %s: %v", platform.MasterRoleMachineConfigLabel, err)
		}
		degraded := status.isMachineConfigPoolDegraded(pools)
		if degraded {
			status.setDegraded(MachineConfig, "MachineConfig", "master role machine config pool(s) in degraded state")
			return
		}
		status.setNotDegraded(MachineConfig)
		progressing := status.isMachineConfigPoolProgressing(pools)
		if progressing {
			status.setProgressing(MachineConfig, "MachineConfig", "master role machine config pool(s) in progressing state")
			return
		}
		for _, pool := range pools {
			rendered := network.AreMachineConfigsRenderedOnPool(pool.Status, masterMachineConfigs)
			if !rendered {
				status.setProgressing(MachineConfig, "MachineConfig",
					"master role machine config pool(s) still rendering with network operator owned machine configs")
				return
			}
		}
		status.unsetProgressing(MachineConfig)
	}
	if len(workerMachineConfigs) > 0 {
		// Network operator owned machine configs exist for worker role, check its machine config pool and update network
		// operator degraded and progressing conditions accordingly.
		pools, err := status.findMachineConfigPoolsForLabel(ctx, platform.WorkerRoleMachineConfigLabel)
		if err != nil {
			log.Printf("failed to get machine config pools for the label %s: %v", platform.WorkerRoleMachineConfigLabel, err)
		}
		degraded := status.isMachineConfigPoolDegraded(pools)
		if degraded {
			status.setDegraded(MachineConfig, "MachineConfig", "worker role machine config pool(s) in degraded state")
			return
		}
		status.setNotDegraded(MachineConfig)

		progressing := status.isMachineConfigPoolProgressing(pools)
		if progressing {
			status.setProgressing(MachineConfig, "MachineConfig", "worker role machine config pool(s) in progressing state")
			return
		}
		for _, pool := range pools {
			rendered := network.AreMachineConfigsRenderedOnPool(pool.Status, workerMachineConfigs)
			if !rendered {
				status.setProgressing(MachineConfig, "MachineConfig",
					"worker role machine config pool(s) still rendering with network operator owned machine configs")
				return
			}
		}
		status.unsetProgressing(MachineConfig)
	}
}

func (status *StatusManager) isMachineConfigPoolDegraded(pools []*mcfgv1.MachineConfigPool) bool {
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

func (status *StatusManager) isMachineConfigPoolProgressing(pools []*mcfgv1.MachineConfigPool) bool {
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

func (status *StatusManager) findMachineConfigPoolsForLabel(ctx context.Context, mcLabel labels.Set) ([]*mcfgv1.MachineConfigPool, error) {
	mcPoolList := &mcfgv1.MachineConfigPoolList{}
	err := status.client.Default().CRClient().List(ctx, mcPoolList, &client.ListOptions{})
	if err != nil {
		return nil, err
	}
	var mcPools []*mcfgv1.MachineConfigPool
	for i := range mcPoolList.Items {
		mcSelector, err := metav1.LabelSelectorAsSelector(mcPoolList.Items[i].Spec.MachineConfigSelector)
		if err != nil {
			return nil, fmt.Errorf("invalid machine config label selector in %s pool", mcPoolList.Items[i].Name)
		}
		if mcSelector.Matches(mcLabel) {
			mcPools = append(mcPools, &mcPoolList.Items[i])
		}
	}
	return mcPools, nil
}

func (status *StatusManager) networkMachineConfigs(ctx context.Context, mcLabel labels.Set) ([]*mcfgv1.MachineConfig, error) {
	mcs := &mcfgv1.MachineConfigList{}
	err := status.client.Default().CRClient().List(ctx, mcs, &client.ListOptions{LabelSelector: mcLabel.AsSelector()})
	if err != nil {
		return nil, err
	}
	var machineConfigs []*mcfgv1.MachineConfig
	for i := range mcs.Items {
		if network.ContainsNetworkOwnerRef(mcs.Items[i].OwnerReferences) {
			machineConfigs = append(machineConfigs, &mcs.Items[i])
		}
	}
	return machineConfigs, nil
}
