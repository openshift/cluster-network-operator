package openstack

import (
	"regexp"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/attributestags"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/layer3/routers"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/mtu"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/security/groups"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/security/rules"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/subnetpools"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/networks"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/ports"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/subnets"
	iputil "github.com/openshift/cluster-network-operator/pkg/util/ip"
	"github.com/pkg/errors"
)

// Use Neutron tags feature to tag Neutron resources. resource argument must
// say what type of resource it is.
func tagResource(client *gophercloud.ServiceClient, resource, id, tag string) ([]string, error) {
	tagOpts := attributestags.ReplaceAllOpts{Tags: []string{tag}}
	return attributestags.ReplaceAll(client, resource, id, tagOpts).Extract()
}

// Looks for a Neutron network by name and tag, if it does not exist creates it.
// Will fail if two networks match with name and tag.
func ensureOpenStackNetwork(client *gophercloud.ServiceClient, name, tag string) (string, error) {
	page, err := networks.List(client, networks.ListOpts{Name: name, Tags: tag}).AllPages()
	if err != nil {
		return "", errors.Wrap(err, "failed to get network list")
	}
	nets, err := networks.ExtractNetworks(page)
	if err != nil {
		return "", errors.Wrap(err, "failed to extract networks list")
	}
	if len(nets) > 1 {
		return "", errors.Errorf("found multiple networks matching name %s and tag %s, cannot proceed", name, tag)
	} else if len(nets) == 1 {
		return nets[0].ID, nil
	} else {
		opts := networks.CreateOpts{
			Name: name,
		}
		netObj, err := networks.Create(client, opts).Extract()
		if err != nil {
			return "", errors.Wrap(err, "failed to create network")
		}

		_, err = tagResource(client, "networks", netObj.ID, tag)
		if err != nil {
			return "", errors.Wrap(err, "failed to tag created network")
		}

		return netObj.ID, nil
	}
}

// Looks for a Neutron subnetpool by name and tag, if it does not exist creates it.
// Will fail if two subnetpools match with name and tag.
func ensureOpenStackSubnetpool(client *gophercloud.ServiceClient, name, tag string, cidrs []string, prefixLen uint32) (string, error) {
	page, err := subnetpools.List(client, subnetpools.ListOpts{Name: name, Tags: tag}).AllPages()
	if err != nil {
		return "", errors.Wrap(err, "failed to get subnetpool list")
	}
	sp, err := subnetpools.ExtractSubnetPools(page)
	if err != nil {
		return "", errors.Wrap(err, "failed to extract subnetpools list")
	}
	if len(sp) > 1 {
		return "", errors.Errorf("found multiple subnetpools matching name %s and tag %s, cannot proceed", name, tag)
	} else if len(sp) == 1 {
		// TODO(dulek): Check if it has correct CIDRs.
		return sp[0].ID, nil
	} else {
		opts := subnetpools.CreateOpts{
			Name:             name,
			Prefixes:         cidrs,
			DefaultPrefixLen: int(prefixLen),
		}
		subnetpoolObj, err := subnetpools.Create(client, opts).Extract()
		if err != nil {
			return "", errors.Wrap(err, "failed to create subnetpool")
		}

		_, err = tagResource(client, "subnetpools", subnetpoolObj.ID, tag)
		if err != nil {
			return "", errors.Wrap(err, "failed to tag created subnetpool")
		}

		return subnetpoolObj.ID, nil
	}
}

// Looks for a Neutron Network by tag. Fails if network is not found
// or multiple networks match.
func findOpenStackNetwork(client *gophercloud.ServiceClient, tag string) (networks.Network, error) {
	empty := networks.Network{}
	opts := networks.ListOpts{Tags: tag}
	page, err := networks.List(client, opts).AllPages()
	if err != nil {
		return empty, errors.Wrap(err, "failed to get network list")
	}
	networkList, err := networks.ExtractNetworks(page)
	if err != nil {
		return empty, errors.Wrap(err, "failed to extract networks list")
	}
	if len(networkList) == 1 {
		return networkList[0], nil
	} else if len(networkList) == 0 {
		return empty, errors.New("network not found")
	} else {
		return empty, errors.New("multiple matching networks")
	}
}

// Gets the MTU of a Network
func getOpenStackNetworkMTUAndAZs(client *gophercloud.ServiceClient, networkID string) (int, []string, error) {
	type NetworkMTU struct {
		networks.Network
		mtu.NetworkMTUExt
	}
	var network NetworkMTU
	err := networks.Get(client, networkID).ExtractInto(&network)
	if err != nil {
		return 0, []string{}, errors.Wrap(err, "failed to extract network MTU")
	}
	return network.MTU, network.Network.AvailabilityZoneHints, nil
}

