package operconfig

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/apply"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
)

const defaultEgressFirewallName = "default"

var gvrEgressFirewall = schema.GroupVersionResource{Group: "k8s.ovn.org", Version: "v1", Resource: "egressfirewalls"}
var gvrEgressNetworkPolicy = schema.GroupVersionResource{Group: "network.openshift.io", Version: "v1", Resource: "egressnetworkpolicies"}
var gvrEgressIp = schema.GroupVersionResource{Group: "k8s.ovn.org", Version: "v1", Resource: "egressips"}
var gvrHostSubnet = schema.GroupVersionResource{Group: "network.openshift.io", Version: "v1", Resource: "hostsubnets"}
var gvrNetnamespace = schema.GroupVersionResource{Group: "network.openshift.io", Version: "v1", Resource: "netnamespaces"}
var gvrCloudPrivateIPConfig = schema.GroupVersionResource{Group: "cloud.network.openshift.io", Version: "v1", Resource: "cloudprivateipconfigs"}

func migrateEgressFirewallCRs(ctx context.Context, operConfig *operv1.Network, client cnoclient.Client) error {
	switch operConfig.Spec.Migration.NetworkType {
	case string(operv1.NetworkTypeOVNKubernetes):
		return convertEgressNetworkPolicyToEgressFirewall(ctx, client)
	case string(operv1.NetworkTypeOpenShiftSDN):
		return convertEgressFirewallToEgressNetworkPolicy(ctx, client)
	}

	return nil
}

func migrateEgressIpCRs(ctx context.Context, operConfig *operv1.Network, client cnoclient.Client) error {
	switch operConfig.Spec.Migration.NetworkType {
	case string(operv1.NetworkTypeOVNKubernetes):
		return convertSdnEgressIpToOvnEgressIp(ctx, client)
	case string(operv1.NetworkTypeOpenShiftSDN):
		return convertOvnEgressIpToSdnEgressIp(ctx, client)
	}

	return nil
}

func convertEgressNetworkPolicyToEgressFirewall(ctx context.Context, client cnoclient.Client) error {
	enpList, err := client.Default().Dynamic().Resource(gvrEgressNetworkPolicy).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, enp := range enpList.Items {
		log.Printf("Convert EgressNetworkPolicy %s/%s", enp.GetNamespace(), enp.GetName())
		spec, ok := enp.Object["spec"]
		if !ok {
			return fmt.Errorf("fail to retrieve spec from EgressNetworkPolicy %s/%s", enp.GetNamespace(), enp.GetName())
		}

		egressFirewall := &uns.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "k8s.ovn.org/v1",
				"kind":       "EgressFirewall",
				"metadata": map[string]interface{}{
					// The name for the EgressFirewall CR must be 'default', other values are not allowed.
					"name":      defaultEgressFirewallName,
					"namespace": enp.GetNamespace(),
				},
				"spec": spec,
			},
		}
		if err := apply.ApplyObject(ctx, client, egressFirewall, ""); err != nil {
			return err
		}
	}
	return nil
}

func convertEgressFirewallToEgressNetworkPolicy(ctx context.Context, client cnoclient.Client) error {
	efList, err := client.Default().Dynamic().Resource(gvrEgressFirewall).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, ef := range efList.Items {
		log.Printf("Convert EgressNetworkPolicy %s/%s", ef.GetNamespace(), ef.GetName())
		spec, ok := ef.Object["spec"]
		if !ok {
			return fmt.Errorf("fail to retrieve spec from EgressFirewall %s/%s", ef.GetNamespace(), ef.GetName())
		}

		specText, _ := json.Marshal(spec)
		if strings.Contains(string(specText), "ports") {
			log.Println("\"ports\" is not supported in EgressNetworkPolicy, this field will be ignored.")
		}

		egressNetworkPolicy := &uns.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "network.openshift.io/v1",
				"kind":       "EgressNetworkPolicy",
				"metadata": map[string]interface{}{
					"name":      ef.GetName(),
					"namespace": ef.GetNamespace(),
				},
				"spec": spec,
			},
		}
		if err := apply.ApplyObject(ctx, client, egressNetworkPolicy, ""); err != nil {
			return err
		}
	}
	return nil
}

