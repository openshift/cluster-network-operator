package machineconfig

import (
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"k8s.io/apimachinery/pkg/util/sets"
)

// IsUserDefinedIPsecMachineConfig return true if machine config's annotation is set with
// `user-ipsec-machine-config: true`, otherwise returns false.
func IsUserDefinedIPsecMachineConfig(machineConfig *mcfgv1.MachineConfig) bool {
	if machineConfig == nil {
		return false
	}
	isSubset := func(mcAnnotations, ipsecAnnotation map[string]string) bool {
		for ipsecKey, ipsecValue := range ipsecAnnotation {
			if mcAnnotationValue, ok := mcAnnotations[ipsecKey]; !ok || mcAnnotationValue != ipsecValue {
				return false
			}
		}
		return true
	}
	return isSubset(machineConfig.Annotations, names.UserDefinedIPsecMachineConfigAnnotation())
}

// AreMachineConfigsRenderedOnPool returns true if machineConfigs are completely rendered on the given machine config
// pool status, otherwise returns false.
func AreMachineConfigsRenderedOnPool(status mcfgv1.MachineConfigPoolStatus, machineConfigs sets.Set[string]) bool {
	return status.MachineCount == status.UpdatedMachineCount &&
		AreMachineConfigsRenderedOnPoolSource(status, machineConfigs)
}

// AreMachineConfigsRemovedFromPool returns true if machineConfigs are completely removed on the given machine config
// pool status, otherwise returns false.
func AreMachineConfigsRemovedFromPool(status mcfgv1.MachineConfigPoolStatus, machineConfigs sets.Set[string]) bool {
	return status.MachineCount == status.UpdatedMachineCount &&
		AreMachineConfigsRemovedFromPoolSource(status, machineConfigs)
}

// AreMachineConfigsRenderedOnPoolSource returns true if machineConfigs are present in the pool's rendered source list.
func AreMachineConfigsRenderedOnPoolSource(status mcfgv1.MachineConfigPoolStatus, machineConfigs sets.Set[string]) bool {
	checkSource := func(sourceNames sets.Set[string], machineConfigs sets.Set[string]) bool {
		return sourceNames.IsSuperset(machineConfigs)
	}
	return checkSourceInMachineConfigPoolStatus(status, machineConfigs, checkSource)
}

// AreMachineConfigsRemovedFromPoolSource returns true if machineConfigs are absent from the pool's rendered source list.
func AreMachineConfigsRemovedFromPoolSource(status mcfgv1.MachineConfigPoolStatus, machineConfigs sets.Set[string]) bool {
	checkSource := func(sourceNames sets.Set[string], machineConfigs sets.Set[string]) bool {
		return !sourceNames.HasAny(machineConfigs.UnsortedList()...)
	}
	return checkSourceInMachineConfigPoolStatus(status, machineConfigs, checkSource)
}

func checkSourceInMachineConfigPoolStatus(machineConfigStatus mcfgv1.MachineConfigPoolStatus, machineConfigs sets.Set[string],
	test func(sets.Set[string], sets.Set[string]) bool) bool {
	sourceNames := sets.New[string]()
	for _, source := range machineConfigStatus.Configuration.Source {
		sourceNames.Insert(source.Name)
	}
	return test(sourceNames, machineConfigs)
}
