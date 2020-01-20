package openstack

import (
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack/identity/v3/tokens"
	"github.com/pkg/errors"
)

func getProjectID(keystone *gophercloud.ServiceClient) (string, error) {
	tokenID := keystone.Token()
	proj, err := tokens.Get(keystone, tokenID).ExtractProject()
	if err != nil {
		return "", errors.Wrap(err, "failed to get token")
	}
	return proj.ID, nil
}