func convertSdnEgressIpToOvnEgressIp(ctx context.Context, client cnoclient.Client) error {

	// 1. query for hostsubnets and netnamespaces
	hsnList, err := client.Default().Dynamic().Resource(gvrHostSubnet).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	nnsList, err := client.Default().Dynamic().Resource(gvrNetnamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}

	// 2. iterate through hostsubnets
	//    - any with egressIP configured will cause update to node label "egress-assignable"
	hostSubnetFound := false
	for _, hsn := range hsnList.Items {
		if hostSubnetHasEgressIpConfigManual(hsn) {
			hostSubnetFound = true
			// manual config detected, translating to ovn
			// (NOTE: manual configuration does not have a direct path to OVN, translation will not be 1 to 1.)
			// - mark node from hostsubnet with annotation as follows:
			//    k8s.ovn.org/egress-assignable: ""
			if err := labelNodeAndRemoveHostSubnetConfig(ctx, client, hsn); err != nil {
				return err
			}
		} else if hostSubnetHasEgressIpConfigAutomatic(hsn) {
			hostSubnetFound = true
			// automatic config detected, translating to ovn
			// - mark node from hostsubnet with annotation as follows:
			//    k8s.ovn.org/egress-assignable: ""
			if err := labelNodeAndRemoveHostSubnetConfig(ctx, client, hsn); err != nil {
				return err
			}
		}

	}
	if !hostSubnetFound {
		return fmt.Errorf("did not find a hostsubnet object with egressIP configured")
	}

	// 3. iterate through netnamespaces
	//    - any with egressIP configured will cause an egressIP ovn resource to be created via k8s api
	//    - a corresponding namespace label will be added to match the egressIP resource's namespace selector field
	for i, nns := range nnsList.Items {
		if netNamespaceHasEgressIpConfig(nns) {
			// config detected, translating to ovn
			// - delete cloudprivateipconfig if it exists
			// - create egressip custom resource for ovn
			if err := deleteCloudPrivateIpConfigs(ctx, client, nns.Object["egressIPs"].([]interface{})); err != nil {
				return err
			}

			egressIpName := fmt.Sprint("egressip-", i)

			egressIP := &uns.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "k8s.ovn.org/v1",
					"kind":       "EgressIP",
					"metadata": map[string]interface{}{
						"name": egressIpName,
					},
					"spec": map[string]interface{}{
						"egressIPs": nns.Object["egressIPs"].([]interface{}),
						"namespaceSelector": map[string]interface{}{
							"matchLabels": map[string]interface{}{
								"kubernetes.io/metadata.name": nns.Object["netname"],
							},
						},
					},
				},
			}
			if err := apply.ApplyObject(ctx, client, egressIP, ""); err != nil {
				return err
			}

			// now remove egressIP config from netnamespace
			nns.Object["egressIPs"] = nil
			if err := apply.ApplyObject(ctx, client, &nns, ""); err != nil {
				return err
			}
		}
	}

	return nil // success
}

