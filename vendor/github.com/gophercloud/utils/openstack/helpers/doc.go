/*
Package helpers collects generic functionality over the Gophercloud OpenStack Go SDK.

OpenStack Project Purge

Set of method to purge resources associated to an OpenStack project.
This is partially inspired on the following projects:

- https://docs.openstack.org/python-openstackclient/latest/cli/command-objects/project-purge.html

- https://docs.openstack.org/neutron/latest/admin/ops-resource-purge.html

- https://opendev.org/x/ospurge


Example to Purge all the resources and Delete a Project

	purgeOpts := ProjectPurgeOpts {
		StoragePurgeOpts: &StoragePurgeOpts{storageClient},
		ComputePurgeOpts: &ComputePurgeOpts{computeClient},
		NetworkPurgeOpts: &NetworkPurgeOpts{networkClient},
	}
	projectID := "966b3c7d36a24facaf20b7e458bf2192"

	err := helpers.ProjectPurgeAll(projectID, opts)
	if err != nil {
		panic(err)
	} else {
		err = projects.Delete(identityClient, projectID).ExtractErr()
		if err != nil {
			panic(err)
		}
	}

Example to Purge storage and networking resources on a Project but keep the Project itself

	purgeOpts := ProjectPurgeOpts {
		StoragePurgeOpts: &StoragePurgeOpts{storageClient},
		NetworkPurgeOpts: &NetworkPurgeOpts{networkClient},
	}
	projectID := "966b3c7d36a24facaf20b7e458bf2192"

	err := helpers.ProjectPurgeAll(projectID, opts)
	if err != nil {
		panic(err)
	}
*/
package helpers
