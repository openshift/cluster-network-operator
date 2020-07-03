package openstack

import (
	"log"

	"github.com/pkg/errors"

	"github.com/Masterminds/semver"
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack/loadbalancer/v2/apiversions"
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

// Iterate on pool members and check their address against provided list
// addresses of current master/bootstrap nodes. Remove all surplus members,
// which address doesn't exists on that list.
func purgeOpenStackLbPoolMember(client *gophercloud.ServiceClient, poolId string, addresses []string) error {
	page, err := pools.ListMembers(client, poolId, nil).AllPages()
	if err != nil {
		return errors.Wrap(err, "failed to get LB member list")
	}

	members, err := pools.ExtractMembers(page)
	if err != nil {
		return errors.Wrap(err, "failed to extract LB members list")
	}

	for _, member := range members {
		found := false
		for _, address := range addresses {
			if address == member.Address {
				found = true
				break
			}
		}
		if !found {
			err = pools.DeleteMember(client, poolId, member.ID).ExtractErr()
			if err != nil {
				return errors.Wrap(err, "failed to delete LB member")
			}
		}
	}
	return nil
}