func convertOvnEgressIpToSdnEgressIp(ctx context.Context, client cnoclient.Client) error {
	egressIpRollbackReady, err := sdnEgressIpResourcesReady(ctx, client)
	if !egressIpRollbackReady {
		return nil // wait for all SDN egressIP resources to be created before rollback
	} else if err != nil {
		return err
	}

	// 1. query for egressips
	eipList, err := client.Default().Dynamic().Resource(gvrEgressIp).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}

	// 2. query for nodes
	nodeList, err := client.Default().Kubernetes().CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}

	// 3. iterate through egressIP objects and generate netnamespaces
	//    - value of `namespaceSelector` will determine netnamespace's name
	//    - egressIP list will be copied to egressIPs field for netnamespace
	for _, eip := range eipList.Items {
		// translate egressIPs to SDN netnamespaces
		// - delete cloudprivateipconfig if it exists
		// - create egressip to netnamespace resources for sdn

		egressIps := eip.Object["spec"].(map[string]interface{})["egressIPs"].([]interface{})

		if err := deleteCloudPrivateIpConfigs(ctx, client, egressIps); err != nil {
			return err
		}

		eipNamespace := eip.Object["spec"].(map[string]interface{})["namespaceSelector"].(map[string]interface{})["matchLabels"].(map[string]interface{})["kubernetes.io/metadata.name"]
		eipNamespaceStr := fmt.Sprintf("%v", eipNamespace)
		nns, err := client.Default().Dynamic().Resource(gvrNetnamespace).Get(ctx, eipNamespaceStr, metav1.GetOptions{})
		if err != nil {
			return err
		}
		nnsResourceVersion := nns.Object["metadata"].(map[string]interface{})["resourceVersion"]
		nnsNetId := nns.Object["netid"]
		netName := eip.Object["spec"].(map[string]interface{})["namespaceSelector"].(map[string]interface{})["matchLabels"].(map[string]interface{})["kubernetes.io/metadata.name"]

		netnamespace := &uns.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "network.openshift.io/v1",
				"kind":       "NetNamespace",
				"metadata": map[string]interface{}{
					"name":            netName,
					"resourceVersion": nnsResourceVersion,
				},
				"netid":     nnsNetId,
				"netname":   netName,
				"egressIPs": egressIps,
			},
		}
		if err := apply.ApplyObject(ctx, client, netnamespace, ""); err != nil {
			return err
		}
	}

	// 4. iterate through nodes and generate hostsubnets for any that are egress-assignable
	for _, node := range nodeList.Items {
		if _, ok := node.Labels["k8s.ovn.org/egress-assignable"]; ok {
			// parse out node's subnet from egressip annotation
			egressIpConfigParsed := strings.SplitAfter(node.Annotations["cloud.network.openshift.io/egress-ipconfig"], "\"ipv4\":\"")
			nodeSubnet := egressIpConfigParsed[1]
			nodeSubnet = nodeSubnet[:strings.IndexByte(nodeSubnet, '"')]
			nodeSubnetArr := []string{nodeSubnet}

			hostIpParsed := strings.SplitAfter(node.Annotations["k8s.ovn.org/host-addresses"], "[\"")
			hostIp := hostIpParsed[1]
			hostIp = hostIp[:strings.IndexByte(hostIp, '"')]

			hsn, err := client.Default().Dynamic().Resource(gvrHostSubnet).Get(ctx, node.Name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			hsnResourceVersion := hsn.Object["metadata"].(map[string]interface{})["resourceVersion"]
			hsnSubnet := hsn.Object["subnet"]

			hostSubnet := &uns.Unstructured{
				Object: map[string]interface{}{
					"apiVersion":  "network.openshift.io/v1",
					"kind":        "HostSubnet",
					"egressCIDRs": nodeSubnetArr,
					"host":        node.Name,
					"hostIP":      hostIp,
					"metadata": map[string]interface{}{
						"name":            node.Name,
						"resourceVersion": hsnResourceVersion,
					},
					"subnet": hsnSubnet,
				},
			}
			if err := apply.ApplyObject(ctx, client, hostSubnet, ""); err != nil {
				return err
			}
		}
	}

	return nil // success
}

func sdnEgressIpResourcesReady(ctx context.Context, client cnoclient.Client) (bool, error) {
	// get all nodes
	// get all hostsubnets
	// iterate over all node names and return false if any of them don't have an associated hostsubnet
	nodeList, err := client.Default().Kubernetes().CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, err
	}
	hostSubnetList, err := client.Default().Dynamic().Resource(gvrHostSubnet).List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, err
	}

	for _, node := range nodeList.Items {
		found := false
		for _, hostSubnet := range hostSubnetList.Items {
			if hostSubnet.Object["host"] == node.Name {
				found = true
				break
			}
		}
		if !found {
			return false, nil
		}
	}

	result, err := netNamespacesExistForAllNamespaces(ctx, client)
	if !result {
		return false, nil
	} else if err != nil {
		return false, err
	}

	return true, nil
}

