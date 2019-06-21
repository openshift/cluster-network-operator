package openstack

import (
	"context"
	"fmt"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"k8s.io/api/core/v1"
	"log"
	"net"
	"regexp"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/pkg/errors"
	"gopkg.in/yaml.v2"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/identity/v3/tokens"
	"github.com/gophercloud/gophercloud/openstack/loadbalancer/v2/listeners"
	"github.com/gophercloud/gophercloud/openstack/loadbalancer/v2/loadbalancers"
	"github.com/gophercloud/gophercloud/openstack/loadbalancer/v2/pools"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/attributestags"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/layer3/routers"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/security/groups"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/security/rules"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/subnetpools"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/networks"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/ports"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/subnets"
	"github.com/gophercloud/utils/openstack/clientconfig"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	confv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"

	iputil "github.com/openshift/cluster-network-operator/pkg/util/ip"
)

const (
	// TODO(dulek): Those values come from openshift/installer and
	//              openshift/cluster-api-provider-openstack and at the moment
	//              are hardcoded there too. One day we might need to fetch them
	//              from somewhere.
	CloudsSecret          = "openstack-credentials"
	CloudsSecretNamespace = "kube-system"
	CloudName             = "openstack"
	CloudsSecretKey       = "clouds.yaml"
	// NOTE(dulek): This one is hardcoded in openshift/installer.
	InfrastructureCRDName = "cluster"
)

func GetClusterID(kubeClient client.Client) (string, error) {
	cluster := &confv1.Infrastructure{
		TypeMeta:   metav1.TypeMeta{APIVersion: "config.openshift.io/v1", Kind: "Infrastructure"},
		ObjectMeta: metav1.ObjectMeta{Name: InfrastructureCRDName},
	}

	err := kubeClient.Get(context.TODO(), client.ObjectKey{Name: InfrastructureCRDName}, cluster)
	if err != nil {
		return "", errors.Wrapf(err, "Failed to get Infrastracture CRD %s", InfrastructureCRDName)
	}
	return cluster.Status.InfrastructureName, nil
}

func GetCloudFromSecret(kubeClient client.Client) (clientconfig.Cloud, error) {
	emptyCloud := clientconfig.Cloud{}

	secret := &v1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: CloudsSecret},
	}

	err := kubeClient.Get(context.TODO(), client.ObjectKey{Namespace: CloudsSecretNamespace, Name: CloudsSecret}, secret)
	if err != nil {
		return emptyCloud, errors.Wrapf(err, "Failed to get %s Secret with OpenStack credentials", CloudsSecret)
	}

	content, ok := secret.Data[CloudsSecretKey]
	if !ok {
		return emptyCloud, errors.Errorf("OpenStack credentials secret %v did not contain key %v",
			CloudsSecret, CloudsSecretKey)
	}
	var clouds clientconfig.Clouds
	err = yaml.Unmarshal(content, &clouds)
	if err != nil {
		return emptyCloud, errors.Wrapf(err, "failed to unmarshal clouds credentials stored in secret %v", CloudsSecret)
	}

	return clouds.Clouds[CloudName], nil
}

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

// Looks for a Neutron subnet by name and tag. Fails if subnet is not found
// or multiple subnets match.
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