// Looks for a Neutron subnet by name and tag. Fails if not found.
func findOpenStackSubnet(client *gophercloud.ServiceClient, name, tag string) (subnets.Subnet, error) {
	empty := subnets.Subnet{}
	page, err := subnets.List(client, subnets.ListOpts{Name: name, Tags: tag}).AllPages()
	if err != nil {
		return empty, errors.Wrap(err, "failed to get subnet list")
	}
	subnetList, err := subnets.ExtractSubnets(page)
	if err != nil {
		return empty, errors.Wrap(err, "failed to extract subnets list")
	}
	if len(subnetList) == 1 {
		return subnetList[0], nil
	} else if len(subnetList) == 0 {
		return empty, errors.New("subnet not found")
	} else {
		return empty, errors.New("multiple matching subnets")
	}
}

func findOpenStackSubnetByNetworkTag(client *gophercloud.ServiceClient, tag string) (subnets.Subnet, error) {
	empty := subnets.Subnet{}
	// The installer tags the provided custom Network
	workerNetwork, err := findOpenStackNetwork(client, tag)
	if err != nil {
		return empty, errors.Wrap(err, "failed to find worker nodes subnet")
	}

	if len(workerNetwork.Subnets) == 1 {
		page, err := subnets.List(client, subnets.ListOpts{ID: workerNetwork.Subnets[0]}).AllPages()
		if err != nil {
			return empty, errors.Wrap(err, "failed to get subnet list")
		}
		subnetList, err := subnets.ExtractSubnets(page)
		if err != nil {
			return empty, errors.Wrap(err, "failed to extract subnets list")
		}
		return subnetList[0], nil
	} else {
		return empty, errors.New("subnet not found")
	}
}

// Gets a Neutron subnet by ID. In case of not found fails.
func getOpenStackSubnet(client *gophercloud.ServiceClient, id string) (subnets.Subnet, error) {
	empty := subnets.Subnet{}
	subnet, err := subnets.Get(client, id).Extract()
	if err != nil {
		return empty, errors.Wrapf(err, "failed to get subnet %s", id)
	}
	return *subnet, nil
}

// Looks for a Neutron subnet by name, tag, network ID, CIDR and gateway IP,
// if it does not exist creates it using allocationRanges as allocation pools.
// Will fail if two subnets match all the criteria.
func ensureOpenStackSubnet(client *gophercloud.ServiceClient, name, tag, netId, cidr, gatewayIp string, allocationRanges []iputil.IPRange) (string, error) {
	dhcp := false
	page, err := subnets.List(client, subnets.ListOpts{
		Name:       name,
		Tags:       tag,
		NetworkID:  netId,
		CIDR:       cidr,
		GatewayIP:  gatewayIp,
		IPVersion:  4,
		EnableDHCP: &dhcp,
	}).AllPages()
	if err != nil {
		return "", errors.Wrap(err, "failed to get subnet list")
	}
	subnetList, err := subnets.ExtractSubnets(page)
	if err != nil {
		return "", errors.Wrap(err, "failed to extract subnets list")
	}
	if len(subnetList) > 1 {
		return "", errors.Errorf("found multiple subnets matching name %s and tag %s, cannot proceed", name, tag)
	} else if len(subnetList) == 1 {
		return subnetList[0].ID, nil
	} else {
		var allocationPools []subnets.AllocationPool
		for _, r := range allocationRanges {
			allocationPools = append(allocationPools, subnets.AllocationPool{
				Start: r.Start.String(), End: r.End.String()})
		}
		opts := subnets.CreateOpts{
			Name:            name,
			NetworkID:       netId,
			CIDR:            cidr,
			GatewayIP:       &gatewayIp,
			IPVersion:       gophercloud.IPv4,
			EnableDHCP:      &dhcp,
			AllocationPools: allocationPools,
		}
		subnetObj, err := subnets.Create(client, opts).Extract()
		if err != nil {
			return "", errors.Wrap(err, "failed to create subnet")
		}

		_, err = tagResource(client, "subnets", subnetObj.ID, tag)
		if err != nil {
			return "", errors.Wrap(err, "failed to tag created subnet")
		}

		return subnetObj.ID, nil
	}
}

