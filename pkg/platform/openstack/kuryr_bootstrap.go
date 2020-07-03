package openstack

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	b64 "encoding/base64"
	"fmt"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"k8s.io/api/core/v1"
	"log"
	"net"
	"net/http"
	"regexp"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strings"

	"github.com/Masterminds/semver"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v2"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/identity/v3/tokens"
	"github.com/gophercloud/gophercloud/openstack/loadbalancer/v2/listeners"
	"github.com/gophercloud/gophercloud/openstack/loadbalancer/v2/loadbalancers"
	"github.com/gophercloud/gophercloud/openstack/loadbalancer/v2/monitors"
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
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/platform/openstack/util/cert"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	confv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"

	iputil "github.com/openshift/cluster-network-operator/pkg/util/ip"
)

const (
	CloudsSecret             = "installer-cloud-credentials"
	CloudName                = "openstack"
	CloudsSecretKey          = "clouds.yaml"
	OpenShiftConfigNamespace = "openshift-config"
	UserCABundleConfigMap    = "cloud-provider-config"
	// NOTE(dulek): This one is hardcoded in openshift/installer.
	InfrastructureCRDName                  = "cluster"
	MinOctaviaVersionWithMultipleListeners = "v2.11"
	MinOctaviaVersionWithHTTPSMonitors     = "v2.10"
	MinOctaviaVersionWithTagSupport        = "v2.5"
	MinOctaviaVersionWithTimeouts          = "v2.1"
	KuryrNamespace                         = "openshift-kuryr"
	KuryrConfigMapName                     = "kuryr-config"
	DNSNamespace                           = "openshift-dns"
	DNSServiceName                         = "dns-default"
	etcdClientPort                         = 2379
	etcdServerToServerPort                 = 2380
	dnsPort                                = 53
	apiPort                                = 6443
	routerMetricsPort                      = 1936
	kubeCMMetricsPort                      = 10257
	kubeSchedulerMetricsPort               = 10259
	kubeletMetricsPort                     = 10250
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

	err := kubeClient.Get(context.TODO(), client.ObjectKey{Namespace: names.APPLIED_NAMESPACE, Name: CloudsSecret}, secret)
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

// Looks for a Neutron router by name and tag. Fails if router is not found
// or multiple routers match.
func findOpenStackRouter(client *gophercloud.ServiceClient, name, tag string) (routers.Router, error) {
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
		return empty, errors.New("router not found")
	} else {
		return empty, errors.New("multiple matching routers")
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
		return "", errors.Errorf("Timed out waiting for the LB %s to become ready", lb.ID)
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
			return "", errors.Errorf("Timed out waiting for the LB %s to become ready", lbId)
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

func generateName(name, clusterID string) string {
	return fmt.Sprintf("%s-%s", clusterID, name)
}

func ensureCA(kubeClient client.Client) ([]byte, []byte, error) {
	secret := &v1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: names.KURYR_ADMISSION_CONTROLLER_SECRET},
	}
	err := kubeClient.Get(context.TODO(), client.ObjectKey{Namespace: KuryrNamespace, Name: names.KURYR_ADMISSION_CONTROLLER_SECRET}, secret)
	if err != nil {
		caBytes, keyBytes, err := cert.GenerateCA("Kuryr")
		if err != nil {
			return nil, nil, errors.Wrapf(err, "Failed to generate CA.")
		}
		return caBytes, keyBytes, nil
	} else {
		crtContent, crtok := secret.Data["ca.crt"]
		keyContent, keyok := secret.Data["ca.key"]
		if !crtok || !keyok {
			caBytes, keyBytes, err := cert.GenerateCA("Kuryr")
			if err != nil {
				return nil, nil, errors.Wrapf(err, "Failed to generate CA.")
			}
			return caBytes, keyBytes, nil
		}
		return crtContent, keyContent, nil
	}
}

func ensureCertificate(kubeClient client.Client, caPEM []byte, privateKey []byte) ([]byte, []byte, error) {
	secret := &v1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: names.KURYR_WEBHOOK_SECRET},
	}
	err := kubeClient.Get(context.TODO(), client.ObjectKey{Namespace: KuryrNamespace, Name: names.KURYR_WEBHOOK_SECRET}, secret)
	if err != nil {
		caBytes, keyBytes, err := cert.GenerateCertificate("Kuryr", []string{"kuryr-dns-admission-controller.openshift-kuryr", "kuryr-dns-admission-controller.openshift-kuryr.svc"}, caPEM, privateKey)
		if err != nil {
			return nil, nil, errors.Wrapf(err, "Failed to generate CA.")
		}
		return caBytes, keyBytes, nil
	} else {
		crtContent, crtok := secret.Data["tls.crt"]
		keyContent, keyok := secret.Data["tls.key"]
		if !crtok || !keyok {
			caBytes, keyBytes, err := cert.GenerateCertificate("Kuryr", []string{"kuryr-dns-admission-controller.openshift-kuryr", "kuryr-dns-admission-controller.openshift-kuryr.svc"}, caPEM, privateKey)
			if err != nil {
				return nil, nil, errors.Wrapf(err, "Failed to generate CA.")
			}
			return caBytes, keyBytes, nil
		}
		return crtContent, keyContent, nil
	}
}

