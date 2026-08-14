package infrastructureconfig

import (
	"fmt"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/util/ip"
	"log"
)

func (*synchronizer) SpecStatusSynchronize(infraConfig *configv1.Infrastructure) (*configv1.Infrastructure, error) {
	updatedInfraConfig := infraConfig.DeepCopy()

	var (
		statusApiVips, statusIngressVips           *[]string
		specApiVips, specIngressVips               *[]configv1.IP
		statusMachineNetworks, specMachineNetworks *[]configv1.CIDR
		elb                                        bool
	)

	if updatedInfraConfig.Status.PlatformStatus == nil {
		// This should not happen because of guards at openshift/installer but if by any chance we
		// arrive at this situation, just exit to avoid errors or panics
		return updatedInfraConfig, nil
	}

	switch {
	case updatedInfraConfig.Status.PlatformStatus.Type == configv1.BareMetalPlatformType:
		if updatedInfraConfig.Status.PlatformStatus.BareMetal == nil {
			// This is a safeguard against strange platform combinations that do not fill the
			// PlatformStatus (e.g. UPI under some circumstances).
			log.Print("Detected nil platform status for baremetal, aborting")
			return updatedInfraConfig, nil
		}
		if updatedInfraConfig.Spec.PlatformSpec.BareMetal == nil {
			// This is migration path from pre-4.16 version in which PlatformSpec was not defined.
			// We first upgrade the struct to a new one (introduced in 4.16) and populate afterward.
			log.Print("Detected nil platform spec for baremetal, initializing")
			updatedInfraConfig.Spec.PlatformSpec.BareMetal = &configv1.BareMetalPlatformSpec{}
		}
		statusApiVips = &updatedInfraConfig.Status.PlatformStatus.BareMetal.APIServerInternalIPs
		specApiVips = &updatedInfraConfig.Spec.PlatformSpec.BareMetal.APIServerInternalIPs

		statusIngressVips = &updatedInfraConfig.Status.PlatformStatus.BareMetal.IngressIPs
		specIngressVips = &updatedInfraConfig.Spec.PlatformSpec.BareMetal.IngressIPs

		statusMachineNetworks = &updatedInfraConfig.Status.PlatformStatus.BareMetal.MachineNetworks
		specMachineNetworks = &updatedInfraConfig.Spec.PlatformSpec.BareMetal.MachineNetworks

		if updatedInfraConfig.Status.PlatformStatus.BareMetal.LoadBalancer != nil &&
			updatedInfraConfig.Status.PlatformStatus.BareMetal.LoadBalancer.Type == configv1.LoadBalancerTypeUserManaged {
			elb = true
		}

	case updatedInfraConfig.Status.PlatformStatus.Type == configv1.VSpherePlatformType:
		// vSphere UPI is a special type of platform that behaves differently than any other UPI.
		// It sets the type, but does not populate Spec and Status fields, leaving them as `nil`.
		// We need to keep it like that so that the API validations pass correctly (as empty struct
		// serializes differently than `nil`).
		if updatedInfraConfig.Status.PlatformStatus.VSphere == nil {
			log.Print("Detected nil platform status for vSphere, aborting")
			return updatedInfraConfig, nil
		}
		if updatedInfraConfig.Spec.PlatformSpec.VSphere == nil {
			log.Print("Detected nil platform spec for vSphere, initializing")
			updatedInfraConfig.Spec.PlatformSpec.VSphere = &configv1.VSpherePlatformSpec{}
		}
		statusApiVips = &updatedInfraConfig.Status.PlatformStatus.VSphere.APIServerInternalIPs
		specApiVips = &updatedInfraConfig.Spec.PlatformSpec.VSphere.APIServerInternalIPs

		statusIngressVips = &updatedInfraConfig.Status.PlatformStatus.VSphere.IngressIPs
		specIngressVips = &updatedInfraConfig.Spec.PlatformSpec.VSphere.IngressIPs

		statusMachineNetworks = &updatedInfraConfig.Status.PlatformStatus.VSphere.MachineNetworks
		specMachineNetworks = &updatedInfraConfig.Spec.PlatformSpec.VSphere.MachineNetworks

		if updatedInfraConfig.Status.PlatformStatus.VSphere.LoadBalancer != nil &&
			updatedInfraConfig.Status.PlatformStatus.VSphere.LoadBalancer.Type == configv1.LoadBalancerTypeUserManaged {
			elb = true
		}

	case updatedInfraConfig.Status.PlatformStatus.Type == configv1.OpenStackPlatformType:
		if updatedInfraConfig.Status.PlatformStatus.OpenStack == nil {
			log.Print("Detected nil platformstatus for OpenStack, aborting")
			return updatedInfraConfig, nil
		}
		if updatedInfraConfig.Spec.PlatformSpec.OpenStack == nil {
			log.Print("Detected nil platform spec for openstack, initializing")
			updatedInfraConfig.Spec.PlatformSpec.OpenStack = &configv1.OpenStackPlatformSpec{}
		}
		statusApiVips = &updatedInfraConfig.Status.PlatformStatus.OpenStack.APIServerInternalIPs
		specApiVips = &updatedInfraConfig.Spec.PlatformSpec.OpenStack.APIServerInternalIPs

		statusIngressVips = &updatedInfraConfig.Status.PlatformStatus.OpenStack.IngressIPs
		specIngressVips = &updatedInfraConfig.Spec.PlatformSpec.OpenStack.IngressIPs

		statusMachineNetworks = &updatedInfraConfig.Status.PlatformStatus.OpenStack.MachineNetworks
		specMachineNetworks = &updatedInfraConfig.Spec.PlatformSpec.OpenStack.MachineNetworks

		if updatedInfraConfig.Status.PlatformStatus.OpenStack.LoadBalancer != nil &&
			updatedInfraConfig.Status.PlatformStatus.OpenStack.LoadBalancer.Type == configv1.LoadBalancerTypeUserManaged {
			elb = true
		}

	default:
		// nothing to do for this platform type
		return updatedInfraConfig, nil
	}

	if err := syncMachineNetworks(specMachineNetworks, statusMachineNetworks); err != nil {
		return nil, fmt.Errorf("Error on syncing machine networks: %v", err)
	}
	if err := validateVipsWithVips(*specApiVips, *specIngressVips, elb); err != nil {
		return nil, fmt.Errorf("Error on validating VIPs: %v", err)
	}
	if err := validateVipsWithMachineNetworks(*specApiVips, *specMachineNetworks); err != nil {
		return nil, fmt.Errorf("Error on validating API VIPs and Machine Networks: %v", err)
	}
	if err := validateVipsWithMachineNetworks(*specIngressVips, *specMachineNetworks); err != nil {
		return nil, fmt.Errorf("Error on validating Ingress VIPs and Machine Networks: %v", err)
	}

	if err := syncVips(specApiVips, statusApiVips); err != nil {
		return nil, fmt.Errorf("Error on syncing API VIPs: %v", err)
	}
	if err := syncVips(specIngressVips, statusIngressVips); err != nil {
		return nil, fmt.Errorf("Error on syncing Ingress VIPs: %v", err)
	}

	return updatedInfraConfig, nil
}