// Looks for a Neutron router by name and tag. If not found, provides
// the router used by the custom Network. If no router exists, creates
// a new one. Fails if multiple routers match.
func ensureOpenStackRouter(client *gophercloud.ServiceClient, name, tag, networkID string, azs []string) (routers.Router, error) {
	empty := routers.Router{}
	page, err := routers.List(client, routers.ListOpts{Name: name, Tags: tag}).AllPages()
	if err != nil {
		return empty, errors.Wrap(err, "failed to get router list")
	}
	routerList, err := routers.ExtractRouters(page)
	if err != nil {
		return empty, errors.Wrap(err, "failed to extract routers list")
	}

	if len(routerList) == 1 {
		return routerList[0], nil
	} else if len(routerList) == 0 {
		networkPorts, err := getOpenStackPortsByNetwork(client, networkID)
		if err != nil {
			return empty, errors.Wrap(err, "failed to list Ports on Network")
		}
		for _, port := range networkPorts {
			if port.DeviceID != "" {
				router, err := routers.Get(client, port.DeviceID).Extract()
				if router != nil {
					return *router, nil
				}
				var gerr gophercloud.ErrDefault404
				if !errors.As(err, &gerr) {
					return empty, errors.Wrap(err, "failed to get router")
				}
			}
		}

		router, err := routers.Create(client, routers.CreateOpts{Name: name, AvailabilityZoneHints: azs}).Extract()
		if err != nil {
			return empty, errors.Wrap(err, "failed to create router")
		}
		_, err = tagResource(client, "routers", router.ID, tag)
		if err != nil {
			return empty, errors.Wrap(err, "failed to tag created router")
		}
		return *router, nil
	} else {
		return empty, errors.New("multiple matching routers")
	}
}

// Returns list of all Neutron ports that belongs to a a given Network.
func getOpenStackPortsByNetwork(client *gophercloud.ServiceClient, networkID string) ([]ports.Port, error) {
	page, err := ports.List(client, ports.ListOpts{NetworkID: networkID}).AllPages()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get port list")
	}
	ps, err := ports.ExtractPorts(page)
	if err != nil {
		return nil, errors.Wrap(err, "failed to extract port list")
	}
	return ps, nil
}

// Returns list of all Neutron ports that belong to a given Neutron router.
func getOpenStackRouterPorts(client *gophercloud.ServiceClient, routerId string) ([]ports.Port, error) {
	page, err := ports.List(client, ports.ListOpts{DeviceID: routerId}).AllPages()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get port list")
	}
	ps, err := ports.ExtractPorts(page)
	if err != nil {
		return nil, errors.Wrap(err, "failed to extract port list")
	}
	return ps, nil
}

// Returns whether slice ps has a Port that is from subnet subnetId.
func lookupOpenStackPort(ps []ports.Port, subnetId string) bool {
	for _, port := range ps {
		for _, ip := range port.FixedIPs {
			if ip.SubnetID == subnetId {
				return true
			}
		}
	}
	return false
}

// Adds a subnetId subnet or portId port to routerId router. Will fail if such
// a connection already exists.
func ensureOpenStackRouterInterface(client *gophercloud.ServiceClient, routerId string, subnetId, portId *string) error {
	opts := routers.AddInterfaceOpts{}
	if subnetId != nil {
		opts.SubnetID = *subnetId
	}
	if portId != nil {
		opts.PortID = *portId
	}
	_, err := routers.AddInterface(client, routerId, opts).Extract()
	if err != nil {
		return errors.Wrap(err, "failed to add interface")
	}
	return nil
}

// Looks up OpenStack ports by tag and regexp pattern matched against name.
// Returns a slice with matched Ports.
func listOpenStackPortsMatchingPattern(client *gophercloud.ServiceClient, tag string, pattern *regexp.Regexp) ([]ports.Port, error) {
	page, err := ports.List(client, ports.ListOpts{Tags: tag}).AllPages()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get port list")
	}
	portList, err := ports.ExtractPorts(page)
	if err != nil {
		return nil, errors.Wrap(err, "failed to extract ports list")
	}
	result := []ports.Port{}
	for _, port := range portList {
		if pattern.MatchString(port.Name) {
			result = append(result, port)
		}
	}

	return result, nil
}