// Looks for a Neutron subnet by name, tag, network ID, CIDR and optionally
// gateway IP, if it does not exist creates it. Will fail if two subnets match
// all the criteria.
func ensureOpenStackSubnet(client *gophercloud.ServiceClient, name, tag, netId, cidr string, gatewayIp *string) (string, error) {
	dhcp := false
	page, err := subnets.List(client, subnets.ListOpts{
		Name:       name,
		Tags:       tag,
		NetworkID:  netId,
		CIDR:       cidr,
		GatewayIP:  *gatewayIp,
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
		opts := subnets.CreateOpts{
			Name:       name,
			NetworkID:  netId,
			CIDR:       cidr,
			GatewayIP:  gatewayIp,
			IPVersion:  gophercloud.IPv4,
			EnableDHCP: &dhcp,
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

// Looks for a Neutron router by name and tag. Fails if router is not found
// or multiple routers match.
func findOpenStackRouterId(client *gophercloud.ServiceClient, name, tag string) (string, error) {
	page, err := routers.List(client, routers.ListOpts{Name: name, Tags: tag}).AllPages()
	if err != nil {
		return "", errors.Wrap(err, "failed to get router list")
	}
	routerList, err := routers.ExtractRouters(page)
	if err != nil {
		return "", errors.Wrap(err, "failed to extract routers list")
	}

	if len(routerList) == 1 {
		return routerList[0].ID, nil
	} else if len(routerList) == 0 {
		return "", errors.New("router not found")
	} else {
		return "", errors.New("multiple matching routers")
	}
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
func ensureOpenStackSgRule(client *gophercloud.ServiceClient, sgId, remotePrefix string) error {
	opts := rules.CreateOpts{
		SecGroupID:     sgId,
		EtherType:      rules.EtherType4,
		Direction:      rules.DirIngress,
		RemoteIPPrefix: remotePrefix,
	}
	_, err := rules.Create(client, opts).Extract()
	if err != nil {
		if errCode, ok := err.(gophercloud.ErrUnexpectedResponseCode); ok {
			if errCode.Actual == 409 {
				// Ignoring 409 Conflict as that means the rule is already there.
				return nil
			}
		}
		return errors.Wrap(err, "failed to create SG rule")
	}
	return nil
}

// Waits up to 5 minutes for OpenStack LoadBalancer to move into ACTIVE
// provisioning_status. Fails if time runs out.
func waitForOpenStackLb(client *gophercloud.ServiceClient, lbId string) error {
	err := gophercloud.WaitFor(300, func() (bool, error) {
		lb, err := loadbalancers.Get(client, lbId).Extract()
		if err != nil {
			return false, err
		}

		return lb.ProvisioningStatus == "ACTIVE", nil
	})

	return err
}

// Looks for a Octavia load balancer by name, address and subnet ID. If it does
// not exist creates it. Will fail if multiple LB's are matching all criteria.
func ensureOpenStackLb(client *gophercloud.ServiceClient, name, vipAddress, vipSubnetId string) (string, error) {
	page, err := loadbalancers.List(client, loadbalancers.ListOpts{
		Name:        name,
		VipAddress:  vipAddress,
		VipSubnetID: vipSubnetId,
	}).AllPages()
	if err != nil {
		return "", errors.Wrap(err, "failed to get LB list")
	}
	lbs, err := loadbalancers.ExtractLoadBalancers(page)
	if err != nil {
		return "", errors.Wrap(err, "failed to extract LB list")
	}
	if len(lbs) > 1 {
		return "", errors.Errorf("found multiple LB matching name %s, cannot proceed", name)
	} else if len(lbs) == 1 {
		return lbs[0].ID, nil
	} else {
		opts := loadbalancers.CreateOpts{
			Name:        name,
			VipAddress:  vipAddress,
			VipSubnetID: vipSubnetId,
		}
		lb, err := loadbalancers.Create(client, opts).Extract()
		if err != nil {
			return "", errors.Wrap(err, "failed to create LB")
		}

		err = waitForOpenStackLb(client, lb.ID)
		if err != nil {
			return "", errors.Errorf("Timed out waiting for the LB %s to become ready", lb.ID)
		}

		return lb.ID, nil
	}
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
			return "", errors.Errorf("Timed out waiting for the LB %s to become ready", lbId)
		}

		return poolsObj.ID, nil
	}
}

// Looks for a Octavia load balancer pool member by name, address and port. If
// it does not exist creates it. Will fail if multiple LB pool members are
// matching all criteria.
func ensureOpenStackLbPoolMember(client *gophercloud.ServiceClient, name, lbId, poolId,
	address, subnetId string, port int) (string, error) {
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
		}
		poolsObj, err := pools.CreateMember(client, poolId, opts).Extract()
		if err != nil {
			return "", errors.Wrap(err, "failed to create LB member")
		}

		err = waitForOpenStackLb(client, lbId)
		if err != nil {
			return "", errors.Errorf("Timed out waiting for the LB %s to become ready", lbId)
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
		listenerObj, err := listeners.Create(client, opts).Extract()
		if err != nil {
			return "", errors.Wrap(err, "failed to create LB listener")
		}

		err = waitForOpenStackLb(client, lbId)
		if err != nil {
			return "", errors.Errorf("Timed out waiting for the LB %s to become ready", lbId)
		}

		return listenerObj.ID, nil
	}
}

func getProjectID(keystone *gophercloud.ServiceClient, username, projectName string) (string, error) {
	tokenID := keystone.Token()
	proj, err := tokens.Get(keystone, tokenID).ExtractProject()
	if err != nil {
		return "", errors.Wrap(err, "failed to get token")
	}
	return proj.ID, nil
}

// Logs into OpenStack and creates all the resources that are required to run
// Kuryr based on conf NetworkConfigSpec. Basically this includes service
// network and subnet, pods subnetpool, security group and load balancer for
// OpenShift API. Besides that it looks up router and subnet used by OpenShift
// worker nodes (needed to configure Kuryr) and makes sure there's a routing
// between them and created subnets. Also SG rules are added to make sure pod
// subnet can reach nodes and nodes can reach pods and services. The data is
// returned to populate Kuryr's configuration.
func BootstrapKuryr(conf *operv1.NetworkSpec, kubeClient client.Client) (*bootstrap.BootstrapResult, error) {
	log.Print("Kuryr bootstrap started")

	clusterID, err := GetClusterID(kubeClient)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get ClusterID")
	}

	cloud, err := GetCloudFromSecret(kubeClient)
	if err != nil {
		return nil, errors.Wrap(err, "failed to authenticate to OpenStack")
	}

	clientOpts := new(clientconfig.ClientOpts)

	if cloud.AuthInfo != nil {
		clientOpts.AuthInfo = cloud.AuthInfo
		clientOpts.AuthType = cloud.AuthType
		clientOpts.Cloud = cloud.Cloud
		clientOpts.RegionName = cloud.RegionName
	}

	opts, err := clientconfig.AuthOptions(clientOpts)
	if err != nil {
		return nil, errors.Wrap(err, "failed to authenticate to OpenStack")
	}

	provider, err := openstack.AuthenticatedClient(*opts)
	if err != nil {
		return nil, errors.Wrap(err, "failed to authenticate to OpenStack")
	}

	// Kuryr will need ProjectID to be set, let's make sure it's set.
	if cloud.AuthInfo.ProjectID == "" && cloud.AuthInfo.ProjectName != "" {
		keystone, err := openstack.NewIdentityV3(provider, gophercloud.EndpointOpts{})
		if err != nil {
			return nil, errors.Wrap(err, "failed to create Keystone client")
		}

		projectID, err := getProjectID(keystone, cloud.AuthInfo.Username, cloud.AuthInfo.ProjectName)
		if err != nil {
			return nil, errors.Wrap(err, "failed to find project ID")
		}
		cloud.AuthInfo.ProjectID = projectID
	}

	client, err := openstack.NewNetworkV2(provider, gophercloud.EndpointOpts{})
	if err != nil {
		return nil, errors.Wrap(err, "failed to create Neutron client")
	}

	tag := "openshiftClusterID=" + clusterID
	log.Printf("Using %s as resources tag", tag)

	log.Print("Ensuring services network")
	svcNetId, err := ensureOpenStackNetwork(client, "kuryr-service-network", tag)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create service network")
	}
	log.Printf("Services network %s present", svcNetId)

	// Service subnet
	// We need last usable IP from this CIDR. We use first subnet, we don't support multiple entries in Kuryr.
	_, svcNet, err := net.ParseCIDR(conf.ServiceNetwork[0])
	if err != nil {
		return nil, errors.Wrapf(err, "Failed to parse ServiceNetwork CIDR %s", conf.ServiceNetwork[0])
	}

	ip := iputil.LastUsableIP(*svcNet)
	ipStr := ip.String()
	log.Printf("Ensuring services subnet with %s CIDR and %s gateway", conf.ServiceNetwork[0], ipStr)
	svcSubnetId, err := ensureOpenStackSubnet(client, "kuryr-service-subnet", tag,
		svcNetId, conf.ServiceNetwork[0], &ipStr)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create service subnet")
	}
	log.Printf("Services subnet %s present", svcSubnetId)

	// Pod subnetpool
	podSubnetCidrs := make([]string, len(conf.ClusterNetwork))
	for i, cn := range conf.ClusterNetwork {
		podSubnetCidrs[i] = cn.CIDR
	}
	// TODO(dulek): Now we only support one ClusterNetwork, so we take first HostPrefix. If we're to support multiple,
	//              we need to validate if all of them are the same - that's how it can work in OpenStack.
	prefixLen := conf.ClusterNetwork[0].HostPrefix
	log.Printf("Ensuring pod subnetpool with following CIDRs: %v", podSubnetCidrs)
	podSubnetpoolId, err := ensureOpenStackSubnetpool(client, "kuryr-pod-subnetpool", tag, podSubnetCidrs, prefixLen)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create pod subnetpool")
	}
	log.Printf("Pod subnetpool %s present", podSubnetpoolId)

	workerSubnet, err := findOpenStackSubnet(client, fmt.Sprintf("%s-nodes", clusterID), tag)
	if err != nil {
		return nil, errors.Wrap(err, "failed to find worker nodes subnet")
	}
	log.Printf("Found worker nodes subnet %s", workerSubnet.ID)
	routerId, err := findOpenStackRouterId(client, fmt.Sprintf("%s-external-router", clusterID), tag)
	if err != nil {
		return nil, errors.Wrap(err, "failed to find worker nodes router")
	}
	log.Printf("Found worker nodes router %s", routerId)
	ps, err := getOpenStackRouterPorts(client, routerId)
	if err != nil {
		return nil, errors.Wrap(err, "failed list ports of worker nodes router")
	}

	if !lookupOpenStackPort(ps, svcSubnetId) {
		log.Printf("Ensuring service subnet router port with %s IP", ipStr)
		portId, err := ensureOpenStackPort(client, "kuryr-service-subnet-router-port", tag,
			svcNetId, svcSubnetId, ipStr)
		if err != nil {
			return nil, errors.Wrap(err, "failed to create service subnet router port")
		}
		log.Printf("Service subnet router port %s present, adding it as interface.", portId)
		err = ensureOpenStackRouterInterface(client, routerId, nil, &portId)
		if err != nil {
			return nil, errors.Wrap(err, "failed to create service subnet router interface")
		}
	}

	masterSgId, err := findOpenStackSgId(client, fmt.Sprintf("%s-master", clusterID), tag)
	if err != nil {
		return nil, errors.Wrap(err, "failed to find master nodes security group")
	}
	log.Printf("Found master nodes security group %s", masterSgId)
	workerSgId, err := findOpenStackSgId(client, fmt.Sprintf("%s-worker", clusterID), tag)
	if err != nil {
		return nil, errors.Wrap(err, "failed to find worker nodes security group")
	}
	log.Printf("Found worker nodes security group %s", workerSgId)

	log.Print("Ensuring pods security group")
	podSgId, err := ensureOpenStackSg(client, "kuryr-pods-security-group", tag)
	log.Printf("Pods security group %s present", podSgId)

	log.Print("Allowing traffic from masters and nodes to pods")
	// Seems like openshift/installer puts masters on worker subnet, so only this is needed.
	err = ensureOpenStackSgRule(client, podSgId, workerSubnet.CIDR)
	if err != nil {
		return nil, errors.Wrap(err, "failed to add rule opening traffic from workers and masters")
	}
	for _, cidr := range podSubnetCidrs {
		err = ensureOpenStackSgRule(client, masterSgId, cidr)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to add rule opening traffic to masters on %s", cidr)
		}
		err = ensureOpenStackSgRule(client, workerSgId, cidr)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to add rule opening traffic to workers on %s", cidr)
		}
	}
	log.Print("All requried traffic allowed")

	lbClient, err := openstack.NewLoadBalancerV2(provider, gophercloud.EndpointOpts{})
	if err != nil {
		return nil, errors.Wrap(err, "failed to create Octavia client")
	}

	// We need first usable IP from services CIDR
	// This will get us the first one (subnet IP)
	ip = iputil.FirstUsableIP(*svcNet)
	ipStr = ip.String()
	log.Printf("Creating OpenShift API loadbalancer with IP %s", ipStr)
	lbId, err := ensureOpenStackLb(lbClient, "kuryr-api-loadbalancer", ipStr, svcSubnetId)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create OpenShift API loadbalancer")
	}
	log.Printf("OpenShift API loadbalancer %s present", lbId)

	log.Print("Creating OpenShift API loadbalancer pool")
	poolId, err := ensureOpenStackLbPool(lbClient, "kuryr-api-loadbalancer-pool", lbId)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create OpenShift API loadbalancer pool")
	}
	log.Printf("OpenShift API loadbalancer pool %s present", poolId)

	log.Print("Creating OpenShift API loadbalancer listener")
	listenerId, err := ensureOpenStackLbListener(lbClient, "kuryr-api-loadbalancer-listener", lbId, poolId, 443)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create OpenShift API loadbalancer listener")
	}
	log.Printf("OpenShift API loadbalancer listener %s present", listenerId)

	// We need to list all master ports and add them to the LB pool
	log.Print("Creating OpenShift API loadbalancer pool members")
	r, _ := regexp.Compile(fmt.Sprintf("^%s-master-port-[0-9]+$", clusterID))
	portList, err := listOpenStackPortsMatchingPattern(client, tag, r)
	for _, port := range portList {
		if len(port.FixedIPs) > 0 {
			portIp := port.FixedIPs[0].IPAddress
			log.Printf("Found port %s with IP %s", port.ID, portIp)
			memberId, err := ensureOpenStackLbPoolMember(lbClient, port.Name, lbId,
				poolId, portIp, workerSubnet.ID, 6443)
			if err != nil {
				log.Printf("Failed to add port %s to LB pool %s: %s", port.ID, poolId, err)
				continue
			}
			log.Printf("Added member %s to LB pool %s", memberId, poolId)
		} else {
			log.Printf("Matching port %s has no IP", port.ID)
		}
	}

	log.Print("Kuryr bootstrap finished")
	if err != nil {
		return nil, errors.Wrap(err, "failed get OpenShift cluster ID")
	}

	res := bootstrap.BootstrapResult{
		Kuryr: bootstrap.KuryrBootstrapResult{
			ServiceSubnet:     svcSubnetId,
			PodSubnetpool:     podSubnetpoolId,
			WorkerNodesRouter: routerId,
			WorkerNodesSubnet: workerSubnet.ID,
			PodSecurityGroups: []string{podSgId},
			ClusterID:         clusterID,
			OpenStackCloud:    cloud,
		}}
	return &res, nil
}
