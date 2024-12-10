package machineconfig

import (
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

var (
	// When user deploys their own machine config for installing and configuring specific version of libreswan, then
	// corresponding master and worker role machine configs annotation must have `user-ipsec-machine-config: true`.
	// When CNO finds machine configs with the annotation, then it skips rendering its own IPsec machine configs
	// and reuse already deployed user machine configs for the ovn-ipsec-host daemonset.
	UserDefinedIPsecMachineConfigAnnotation = map[string]string{"user-ipsec-machine-config": "true"}
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
	return isSubset(machineConfig.Annotations, UserDefinedIPsecMachineConfigAnnotation)
}

// AreMachineConfigsRenderedOnPool returns true if machineConfigs are completely rendered on the given machine config
// pool status, otherwise returns false.
func AreMachineConfigsRenderedOnPool(status mcfgv1.MachineConfigPoolStatus, machineConfigs sets.Set[string]) bool {
	checkSource := func(sourceNames sets.Set[string], machineConfigs sets.Set[string]) bool {
		return sourceNames.IsSuperset(machineConfigs)
	}
	return status.MachineCount == status.UpdatedMachineCount &&
		checkSourceInMachineConfigPoolStatus(status, machineConfigs, checkSource)
}

// AreMachineConfigsRemovedFromPool returns true if machineConfigs are completely removed on the given machine config
// pool status, otherwise returns false.
func AreMachineConfigsRemovedFromPool(status mcfgv1.MachineConfigPoolStatus, machineConfigs sets.Set[string]) bool {
	checkSource := func(sourceNames sets.Set[string], machineConfigs sets.Set[string]) bool {
		return !sourceNames.HasAny(machineConfigs.UnsortedList()...)
	}
	return status.MachineCount == status.UpdatedMachineCount &&
		checkSourceInMachineConfigPoolStatus(status, machineConfigs, checkSource)
}

func checkSourceInMachineConfigPoolStatus(machineConfigStatus mcfgv1.MachineConfigPoolStatus, machineConfigs sets.Set[string],
	test func(sets.Set[string], sets.Set[string]) bool) bool {
	sourceNames := sets.New[string]()
	for _, source := range machineConfigStatus.Configuration.Source {
		sourceNames.Insert(source.Name)
	}
	return test(sourceNames, machineConfigs)
}