func getConfigMap(kubeClient client.Client, namespace, name string) (*v1.ConfigMap, error) {
	cm := &v1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
	err := kubeClient.Get(context.TODO(), client.ObjectKey{Namespace: namespace, Name: name}, cm)
	if err != nil {
		return nil, err
	}
	return cm, nil
}

func getUserCACert(kubeClient client.Client) (string, error) {
	cm := &v1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: UserCABundleConfigMap},
	}
	err := kubeClient.Get(context.TODO(), client.ObjectKey{Namespace: OpenShiftConfigNamespace, Name: UserCABundleConfigMap}, cm)
	if err != nil {
		return "", err
	}
	return string(cm.Data["ca-bundle.pem"]), nil
}

func getSavedAnnotation(kubeClient client.Client, annotation string) (string, error) {
	cm, err := getConfigMap(kubeClient, KuryrNamespace, KuryrConfigMapName)
	if err != nil {
		return "", err
	}
	return cm.Annotations[annotation], nil
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
	kc := conf.DefaultNetwork.KuryrConfig

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
	provider, err := openstack.NewClient(opts.IdentityEndpoint)
	if err != nil {
		return nil, errors.Wrap(err, "failed to authenticate to OpenStack")
	}

	// We need to fetch user-provided OpenStack cloud CA certificate and make gophercloud use it.
	// Also it'll get injected into a ConfigMap mounted into kuryr-controller later on.
	userCACert, err := getUserCACert(kubeClient)
	if userCACert != "" {
		certPool, err := x509.SystemCertPool()
		if err == nil {
			certPool.AppendCertsFromPEM([]byte(userCACert))
			client := http.Client{
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{
						RootCAs: certPool,
					},
				},
			}
			provider.HTTPClient = client
		}
	}

	err = openstack.Authenticate(provider, *opts)
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
	svcNetId, err := ensureOpenStackNetwork(client, generateName("kuryr-service-network", clusterID), tag)
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

	openStackSvcCIDR := kc.OpenStackServiceNetwork
	_, openStackSvcNet, _ := net.ParseCIDR(openStackSvcCIDR)
	allocationRanges := iputil.UsableNonOverlappingRanges(*openStackSvcNet, *svcNet)
	// OpenShift will use svcNet range. In allocationRanges we have parts of openStackSvcNet that are not overlapping
	// with svcNet. We will put gatewayIP on the highest usable IP from those ranges. We need to exclude that IP from
	// the ranges we pass to Neutron or it will complain.
	gatewayIP := allocationRanges[len(allocationRanges)-1].End
	allocationRanges[len(allocationRanges)-1].End = iputil.IterateIP4(gatewayIP, -1)

	log.Printf("Ensuring services subnet with %s CIDR (services from %s) and %s gateway with allocation pools %+v",
		openStackSvcCIDR, conf.ServiceNetwork[0], gatewayIP.String(), allocationRanges)
	svcSubnetId, err := ensureOpenStackSubnet(client, generateName("kuryr-service-subnet", clusterID), tag,
		svcNetId, openStackSvcCIDR, gatewayIP.String(), allocationRanges)
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
	podSubnetpoolId, err := ensureOpenStackSubnetpool(client, generateName("kuryr-pod-subnetpool", clusterID), tag,
		podSubnetCidrs, prefixLen)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create pod subnetpool")
	}
	log.Printf("Pod subnetpool %s present", podSubnetpoolId)

	workerSubnet, err := findOpenStackSubnet(client, generateName("nodes", clusterID), tag)
	if err != nil {
		return nil, errors.Wrap(err, "failed to find worker nodes subnet")
	}
	log.Printf("Found worker nodes subnet %s", workerSubnet.ID)
	router, err := findOpenStackRouter(client, generateName("external-router", clusterID), tag)
	if err != nil {
		return nil, errors.Wrap(err, "failed to find worker nodes router")
	}
	routerId := router.ID
	externalNetwork := router.GatewayInfo.NetworkID
	log.Printf("Found worker nodes router %s", routerId)
	ps, err := getOpenStackRouterPorts(client, routerId)
	if err != nil {
		return nil, errors.Wrap(err, "failed list ports of worker nodes router")
	}

	if !lookupOpenStackPort(ps, svcSubnetId) {
		log.Printf("Ensuring service subnet router port with %s IP", gatewayIP.String())
		portId, err := ensureOpenStackPort(client, generateName("kuryr-service-subnet-router-port", clusterID), tag,
			svcNetId, svcSubnetId, gatewayIP.String())
		if err != nil {
			return nil, errors.Wrap(err, "failed to create service subnet router port")
		}
		log.Printf("Service subnet router port %s present, adding it as interface.", portId)
		err = ensureOpenStackRouterInterface(client, routerId, nil, &portId)
		if err != nil {
			return nil, errors.Wrap(err, "failed to create service subnet router interface")
		}
	}

	masterSgId, err := findOpenStackSgId(client, generateName("master", clusterID), tag)
	if err != nil {
		return nil, errors.Wrap(err, "failed to find master nodes security group")
	}
	log.Printf("Found master nodes security group %s", masterSgId)
	workerSgId, err := findOpenStackSgId(client, generateName("worker", clusterID), tag)
	if err != nil {
		return nil, errors.Wrap(err, "failed to find worker nodes security group")
	}
	log.Printf("Found worker nodes security group %s", workerSgId)

	log.Print("Ensuring pods security group")
	podSgId, err := ensureOpenStackSg(client, generateName("kuryr-pods-security-group", clusterID), tag)
	log.Printf("Pods security group %s present", podSgId)

	type sgRule struct {
		sgId     string
		cidr     string
		minPort  int
		maxPort  int
		protocol rules.RuleProtocol
	}

	var sgRules = []sgRule{
		{podSgId, "0.0.0.0/0", 0, 0, rules.ProtocolTCP},
		{masterSgId, openStackSvcCIDR, etcdClientPort, etcdClientPort, rules.ProtocolTCP},
		{masterSgId, openStackSvcCIDR, apiPort, apiPort, rules.ProtocolTCP},
		// NOTE (maysamacedo): Splitting etcd sg port ranges in different
		// rules to avoid the issue of constant leader election changes
		{masterSgId, workerSubnet.CIDR, etcdClientPort, etcdClientPort, rules.ProtocolTCP},
		{masterSgId, workerSubnet.CIDR, etcdServerToServerPort, etcdServerToServerPort, rules.ProtocolTCP},
	}

	var decommissionedRules = []sgRule{
		{podSgId, workerSubnet.CIDR, 0, 0, ""},
		{masterSgId, openStackSvcCIDR, etcdClientPort, etcdServerToServerPort, rules.ProtocolTCP},
		// NOTE(maysamacedo): This sg rule is created by the installer. We need to remove it and
		// create two more, each with a unique port from the range.
		{masterSgId, workerSubnet.CIDR, etcdClientPort, etcdServerToServerPort, rules.ProtocolTCP},
	}

	for _, cidr := range podSubnetCidrs {
		sgRules = append(sgRules,
			sgRule{masterSgId, cidr, etcdClientPort, etcdClientPort, rules.ProtocolTCP},
			sgRule{masterSgId, cidr, dnsPort, dnsPort, rules.ProtocolTCP},
			sgRule{masterSgId, cidr, dnsPort, dnsPort, rules.ProtocolUDP},
			sgRule{workerSgId, cidr, dnsPort, dnsPort, rules.ProtocolTCP},
			sgRule{workerSgId, cidr, dnsPort, dnsPort, rules.ProtocolUDP},
			sgRule{workerSgId, cidr, routerMetricsPort, routerMetricsPort, rules.ProtocolTCP},
			sgRule{masterSgId, cidr, kubeCMMetricsPort, kubeCMMetricsPort, rules.ProtocolTCP},
			sgRule{masterSgId, cidr, kubeSchedulerMetricsPort, kubeSchedulerMetricsPort, rules.ProtocolTCP},
			sgRule{masterSgId, cidr, kubeletMetricsPort, kubeletMetricsPort, rules.ProtocolTCP},
			sgRule{workerSgId, cidr, kubeletMetricsPort, kubeletMetricsPort, rules.ProtocolTCP},
			// NOTE(dulek): I honestly don't know why this is so broad range, but I took it from SGs installer sets
			//              for workers and masters. In general point was to open 9192 so that metrics of
			//              cluster-autoscaler-operator were rechable.
			sgRule{masterSgId, cidr, 9000, 9999, rules.ProtocolTCP},
			sgRule{masterSgId, cidr, 9000, 9999, rules.ProtocolUDP},
			sgRule{workerSgId, cidr, 9000, 9999, rules.ProtocolTCP},
			sgRule{workerSgId, cidr, 9000, 9999, rules.ProtocolUDP},
		)

		decommissionedRules = append(decommissionedRules,
			sgRule{masterSgId, cidr, 0, 0, ""},
			sgRule{workerSgId, cidr, 0, 0, ""},
		)
	}

	log.Print("Allowing required traffic")
	for _, rule := range sgRules {
		err = ensureOpenStackSgRule(client, rule.sgId, rule.cidr, rule.minPort, rule.maxPort, rule.protocol)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to add %s rule opening traffic from %s in security group %s on ports %d:%d",
				rule.protocol, rule.cidr, rule.sgId, rule.minPort, rule.maxPort)
		}
	}
	log.Print("All requried traffic allowed")

	// It may happen that we tightened some SG rules in an upgrade, we need to make sure to remove the ones that are
	// not expected anymore.
	log.Print("Removing old SG rules")
	existingRules, err := listOpenStackSgRules(client, tag)
	if err != nil {
		return nil, errors.Wrap(err, "failed to list SG rules")
	}

	for _, existingRule := range existingRules {
		// Need to convert to "our" format for easy comparisons.
		rule := sgRule{existingRule.SecGroupID, existingRule.RemoteIPPrefix, existingRule.PortRangeMin,
			existingRule.PortRangeMax, rules.RuleProtocol(existingRule.Protocol)}
		for _, decommissionedRule := range decommissionedRules {
			if decommissionedRule == rule {
				log.Printf("Removing decommisioned rule %s (%s, %d, %d, %s) from SG %s", existingRule.ID,
					existingRule.RemoteIPPrefix, existingRule.PortRangeMin, existingRule.PortRangeMax,
					existingRule.Protocol, existingRule.SecGroupID)
				err = rules.Delete(client, existingRule.ID).ExtractErr()
				if err != nil {
					return nil, errors.Wrapf(err, "Could not delete SG rule %s", existingRule.ID)
				}
				break
			}
		}
	}
	log.Print("All old SG rules removed")

	lbClient, err := openstack.NewLoadBalancerV2(provider, gophercloud.EndpointOpts{})
	if err != nil {
		return nil, errors.Wrap(err, "failed to create Octavia client")
	}

	// We need first usable IP from services CIDR
	// This will get us the first one (subnet IP)
	apiIP := iputil.FirstUsableIP(*svcNet)
	log.Printf("Creating OpenShift API loadbalancer with IP %s", apiIP.String())
	lbId, err := ensureOpenStackLb(lbClient, generateName("kuryr-api-loadbalancer", clusterID), apiIP.String(), svcSubnetId, tag)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create OpenShift API loadbalancer")
	}
	log.Printf("OpenShift API loadbalancer %s present", lbId)

	log.Print("Creating OpenShift API loadbalancer pool")
	poolId, err := ensureOpenStackLbPool(lbClient, generateName("kuryr-api-loadbalancer-pool", clusterID), lbId)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create OpenShift API loadbalancer pool")
	}
	log.Printf("OpenShift API loadbalancer pool %s present", poolId)

	log.Print("Creating OpenShift API loadbalancer health monitor")
	monitorId, err := ensureOpenStackLbMonitor(lbClient, "kuryr-api-loadbalancer-monitor", poolId)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create OpenShift API loadbalancer health monitor")
	}
	log.Printf("OpenShift API loadbalancer health monitor %s present", monitorId)

	log.Print("Creating OpenShift API loadbalancer listener")
	listenerId, err := ensureOpenStackLbListener(lbClient, generateName("kuryr-api-loadbalancer-listener", clusterID),
		lbId, poolId, 443)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create OpenShift API loadbalancer listener")
	}
	log.Printf("OpenShift API loadbalancer listener %s present", listenerId)

	// We need to list all master ports and add them to the LB pool. We also add the
	// bootstrap node port as for a portion of installation only it provides access to
	// the API. With healthchecks enabled for the pool we'll get masters added automatically
	// when they're up and ready.
	log.Print("Creating OpenShift API loadbalancer pool members")
	r, _ := regexp.Compile(fmt.Sprintf("^%s-(master-port-[0-9]+|bootstrap-port)$", clusterID))
	portList, err := listOpenStackPortsMatchingPattern(client, tag, r)
	addresses := make([]string, 0)
	for _, port := range portList {
		if len(port.FixedIPs) > 0 {
			portIp := port.FixedIPs[0].IPAddress
			addresses = append(addresses, portIp)
			log.Printf("Found port %s with IP %s", port.ID, portIp)

			// We want bootstrap to stop being used as soon as possible, as it will serve
			// outdated data during the bootstrap transition period.
			weight := 100
			if strings.HasSuffix(port.Name, "bootstrap-port") {
				weight = 1
			}

			memberId, err := ensureOpenStackLbPoolMember(lbClient, port.Name, lbId,
				poolId, portIp, svcSubnetId, 6443, weight)
			if err != nil {
				log.Printf("Failed to add port %s (%s) to LB pool %s: %s", port.ID, port.Name, poolId, err)
				continue
			}
			log.Printf("Added member %s to LB pool %s", memberId, poolId)
		} else {
			log.Printf("Matching port %s has no IP", port.ID)
		}
	}

	err = purgeOpenStackLbPoolMember(lbClient, poolId, addresses)
	if err != nil {
		return nil, errors.Wrap(err, "Failed on purging invalid LB members from LB pool")
	}

	log.Print("Ensuring certificates")
	ca, key, err := ensureCA(kubeClient)
	if err != nil {
		return nil, errors.Wrap(err, "failed to ensure CA")
	}
	webhookCert, webhookKey, err := ensureCertificate(kubeClient, ca, key)
	if err != nil {
		return nil, errors.Wrap(err, "failed to ensure Certificate")
	}

	log.Print("Checking Double Listeners Octavia support")
	octaviaMultipleListenersSupport, err := IsOctaviaVersionSupported(lbClient, MinOctaviaVersionWithMultipleListeners)
	if err != nil {
		return nil, errors.Wrap(err, "failed to determine if Octavia supports double listeners")
	}

	octaviaVersion, err := getSavedAnnotation(kubeClient, names.KuryrOctaviaVersionAnnotation)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, errors.Wrap(err, "failed to get kuryr-config ConfigMap")
	}

	maxOctaviaVersion, err := getMaxOctaviaAPIVersion(lbClient)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get max octavia api version")
	}

	// In case the Kuryr config-map is annotated with an Octavia version different
	// than the current Octavia version, and older than the version that multiple
	// listeners becomes available and the Octavia provider is amphora, an Octavia
	// upgrade happened and UDP listeners are now allowed to be created.
	// By recreating the OpenShift DNS service a new load balancer amphora is in
	// place with all required listeners.
	log.Print("Checking Octavia upgrade happened")
	if octaviaVersion != "" {
		savedOctaviaVersion := semver.MustParse(octaviaVersion)
		multipleListenersVersion := semver.MustParse(MinOctaviaVersionWithMultipleListeners)
		if !savedOctaviaVersion.Equal(maxOctaviaVersion) && savedOctaviaVersion.LessThan(multipleListenersVersion) && octaviaMultipleListenersSupport {
			dnsService := &v1.Service{
				TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
				ObjectMeta: metav1.ObjectMeta{Name: DNSServiceName, Namespace: DNSNamespace},
			}
			err := kubeClient.Delete(context.TODO(), dnsService)
			if err != nil {
				return nil, errors.Wrapf(err, "Failed to delete %s Service", DNSServiceName)
			}
		}
	}
	octaviaVersion = maxOctaviaVersion.Original()

	log.Print("Kuryr bootstrap finished")

	res := bootstrap.BootstrapResult{
		Kuryr: bootstrap.KuryrBootstrapResult{
			ServiceSubnet:            svcSubnetId,
			PodSubnetpool:            podSubnetpoolId,
			WorkerNodesRouter:        routerId,
			WorkerNodesSubnet:        workerSubnet.ID,
			PodSecurityGroups:        []string{podSgId},
			ExternalNetwork:          externalNetwork,
			ClusterID:                clusterID,
			OctaviaMultipleListeners: octaviaMultipleListenersSupport,
			OpenStackCloud:           cloud,
			OctaviaVersion:           octaviaVersion,
			WebhookCA:                b64.StdEncoding.EncodeToString(ca),
			WebhookCAKey:             b64.StdEncoding.EncodeToString(key),
			WebhookKey:               b64.StdEncoding.EncodeToString(webhookKey),
			WebhookCert:              b64.StdEncoding.EncodeToString(webhookCert),
			UserCACert:               userCACert,
		}}
	return &res, nil
}