func netNamespacesExistForAllNamespaces(ctx context.Context, client cnoclient.Client) (bool, error) {
	// get all namespaces
	// get all netnamespaces
	// iterate over all namespaces and return false if any of them don't have an associated netnamespace
	namespaceList, err := client.Default().Kubernetes().CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, err
	}
	netnamespaceList, err := client.Default().Dynamic().Resource(gvrNetnamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, err
	}

	for _, namespace := range namespaceList.Items {
		found := false
		for _, netnamespace := range netnamespaceList.Items {
			if netnamespace.Object["netname"] == namespace.Name {
				found = true
				break
			}
		}
		if !found {
			return false, nil
		}
	}

	return true, nil
}

func netNamespaceHasEgressIpConfig(nns uns.Unstructured) bool {
	if nns.Object["egressIPs"] != nil {
		return len(nns.Object["egressIPs"].([]interface{})) > 0
	}
	return false
}

func hostSubnetHasEgressIpConfigAutomatic(hsn uns.Unstructured) bool {
	if hsn.Object["egressIPs"] != nil && hsn.Object["egressCIDRs"] != nil {
		return len(hsn.Object["egressIPs"].([]interface{})) > 0 && len(hsn.Object["egressCIDRs"].([]interface{})) > 0
	}
	return false
}

func hostSubnetHasEgressIpConfigManual(hsn uns.Unstructured) bool {
	if hsn.Object["egressIPs"] != nil && hsn.Object["egressCIDRs"] != nil {
		return len(hsn.Object["egressIPs"].([]interface{})) > 0 && len(hsn.Object["egressCIDRs"].([]interface{})) <= 0
	}
	return false
}

func labelNodeAndRemoveHostSubnetConfig(ctx context.Context, client cnoclient.Client, hsn uns.Unstructured) error {
	// label node as egressassignable
	hostStr := fmt.Sprintf("%v", hsn.Object["host"])
	nodeObj, err := client.Default().Kubernetes().CoreV1().Nodes().Get(ctx, hostStr, metav1.GetOptions{})
	if err != nil {
		return err
	}

	if _, ok := nodeObj.Labels["k8s.ovn.org/egress-assignable"]; !ok {
		nodeObj.Labels["k8s.ovn.org/egress-assignable"] = ""
		if err := apply.ApplyObject(ctx, client, nodeObj, ""); err != nil {
			return err
		}
	}

	// now remove egressIP config from hostsubnet
	hsn.Object["egressCIDRs"] = nil
	hsn.Object["egressIPs"] = nil
	if err := apply.ApplyObject(ctx, client, &hsn, ""); err != nil {
		return err
	}

	return nil
}

func deleteCloudPrivateIpConfigs(ctx context.Context, client cnoclient.Client, egressIps []interface{}) error {
	for _, egressIp := range egressIps {
		egressIpStr := fmt.Sprintf("%v", egressIp)
		// check if cloudprivateipconfig exists before attempting to delete
		if _, err := client.Default().Dynamic().Resource(gvrCloudPrivateIPConfig).Get(ctx, egressIpStr, metav1.GetOptions{}); err != nil {
			errMessage := fmt.Sprintf("cloudprivateipconfigs.cloud.network.openshift.io \"%s\" not found", egressIpStr)
			if err.Error() == errMessage {
				continue
			} else {
				return err
			}
		}
		// if it exists, delete it.
		if err := client.Default().Dynamic().Resource(gvrCloudPrivateIPConfig).Delete(ctx, egressIpStr, metav1.DeleteOptions{}); err != nil {
			return err
		}
	}
	return nil
}
