package openstack

import (
	"github.com/gophercloud/gophercloud/openstack/loadbalancer/v2/providers"
	"log"

	"github.com/pkg/errors"

	"github.com/Masterminds/semver"
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack/loadbalancer/v2/apiversions"
	"github.com/gophercloud/gophercloud/openstack/loadbalancer/v2/listeners"
	"github.com/gophercloud/gophercloud/openstack/loadbalancer/v2/loadbalancers"
	"github.com/gophercloud/gophercloud/openstack/loadbalancer/v2/monitors"
	"github.com/gophercloud/gophercloud/openstack/loadbalancer/v2/pools"
)

func getMaxOctaviaAPIVersion(client *gophercloud.ServiceClient) (*semver.Version, error) {
	allPages, err := apiversions.List(client).AllPages()
	if err != nil {
		return nil, err
	}

	apiVersions, err := apiversions.ExtractAPIVersions(allPages)
	if err != nil {
		return nil, err
	}

	var max *semver.Version = nil
	for _, apiVersion := range apiVersions {
		ver, err := semver.NewVersion(apiVersion.ID)

		if err != nil {
			// We're ignoring the error, if Octavia is returning anything odd we don't care.
			log.Printf("Error when parsing Octavia API version %s: %v. Ignoring it", apiVersion.ID, err)
			continue
		}

		if max == nil || ver.GreaterThan(max) {
			max = ver
		}
	}

	if max == nil {
		// If we have max == nil at this point, then we couldn't read the versions at all.
		// This happens for 2.0 API and let's return that.
		max = semver.MustParse("v2.0")
	}

	log.Printf("Detected Octavia API v%s", max)

	return max, nil
}

func IsOctaviaVersionSupported(client *gophercloud.ServiceClient, constraint string) (bool, error) {
	maxOctaviaVersion, err := getMaxOctaviaAPIVersion(client)
	if err != nil {
		return false, errors.Wrap(err, "cannot get Octavia API versions")
	}

	constraintVer := semver.MustParse(constraint)

	return !constraintVer.GreaterThan(maxOctaviaVersion), nil
}

// Delete a LoadBalancer and all associated resources
func deleteOpenStackLb(client *gophercloud.ServiceClient, lbId string) error {
	log.Printf("Deleting Openstack LoadBalancer: %s", lbId)
	deleteOpts := loadbalancers.DeleteOpts{
		Cascade: true,
	}
	err := loadbalancers.Delete(client, lbId, deleteOpts).ExtractErr()
	if err != nil {
		return err
	}

	err = gophercloud.WaitFor(300, func() (bool, error) {
		lb, err := loadbalancers.Get(client, lbId).Extract()
		if err != nil {
			if _, ok := err.(gophercloud.ErrDefault404); ok {
				return true, nil
			}
			return false, err
		}
		if lb.ProvisioningStatus == "DELETED" {
			return true, nil
		}
		return false, nil
	})

	return err
}

// Waits up to 5 minutes for OpenStack LoadBalancer to move into ACTIVE
// provisioning_status. Fails if time runs out, or LoadBalancer goes in ERROR
// state.
func waitForOpenStackLb(client *gophercloud.ServiceClient, lbId string) error {
	err := gophercloud.WaitFor(300, func() (bool, error) {
		lb, err := loadbalancers.Get(client, lbId).Extract()
		if err != nil {
			return false, err
		}

		if lb.ProvisioningStatus == "ERROR" {
			return true, errors.Errorf("LoadBalancer gone in error state")
		}

		return lb.ProvisioningStatus == "ACTIVE", nil
	})

	return err
}