// syncMachineNetworks propagates machine networks from Spec to Status assuming they pass required validations.
// In order to be valid, machine networks need to fulfil the following spec:
//
//   - the first machine network can never be modified
//     -- the only exception is when Spec is empty, then adding is allowed
//   - the number of machine networks is not limited to 2 as opposed to e.g. service networks
//   - removal of machine networks is allowed as long as first entry is not modified
//
// It is possible that Status and/or Spec are empty if this is the very first time we modify this field
// after upgrading from the version of the API that did not contain the field.
func syncMachineNetworks(spec, status *[]configv1.CIDR) error {
	if spec == nil || status == nil {
		return fmt.Errorf("passed nil value as spec or status machine network")
	}
	if len(*spec) == 0 {
		// This is a subtlety of JSON marshalling. What happens is that
		// `var myslice []int` gets marshalled to null, but
		// `myslice := []int{}` gets marshalled to []
		// Because of this we want to be explicit that machine networks is an empty list and not null
		*spec = []configv1.CIDR{}
	}

	if len(*status) != 0 {
		if len(*spec) == 0 {
			return fmt.Errorf("removing machine networks is forbidden")
		}
		if (*spec)[0] != (*status)[0] {
			return fmt.Errorf("first machine network cannot be modified, have '%s' and requested '%s'", (*status)[0], (*spec)[0])
		}
	}

	// `spec` is updated, need to propagate to `status`
	*status = *spec

	return nil
}

// syncVips propagates set of VIPs (ingress or API) from Spec to Status assuming they pass required validations.
// It uses machine networks as additional source of knowledge about the environment so that validations can be
// stricter. In order to be valid, VIPs need to fulfil the following spec:
//
//   - the first VIP can never be modified
//   - modification or removal of the second VIP is allowed
//
// It is possible that Spec is empty and Status is not if this is a cluster that has been upgraded from an older
// version where this CRD did not have Spec defined.
//
// It is not possible to have Status field empty. This would happen only when something went wrong during
// the installation, and we would catch it way before running this code.
func syncVips(spec *[]configv1.IP, status *[]string) error {
	if spec == nil || status == nil {
		return fmt.Errorf("passed nil value as spec or status vip")
	}

	// If status is not initialized, it will cause issues when committing "null" value to update
	if *status == nil {
		*status = []string{}
	}

	// `spec` is empty, `status` with value: copy status to spec
	if len(*spec) == 0 {
		*spec = ip.StringsToIPs(*status)
	} else {
		if string((*spec)[0]) != (*status)[0] {
			return fmt.Errorf("first VIP cannot be modified, have '%s' and requested '%s'", (*status)[0], (*spec)[0])
		}

		// `spec` is updated, need to propagate to `status`
		*status = ip.IPsToStrings(*spec)
	}

	return nil
}
