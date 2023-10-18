package infrastructureconfig

import (
	configv1 "github.com/openshift/api/config/v1"
	"log"
)

type specStatusSynchronizer interface {
	SpecStatusSynchronize(*configv1.Infrastructure) *configv1.Infrastructure
}

type specStatusSunchronizer struct{}

func (*specStatusSunchronizer) SpecStatusSynchronize(infraConfig *configv1.Infrastructure) *configv1.Infrastructure {
	updatedInfraConfig := infraConfig.DeepCopy()

	var (
		statusApiVips, specApiVips                 *[]string
		statusIngressVips, specIngressVips         *[]string
		statusMachineNetworks, specMachineNetworks *[]string
	)

	switch {
	case updatedInfraConfig.Status.Platform == configv1.BareMetalPlatformType:
		statusApiVips = &updatedInfraConfig.Status.PlatformStatus.BareMetal.APIServerInternalIPs
		specApiVips = &updatedInfraConfig.Spec.PlatformSpec.BareMetal.APIServerInternalIPs

		statusIngressVips = &updatedInfraConfig.Status.PlatformStatus.BareMetal.IngressIPs
		specIngressVips = &updatedInfraConfig.Spec.PlatformSpec.BareMetal.IngressIPs

		statusMachineNetworks = &updatedInfraConfig.Status.PlatformStatus.BareMetal.MachineNetworks
		specMachineNetworks = &updatedInfraConfig.Spec.PlatformSpec.BareMetal.MachineNetworks

	// TODO(chocobomb) Add case statements for vSphere and OpenStack
	default:
		// nothing to do for this platform type
		return updatedInfraConfig
	}

	// TODO(chocobomb) Decide on the failure strategy if validation fails. Should we set InfraConfig as degraded or only print warning in the log?
	if err := syncMachineNetworks(specMachineNetworks, statusMachineNetworks); err != nil {
		log.Printf("Warning on syncing machine networks: %v", err) // this is only a warning -> continue
	}

	if err := syncVips(specApiVips, statusApiVips, *statusMachineNetworks); err != nil {
		log.Printf("Warning on syncing API VIPs: %v", err) // this is only a warning -> continue
	}

	if err := syncVips(specIngressVips, statusIngressVips, *statusMachineNetworks); err != nil {
		log.Printf("Warning on syncing Ingress VIPs: %v", err) // this is only a warning -> continue
	}

	return updatedInfraConfig
}

func syncMachineNetworks(spec, status *[]string) error {
	// `spec` is empty, `status` with value: copy status to spec
	if len(*spec) == 0 {
		*spec = *status
	} else {
		// TODO(chocobomb) Add some validations here
		// `spec` is updated, need to propagate to `status`
		*status = *spec
	}

	return nil
}

func syncVips(spec, status *[]string, machineNetworks []string) error {
	// `spec` is empty, `status` with value: copy status to spec
	if len(*spec) == 0 {
		*spec = *status
	} else {
		// TODO(chocobomb) Add some validations here. Remember to validate if VIP inside MachineNetwork
		// `spec` is updated, need to propagate to `status`
		*status = *spec
	}

	return nil
}