// Looks for a Octavia load balancer by name, address and subnet ID. If it does
// not exist creates it. Will fail if multiple LB's are matching all criteria.
func ensureOpenStackLb(client *gophercloud.ServiceClient, name, vipAddress, vipSubnetId, tag string) (string, error) {
	// We need to figure out if Octavia supports tags and use description field if it's too old. To do that
	// we list available API versions and look for 2.5. This is because we support Queens and Rocky releases of
	// OpenStack and those were before tags got implemented.
	// TODO(dulek): This workaround can be removed once we stop supporting Queens and Rocky OpenStack releases.
	octaviaTagSupport, err := IsOctaviaVersionSupported(client, MinOctaviaVersionWithTagSupport)
	if err != nil {
		return "", errors.Wrap(err, "failed to determine if Octavia supports tags")
	}

	opts := loadbalancers.ListOpts{
		Name:        name,
		VipAddress:  vipAddress,
		VipSubnetID: vipSubnetId,
	}
	if octaviaTagSupport {
		opts.Tags = []string{tag}
	} else {
		opts.Description = tag
	}

	page, err := loadbalancers.List(client, opts).AllPages()
	if err != nil {
		return "", errors.Wrap(err, "failed to get LB list")
	}
	lbs, err := loadbalancers.ExtractLoadBalancers(page)
	if err != nil {
		return "", errors.Wrap(err, "failed to extract LB list")
	}

	if len(lbs) == 0 && octaviaTagSupport {
		// Attempt to retrieve API load balancer with description tagging
		// to avoid another load balancer creation upon Octavia upgrade.
		opts := loadbalancers.ListOpts{
			Name:        name,
			VipAddress:  vipAddress,
			VipSubnetID: vipSubnetId,
			Description: tag,
		}
		page, err = loadbalancers.List(client, opts).AllPages()
		if err != nil {
			return "", errors.Wrap(err, "failed to get LB list")
		}
		lbs, err = loadbalancers.ExtractLoadBalancers(page)
		if err != nil {
			return "", errors.Wrap(err, "failed to extract LB list")
		}
		if len(lbs) == 1 {
			log.Printf("Tagging existing loadbalancer API %s", lbs[0].ID)
			tags := []string{tag}
			updateOpts := loadbalancers.UpdateOpts{
				Tags: &tags,
			}
			_, err := loadbalancers.Update(client, lbs[0].ID, updateOpts).Extract()
			if err != nil {
				return "", errors.Wrap(err, "failed to update LB")
			}
		}
	}

	if len(lbs) > 1 {
		return "", errors.Errorf("found multiple LB matching name %s, tag %s, cannot proceed", name, tag)
	} else if len(lbs) == 1 {
		if lbs[0].ProvisioningStatus == "ACTIVE" {
			return lbs[0].ID, nil
		} else if lbs[0].ProvisioningStatus == "ERROR" {
			err := deleteOpenStackLb(client, lbs[0].ID)
			if err != nil {
				return "", errors.Wrap(err, "failed to delete LB")
			}
		}
	}

	createOpts := loadbalancers.CreateOpts{
		Name:        name,
		VipAddress:  vipAddress,
		VipSubnetID: vipSubnetId,
	}
	if octaviaTagSupport {
		createOpts.Tags = []string{tag}
	} else {
		createOpts.Description = tag
	}
	lb, err := loadbalancers.Create(client, createOpts).Extract()
	if err != nil {
		return "", errors.Wrap(err, "failed to create LB")
	}
	err = waitForOpenStackLb(client, lb.ID)
	if err != nil {
		return "", errors.Wrapf(err, "Error waiting for LB %s", lb.ID)
	}

	return lb.ID, nil
}

// Looks for a Octavia load balancer pool by name and LB ID. If it does
// not exist creates it. Will fail if multiple LB pools are matching all criteria.
func ensureOpenStackLbPool(client *gophercloud.ServiceClient, name, lbId string) (string, error) {
	page, err := pools.List(client, pools.ListOpts{
		Name:           name,
		LoadbalancerID: lbId,
		Protocol:       "HTTPS",
		LBMethod:       "ROUND_ROBIN",
	}).AllPages()
	if err != nil {
		return "", errors.Wrap(err, "failed to get LB pools list")
	}
	poolsList, err := pools.ExtractPools(page)
	if err != nil {
		return "", errors.Wrap(err, "failed to extract LB pools list")
	}
	if len(poolsList) > 1 {
		return "", errors.Errorf("found multiple LB pools matching name %s, LB %s, cannot proceed", name, lbId)
	} else if len(poolsList) == 1 {
		return poolsList[0].ID, nil
	} else {
		opts := pools.CreateOpts{
			Name:           name,
			LoadbalancerID: lbId,
			Protocol:       pools.ProtocolHTTPS,
			LBMethod:       pools.LBMethodRoundRobin,
		}
		poolsObj, err := pools.Create(client, opts).Extract()
		if err != nil {
			return "", errors.Wrap(err, "failed to create LB pool")
		}

		err = waitForOpenStackLb(client, lbId)
		if err != nil {
			return "", errors.Wrapf(err, "Error waiting for LB %s", lbId)
		}

		return poolsObj.ID, nil
	}
}

// Looks for Octavia load balancer health monitor by name and pool ID. If it does
// not exist creates it. Will fail if multiple LB health monitors are matching all criteria.
func ensureOpenStackLbMonitor(client *gophercloud.ServiceClient, name, poolId string) (string, error) {
	octaviaHTTPSMonitors, err := IsOctaviaVersionSupported(client, MinOctaviaVersionWithHTTPSMonitors)
	if err != nil {
		return "", errors.Wrap(err, "failed to determine if Octavia supports HTTPS health monitors")
	}

	page, err := monitors.List(client, monitors.ListOpts{
		Name:   name,
		PoolID: poolId,
	}).AllPages()
	if err != nil {
		return "", errors.Wrap(err, "failed to get LB monitors list")
	}
	monitorsList, err := monitors.ExtractMonitors(page)
	if err != nil {
		return "", errors.Wrap(err, "failed to extract LB monitors list")
	}
	if len(monitorsList) > 1 {
		return "", errors.Errorf("found multiple LB monitors matching name %s, pool %s, cannot proceed", name, poolId)
	} else if len(monitorsList) == 1 {
		return monitorsList[0].ID, nil
	} else {
		opts := monitors.CreateOpts{
			Name:       name,
			PoolID:     poolId,
			Type:       monitors.TypeTCP,
			MaxRetries: 3,
			Delay:      10,
			Timeout:    10,
		}
		if octaviaHTTPSMonitors {
			// TODO(dulek): Octavia is a wild animal. So in OpenStack Stein the meaning of HTTPS type of health monitor
			//              changed. Before, it was a simple TLS handshake. Now it's really doing an HTTPS connection to
			//              a certain URL and TLS-HELLO is the new HTTPS. We're going to stick with the simple TCP check
			//              for Octavia's prior to Stein and in Stein we switch to proper HTTPS healthcheck. The former
			//              behavior can be removed once we stop supporting OpenStack Queens and Rocky.
			opts.URLPath = "/healthz"
			opts.Type = monitors.TypeHTTPS
		}
		monitorObj, err := monitors.Create(client, opts).Extract()
		if err != nil {
			return "", errors.Wrap(err, "failed to create LB monitor")
		}

		return monitorObj.ID, nil
	}
}

