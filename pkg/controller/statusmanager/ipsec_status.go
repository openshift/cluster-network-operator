package statusmanager

import (
	"fmt"
	"log"

	"github.com/openshift/cluster-network-operator/pkg/network"
	"github.com/openshift/cluster-network-operator/pkg/platform"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
)

func (status *StatusManager) SetFromMachineConfigs() {
	status.Lock()
	defer status.Unlock()

	master, err := status.ipsecMachineConfigExists(platform.MasterRoleMachineConfigLabel)
	if err != nil {
		log.Printf("failed to retrieve machine configs for master role: %v", err)
	}
	worker, err := status.ipsecMachineConfigExists(platform.WorkerRoleMachineConfigLabel)
	if err != nil {
		log.Printf("failed to retrieve machine configs for worker role: %v", err)
	}
	// Both master and worker role machine configs don't have ipsec plugin present, so return now.
	if !master && !worker {
		status.setNotDegraded(MachineConfig)
		return
	}
	var degraded bool
	if master {
		// IPsec machine config exists for master role, check its machine config pool and update network
		// operator degraded condition accordingly.
		degraded, err = status.isMachineConfigPoolDegraded(platform.MasterRoleMachineConfigLabel)
		if err != nil {
			log.Printf("failed to check machine config pools for master role: %v", err)
		}
		if degraded {
			status.setDegraded(MachineConfig, "IPsec", "master role machine config pool(s) in degraded state")
			return
		}
	}
	if worker {
		// IPsec machine config exists for worker role, check its machine config pool and update network
		// operator degraded condition accordingly.
		degraded, err = status.isMachineConfigPoolDegraded(platform.WorkerRoleMachineConfigLabel)
		if err != nil {
			log.Printf("failed to check machine config pools for worker role: %v", err)
		}
		if degraded {
			status.setDegraded(MachineConfig, "IPsec", "worker role machine config pool(s) in degraded state")
			return
		}
	}
	if !degraded {
		status.setNotDegraded(MachineConfig)
	}
}

func (status *StatusManager) isMachineConfigPoolDegraded(mcLabel labels.Set) (bool, error) {
	var degraded bool
	pools, err := status.findIPsecMachineConfigPoolsForLabel(mcLabel)
	if err != nil {
		return false, err
	}
	for _, pool := range pools {
		if mcfgv1.IsMachineConfigPoolConditionTrue(pool.Status.Conditions, mcfgv1.MachineConfigPoolDegraded) {
			degraded = true
			break
		}
	}
	return degraded, nil
}

func (status *StatusManager) findIPsecMachineConfigPoolsForLabel(mcLabel labels.Set) ([]*mcfgv1.MachineConfigPool, error) {
	var mcPools []*mcfgv1.MachineConfigPool
	pools, err := status.mcpLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}
	for i := range pools {
		mcSelector, err := metav1.LabelSelectorAsSelector(pools[i].Spec.MachineConfigSelector)
		if err != nil {
			return nil, fmt.Errorf("invalid machine config label selector in %s pool", pools[i].Name)
		}
		if mcSelector.Matches(mcLabel) {
			mcPools = append(mcPools, pools[i])
		}
	}
	return mcPools, nil
}

func (status *StatusManager) ipsecMachineConfigExists(mcLabel labels.Set) (bool, error) {
	mcs, err := status.mcLister.List(mcLabel.AsSelector())
	if err != nil {
		return false, err
	}
	exists := false
	for _, mc := range mcs {
		if network.ContainsNetworkOwnerRef(mc.OwnerReferences) && sets.New(mc.Spec.Extensions...).Has("ipsec") {
			exists = true
			break
		}
	}
	return exists, nil
}