// Looks for a Neutron Port by name, tag and network ID. If it does not exist
// creates it. Will fail if multiple ports are matching all criteria.
func ensureOpenStackPort(client *gophercloud.ServiceClient, name, tag, netId, subnetId, ip string) (string, error) {
	page, err := ports.List(client, ports.ListOpts{Name: name, Tags: tag, NetworkID: netId}).AllPages()
	if err != nil {
		return "", errors.Wrap(err, "failed to get port list")
	}
	portList, err := ports.ExtractPorts(page)
	if err != nil {
		return "", errors.Wrap(err, "failed to extract ports list")
	}
	if len(portList) > 1 {
		return "", errors.Errorf("found multiple ports matching name %s, tag %s, cannot proceed", name, tag)
	} else if len(portList) == 1 {
		return portList[0].ID, nil
	} else {
		opts := ports.CreateOpts{
			Name:      name,
			NetworkID: netId,
			FixedIPs:  []ports.IP{{SubnetID: subnetId, IPAddress: ip}},
		}
		portObj, err := ports.Create(client, opts).Extract()
		if err != nil {
			return "", errors.Wrap(err, "failed to create port")
		}

		_, err = tagResource(client, "ports", portObj.ID, tag)
		if err != nil {
			return "", errors.Wrap(err, "failed to tag created port")
		}

		return portObj.ID, nil
	}
}

// Looks for a OpenStack security group by name and tag. Fails if SG is not found
// or multiple SG's match.
func findOpenStackSgId(client *gophercloud.ServiceClient, name, tag string) (string, error) {
	page, err := groups.List(client, groups.ListOpts{Name: name, Tags: tag}).AllPages()
	if err != nil {
		return "", errors.Wrap(err, "failed to get SG list")
	}
	sgs, err := groups.ExtractGroups(page)
	if err != nil {
		return "", errors.Wrap(err, "failed to extract SG list")
	}

	if len(sgs) == 1 {
		return sgs[0].ID, nil
	} else if len(sgs) == 0 {
		return "", errors.New("SG not found")
	} else {
		return "", errors.New("multiple matching SGs")
	}
}

// Looks for a OpenStack security group by name and tag. If it does not exist
// creates it. Will fail if multiple SG's are matching all criteria.
func ensureOpenStackSg(client *gophercloud.ServiceClient, name, tag string) (string, error) {
	page, err := groups.List(client, groups.ListOpts{Name: name, Tags: tag}).AllPages()
	if err != nil {
		return "", errors.Wrap(err, "failed to get SG list")
	}
	sgs, err := groups.ExtractGroups(page)
	if err != nil {
		return "", errors.Wrap(err, "failed to extract SG list")
	}
	if len(sgs) > 1 {
		return "", errors.Errorf("found multiple SG matching name %s, tag %s, cannot proceed", name, tag)
	} else if len(sgs) == 1 {
		return sgs[0].ID, nil
	} else {
		opts := groups.CreateOpts{
			Name: name,
		}
		sg, err := groups.Create(client, opts).Extract()
		if err != nil {
			return "", errors.Wrap(err, "failed to create SG")
		}

		_, err = tagResource(client, "security-groups", sg.ID, tag)
		if err != nil {
			return "", errors.Wrap(err, "failed to tag created SG")
		}

		return sg.ID, nil
	}
}

// Tries to create an OpenStack security group rule on sgId SG. Ignores an
// error if such rule already exists.
func ensureOpenStackSgRule(client *gophercloud.ServiceClient, sgId, remotePrefix string, portMin, portMax int, protocol rules.RuleProtocol) error {
	opts := rules.CreateOpts{
		SecGroupID:     sgId,
		EtherType:      rules.EtherType4,
		Direction:      rules.DirIngress,
		RemoteIPPrefix: remotePrefix,
	}
	// Let's just assume that we're getting passed 0 when we aren't supposed to set those
	if portMin > 0 && portMax > 0 {
		opts.PortRangeMin = portMin
		opts.PortRangeMax = portMax
		opts.Protocol = protocol
	}
	_, err := rules.Create(client, opts).Extract()
	if err != nil {
		if _, ok := err.(gophercloud.ErrDefault409); ok {
			// Ignoring 409 Conflict as that means the rule is already there.
			return nil
		}
		return errors.Wrap(err, "failed to create SG rule")
	}
	return nil
}

// Returns list of OpenStack ingress security group rules on SGs tagged with tag.
func listOpenStackSgRules(client *gophercloud.ServiceClient, tag string) ([]rules.SecGroupRule, error) {
	page, err := groups.List(client, groups.ListOpts{Tags: tag}).AllPages()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get security group list")
	}
	groupsList, err := groups.ExtractGroups(page)
	if err != nil {
		return nil, errors.Wrap(err, "failed to extract security group list")
	}

	var rulesList []rules.SecGroupRule
	for _, group := range groupsList {
		for _, rule := range group.Rules {
			if rule.Direction == string(rules.DirIngress) {
				rulesList = append(rulesList, rule)
			}
		}
	}

	return rulesList, nil
}