// Looks for a Octavia load balancer pool member by name, address and port. If
// it does not exist creates it. Will fail if multiple LB pool members are
// matching all criteria.
func ensureOpenStackLbPoolMember(client *gophercloud.ServiceClient, name, lbId, poolId,
	address, subnetId string, port, weight int) (string, error) {
	page, err := pools.ListMembers(client, poolId, pools.ListMembersOpts{
		Name:         name,
		Address:      address,
		ProtocolPort: port,
	}).AllPages()
	if err != nil {
		return "", errors.Wrap(err, "failed to get LB member list")
	}
	members, err := pools.ExtractMembers(page)
	if err != nil {
		return "", errors.Wrap(err, "failed to extract LB members list")
	}
	if len(members) > 1 {
		return "", errors.Errorf("found multiple LB members matching name %s, cannot proceed", name)
	} else if len(members) == 1 {
		return members[0].ID, nil
	} else {
		opts := pools.CreateMemberOpts{
			Name:         name,
			Address:      address,
			ProtocolPort: port,
			SubnetID:     subnetId,
			Weight:       &weight,
		}
		poolsObj, err := pools.CreateMember(client, poolId, opts).Extract()
		if err != nil {
			return "", errors.Wrap(err, "failed to create LB member")
		}

		err = waitForOpenStackLb(client, lbId)
		if err != nil {
			return "", errors.Wrapf(err, "Error waiting for LB %s", lbId)
		}

		return poolsObj.ID, nil
	}
}

// Looks for a Octavia load balancer listeners by name, port, pool ID and LB ID.
// If it does not exist creates it. Will fail if multiple LB listeners are
// matching all criteria.
func ensureOpenStackLbListener(client *gophercloud.ServiceClient, name, lbId, poolId string, port int) (string, error) {
	page, err := listeners.List(client, listeners.ListOpts{
		Name:           name,
		Protocol:       "HTTPS",
		ProtocolPort:   port,
		DefaultPoolID:  poolId,
		LoadbalancerID: lbId,
	}).AllPages()
	if err != nil {
		return "", errors.Wrap(err, "failed to get LB listeners list")
	}
	listenersList, err := listeners.ExtractListeners(page)
	if err != nil {
		return "", errors.Wrap(err, "failed to extract LB listeners list")
	}
	if len(listenersList) > 1 {
		return "", errors.Errorf("found multiple LB listeners matching name %s, LB %s, cannot proceed", name, lbId)
	} else if len(listenersList) == 1 {
		return listenersList[0].ID, nil
	} else {
		opts := listeners.CreateOpts{
			Name:           name,
			Protocol:       listeners.ProtocolHTTPS,
			ProtocolPort:   port,
			DefaultPoolID:  poolId,
			LoadbalancerID: lbId,
		}

		// NOTE(dulek): If Octavia supports setting data timeouts in listeners (Rocky+) we set them to 10 mins hour as this
		//              LB will be used for watching the Kubernetes API, that shouldn't time out after the default 50 seconds.
		timeoutSupport, err := IsOctaviaVersionSupported(client, MinOctaviaVersionWithTimeouts)
		if err != nil {
			return "", errors.Wrap(err, "failed to determine if Octavia supports listener timeouts API")
		}
		timeout := 600000
		if timeoutSupport {
			opts.TimeoutClientData = &timeout
			opts.TimeoutMemberData = &timeout
		}

		listenerObj, err := listeners.Create(client, opts).Extract()
		if err != nil {
			return "", errors.Wrap(err, "failed to create LB listener")
		}

		err = waitForOpenStackLb(client, lbId)
		if err != nil {
			return "", errors.Wrapf(err, "Error waiting for LB %s", lbId)
		}

		return listenerObj.ID, nil
	}
}

func listOpenStackOctaviaProviders(client *gophercloud.ServiceClient) ([]providers.Provider, error) {
	page, err := providers.List(client, providers.ListOpts{}).AllPages()
	if err != nil {
		return nil, err
	} else {
		providersList, err := providers.ExtractProviders(page)
		if err != nil {
			return nil, err
		}
		return providersList, nil
	}
}
