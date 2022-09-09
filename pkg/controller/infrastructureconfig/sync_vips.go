package infrastructureconfig

import (
	"fmt"
	"log"
	"strings"

	configv1 "github.com/openshift/api/config/v1"
	utilslice "k8s.io/utils/strings/slices"
)

type vipsSynchronizer interface {
	VipsSynchronize(*configv1.Infrastructure) *configv1.Infrastructure
}

type apiAndIngressVipsSynchronizer struct{}

// VipsSynchronize synchronizes new API & Ingress VIPs with old fields.
// It returns if the status was updated and the updated infrastructure object.
//nolint:staticcheck
func (*apiAndIngressVipsSynchronizer) VipsSynchronize(infraConfig *configv1.Infrastructure) *configv1.Infrastructure {
	var apiVIPs, ingressVIPs *[]string // new fields
	var apiVIP, ingressVIP *string     // old/deprecated fields

	updatedInfraConfig := infraConfig.DeepCopy()
	switch updatedInfraConfig.Status.Platform {
	case configv1.BareMetalPlatformType:
		apiVIPs = &updatedInfraConfig.Status.PlatformStatus.BareMetal.APIServerInternalIPs
		apiVIP = &updatedInfraConfig.Status.PlatformStatus.BareMetal.APIServerInternalIP
		ingressVIPs = &updatedInfraConfig.Status.PlatformStatus.BareMetal.IngressIPs
		ingressVIP = &updatedInfraConfig.Status.PlatformStatus.BareMetal.IngressIP

	case configv1.VSpherePlatformType:
		apiVIPs = &updatedInfraConfig.Status.PlatformStatus.VSphere.APIServerInternalIPs
		apiVIP = &updatedInfraConfig.Status.PlatformStatus.VSphere.APIServerInternalIP
		ingressVIPs = &updatedInfraConfig.Status.PlatformStatus.VSphere.IngressIPs
		ingressVIP = &updatedInfraConfig.Status.PlatformStatus.VSphere.IngressIP

	case configv1.OpenStackPlatformType:
		apiVIPs = &updatedInfraConfig.Status.PlatformStatus.OpenStack.APIServerInternalIPs
		apiVIP = &updatedInfraConfig.Status.PlatformStatus.OpenStack.APIServerInternalIP
		ingressVIPs = &updatedInfraConfig.Status.PlatformStatus.OpenStack.IngressIPs
		ingressVIP = &updatedInfraConfig.Status.PlatformStatus.OpenStack.IngressIP

	case configv1.OvirtPlatformType:
		apiVIPs = &updatedInfraConfig.Status.PlatformStatus.Ovirt.APIServerInternalIPs
		apiVIP = &updatedInfraConfig.Status.PlatformStatus.Ovirt.APIServerInternalIP
		ingressVIPs = &updatedInfraConfig.Status.PlatformStatus.Ovirt.IngressIPs
		ingressVIP = &updatedInfraConfig.Status.PlatformStatus.Ovirt.IngressIP

	case configv1.NutanixPlatformType:
		apiVIPs = &updatedInfraConfig.Status.PlatformStatus.Nutanix.APIServerInternalIPs
		apiVIP = &updatedInfraConfig.Status.PlatformStatus.Nutanix.APIServerInternalIP
		ingressVIPs = &updatedInfraConfig.Status.PlatformStatus.Nutanix.IngressIPs
		ingressVIP = &updatedInfraConfig.Status.PlatformStatus.Nutanix.IngressIP

	default:
		// nothing to do for this platform type
		return updatedInfraConfig
	}

	if err := syncVIPs(apiVIPs, apiVIP); err != nil {
		log.Printf("Warning on syncing api VIP fields: %v", err) // this is only a warning -> continue
	}

	if err := syncVIPs(ingressVIPs, ingressVIP); err != nil {
		log.Printf("Warning on syncing ingress VIP fields: %v", err) // this is only a warning -> continue
	}

	return updatedInfraConfig
}

// syncVIPs syncs the VIPs according to the following rules:
// | # | Initial value of new field | Initial value of old field | Resulting value of new field | Resulting value of old field | Description |
// | - | -------------------------- | -------------------------- | ---------------------------- | ---------------------------- | ----------- |
// | 1 | empty                      | foo                        | [0]: foo                     | foo                          | `new` is empty, `old` with value: set `new[0]` to value from `old` |
// | 2 | [0]: foo, [1]: bar         | empty                      | [0]: foo, [1]: bar           | foo                          | `new` contains values, `old` is empty: set `old` to value from `new[0]` |
// | 3 | [0]: foo, [1]: bar         | foo                        | [0]: foo, [1]: bar           | foo                          | `new` contains values, `old` contains `new[0]`: we are fine, as `old` is part of `new` |
// | 4 | [0]: foo, [1]: bar         | bar                        | [0]: foo, [1]: bar           | foo                          | `new` contains values, `old` contains `new[1]`: as `new[0]` contains the clusters primary IP family, new values take precedence over old values, so set `old` to value from `new[0]` |
// | 5 | [0]: foo, [1]: bar         | baz                        | [0]: foo, [1]: bar           | foo                          | `new` contains values, `old` contains a value which is not included in `new`: new values take precedence over old values, so set `old` to value from `new[0]` (and return a warning) |
//
// it returns in case 5 the error.
func syncVIPs(newVIPs *[]string, oldVIP *string) error {
	if len(*newVIPs) == 0 {
		if *oldVIP != "" {
			// case 1
			// -> `new` is empty, `old` with value: set `new[0]` to value from `old`
			*newVIPs = []string{*oldVIP}
		}
	} else {
		// case 2-5, we have old = new[0]
		if !utilslice.Contains(*newVIPs, *oldVIP) {
			// case 5
			// -> `new` contains values, `old` contains a value which is not
			// included in `new`: new values take precedence over old
			// values, so set `old` to value from `new[0]` (and return a
			// warning)
			err := fmt.Errorf("old (%s) and new VIPs (%s) were both set and differed. New VIPs field will take precedence.", *oldVIP, strings.Join(*newVIPs, ", "))
			*oldVIP = (*newVIPs)[0]
			return err
		}

		*oldVIP = (*newVIPs)[0]
	}

	return nil
}
