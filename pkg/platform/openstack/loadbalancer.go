package openstack

import (
	"log"

	"github.com/pkg/errors"

	"github.com/Masterminds/semver"
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack/loadbalancer/v2/apiversions"
)

var maxOctaviaVersion *semver.Version = nil

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
	if maxOctaviaVersion == nil {
		var err error
		maxOctaviaVersion, err = getMaxOctaviaAPIVersion(client)
		if err != nil {
			return false, errors.Wrap(err, "cannot get Octavia API versions")
		}
	}

	constraintVer := semver.MustParse(constraint)

	return !constraintVer.GreaterThan(maxOctaviaVersion), nil
}
