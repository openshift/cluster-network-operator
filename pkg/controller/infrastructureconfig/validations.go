package infrastructureconfig

import (
	"fmt"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/util/ip"
	utilnet "k8s.io/utils/net"
	"net/netip"
)

// validateVipsWithVips compares API and Ingress VIPs to detect whether those can coexist. It is supposed to catch
// scenarios when configuring particular set of addresses is valid in the given environment (by machine networks)
// but invalid between set of VIPs. In order to be valid, VIPs need to fulfil the following spec:
//
//   - 0 VIPs is allowed as a migration from pre-4.16 (where Spec is empty)
//   - the number of VIPs is either 1 or 2
//   - number of VIPs needs to be the same for API and ingress
//   - no any pair of VIPs can be equal, unless External Load Balancer is used
//   - IP stack of the second VIP needs to be different from IP stack of the first VIP
func validateVipsWithVips(api, ingress []configv1.IP, elb bool) error {
	if len(api) > 2 {
		return fmt.Errorf("number of API VIPs needs to be less or equal to 2, got %d", len(api))
	}
	if len(ingress) > 2 {
		return fmt.Errorf("number of Ingress VIPs needs to be less or equal to 2, got %d", len(api))
	}
	if len(api) != len(ingress) {
		return fmt.Errorf("number of API VIPs (%d) does not match number of Ingress VIPs (%d)", len(api), len(ingress))
	}

	// For external load balancer we allow VIPs to be equal.
	if !elb {
		for i := 0; i < len(api); i++ {
			if api[i] == ingress[i] {
				return fmt.Errorf("VIPs cannot be equal, got '%s' for API and '%s' for Ingress", api[i], ingress[i])
			}
		}
	}

	if len(api) > 1 {
		ok, err := utilnet.IsDualStackIPStrings(ip.IPsToStrings(api))
		if !ok || err != nil {
			return fmt.Errorf("with more than 1 API VIP at least one from each IP family is required, err: %v", err)
		}

		ok, err = utilnet.IsDualStackIPStrings(ip.IPsToStrings(ingress))
		if !ok || err != nil {
			return fmt.Errorf("with more than 1 Ingress VIP at least one from each IP family is required, err: %v", err)
		}
	}

	return nil
}

// validateVipsWithMachineNetworks compares VIPs and machine networks to detect whether those can coexist. It is
// supposed to catch scenarios when configuring particular set of VIPs would be illegal given the provided Machine
// Networks. In order to be valid, VIPs need to fulfil the following spec:
//
//   - IP stack of the first VIP needs to be IP stack of the first machine network (we do not actually check it in
//     this function because o/api and o/installer make sure this is the case; unless we allow changing 1st VIP no
//     need to check it also here)
//   - every VIP needs to belong to the machine network (one of them, no matter which one)
func validateVipsWithMachineNetworks(vips []configv1.IP, machineNetworks []configv1.CIDR) error {
	if len(machineNetworks) == 0 {
		return nil
	}

	for _, vip := range vips {
		found := false
		ip, err := netip.ParseAddr(string(vip))
		if err != nil {
			return err
		}

		for _, machine := range machineNetworks {
			network, err := netip.ParsePrefix(string(machine))
			if err != nil {
				return err
			}
			if network.Contains(ip) {
				found = true
				break
			}
		}

		if !found {
			return fmt.Errorf("VIP '%s' cannot be found in any machine network", vip)
		}
	}

	return nil
}
