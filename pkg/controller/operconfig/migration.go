package operconfig

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"

	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/apply"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
)

const defaultEgressFirewallName = "default"
const egressCIDRAnnotationName = "networkoperator.openshift.io/sdn-to-ovn-migration"
const egressIPNodeConfig = "cloud.network.openshift.io/egress-ipconfig"
const egressAssignable = "k8s.ovn.org/egress-assignable"
const multicastEnabledSDN = "netnamespace.network.openshift.io/multicast-enabled"
const multicastEnabledOVN = "k8s.ovn.org/multicast-enabled"

var gvrEgressFirewall = schema.GroupVersionResource{Group: "k8s.ovn.org", Version: "v1", Resource: "egressfirewalls"}
var gvrEgressNetworkPolicy = schema.GroupVersionResource{Group: "network.openshift.io", Version: "v1", Resource: "egressnetworkpolicies"}
var gvrEgressIp = schema.GroupVersionResource{Group: "k8s.ovn.org", Version: "v1", Resource: "egressips"}
var gvrHostSubnet = schema.GroupVersionResource{Group: "network.openshift.io", Version: "v1", Resource: "hostsubnets"}
var gvrNetnamespace = schema.GroupVersionResource{Group: "network.openshift.io", Version: "v1", Resource: "netnamespaces"}
var gvrCloudPrivateIPConfig = schema.GroupVersionResource{Group: "cloud.network.openshift.io", Version: "v1", Resource: "cloudprivateipconfigs"}

type NodeEgressIpConfig struct {
	Interface string
	IfAddr    map[string]string
	Capacity  map[string]int
}

type OVNMigrationNodeAnnotation struct {
	EgressCIDRs []string
}

func migrateMulticastEnablement(ctx context.Context, operConfig *operv1.Network, client cnoclient.Client) error {
	switch operConfig.Spec.Migration.NetworkType {
	case string(operv1.NetworkTypeOVNKubernetes):
		return enableMulticastOVN(ctx, client)
	case string(operv1.NetworkTypeOpenShiftSDN):
		return enableMulticastSDN(ctx, client)
	}

	return nil
}

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
		egressIpList, netNamespaceList, err := convertSdnEgressIpToOvnEgressIp(ctx, client)
		if err != nil {
			return err
		}
		return applyEgressIpList(ctx, client, egressIpList, netNamespaceList)
	case string(operv1.NetworkTypeOpenShiftSDN):
		return convertOvnEgressIpToSdnEgressIp(ctx, client)
	}

	return nil
}

func enableMulticastOVN(ctx context.Context, client cnoclient.Client) error {
	// 1. query for netnamespaces
	netNamespaceList, err := cnoclient.ListAllOfSpecifiedType(gvrNetnamespace, ctx, client)
	if err != nil {
		return err
	}

	// 2. iterate through netnamespaces
	//    - any with multicast-enabled="true" annotation will cause an update to the corresponding
	//      namespace to add the necessary OVN annotation
	for _, nns := range netNamespaceList {
		multicastAnnotation := nns.GetAnnotations()[multicastEnabledSDN]
		if multicastAnnotation == "true" {
			// update namespace to have the same annotation
			nspStr := nns.GetName()

			if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
				namespaceObj, err := client.Default().Kubernetes().CoreV1().Namespaces().Get(ctx, nspStr, metav1.GetOptions{})
				if err != nil {
					return err
				}
				if namespaceObj.Annotations == nil {
					namespaceObj.Annotations = make(map[string]string)
				}
				namespaceObj.Annotations[multicastEnabledOVN] = "true"
				_, err = client.Default().Kubernetes().CoreV1().Namespaces().Update(ctx, namespaceObj, metav1.UpdateOptions{})
				return err
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func enableMulticastSDN(ctx context.Context, client cnoclient.Client) error {
	multicastRollbackReady, err := netNamespacesExistForAllNamespaces(ctx, client)
	if !multicastRollbackReady {
		return nil // wait for all SDN netnamespace resources to be created before rollback
	} else if err != nil {
		return err
	}

	// 1. query for namespaces
	namespaceList, err := cnoclient.ListAllNamespaces(ctx, client)
	if err != nil {
		return err
	}

	// 2. iterate through namespaces
	//    - any with multicast-enabled=true annotation will cause an update to the corresponding
	//      netnamespace to add the necessary SDN annotation
	for _, ns := range namespaceList {
		if ns.Annotations[multicastEnabledOVN] == "true" {
			if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
				nns, err := client.Default().Dynamic().Resource(gvrNetnamespace).Get(ctx, ns.Name, metav1.GetOptions{})
				if err != nil {
					return err
				}
				if nns.Object["metadata"].(map[string]interface{})["annotations"] == nil {
					nns.Object["metadata"].(map[string]interface{})["annotations"] = make(map[string]string)
				}

				multicastEnabledMap := map[string]string{
					multicastEnabledSDN: "true",
				}
				err = uns.SetNestedStringMap(nns.Object, multicastEnabledMap, "metadata", "annotations")
				if err != nil {
					return err
				}
				_, err = client.Default().Dynamic().Resource(gvrNetnamespace).Update(ctx, nns, metav1.UpdateOptions{})
				return err
			}); err != nil {
				return err
			}

			// cleanup: remove the annotation from namespace
			if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
				ns, err = client.Default().Kubernetes().CoreV1().Namespaces().Get(ctx, ns.Name, metav1.GetOptions{})
				if err != nil {
					return err
				}
				delete(ns.Annotations, multicastEnabledOVN)
				_, err = client.Default().Kubernetes().CoreV1().Namespaces().Update(ctx, ns, metav1.UpdateOptions{})
				return err
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func convertEgressNetworkPolicyToEgressFirewall(ctx context.Context, client cnoclient.Client) error {
	egressNetworkPolicyList, err := cnoclient.ListAllOfSpecifiedType(gvrEgressNetworkPolicy, ctx, client)
	if err != nil {
		return err
	}
	for _, enp := range egressNetworkPolicyList {
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
	egressFirewallList, err := cnoclient.ListAllOfSpecifiedType(gvrEgressFirewall, ctx, client)
	if err != nil {
		return err
	}
	for _, ef := range egressFirewallList {
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

func convertSdnEgressIpToOvnEgressIp(ctx context.Context, client cnoclient.Client) ([]*uns.Unstructured, []*uns.Unstructured, error) {

	// 1. query for hostsubnets and netnamespaces
	hostSubnetList, err := cnoclient.ListAllOfSpecifiedType(gvrHostSubnet, ctx, client)
	if err != nil {
		return nil, nil, err
	}
	netNamespaceList, err := cnoclient.ListAllOfSpecifiedType(gvrNetnamespace, ctx, client)
	if err != nil {
		return nil, nil, err
	}

	// 2. iterate through hostsubnets
	//    - any with egressIP configured will cause update to node label "egress-assignable"
	hostSubnetFound := false
	for _, hsn := range hostSubnetList {
		hostSubnetHasEgressIpConfigAutomatic := hostSubnetHasEgressIpConfigAutomatic(*hsn)
		hostSubnetHasEgressIpConfigManual := hostSubnetHasEgressIpConfigManual(*hsn)

		// mark node from hostsubnet with annotation as follows:
		// - k8s.ovn.org/egress-assignable: ""
		if hostSubnetHasEgressIpConfigAutomatic || hostSubnetHasEgressIpConfigManual {
			hostSubnetFound = true
			if err := labelNodeAndRemoveHostSubnetConfig(ctx, client, hsn); err != nil {
				return nil, nil, err
			}
		}

		if hostSubnetHasEgressIpConfigManual {
			log.Printf("Manual configuration of SDN egressIP detected and is unsupported for migration; OVN egressIPs will be generated but will not maintain individual node assignments from SDN hostsubnets")
		}
	}

	if !hostSubnetFound {
		log.Printf("did not find a hostsubnet object with egressIP configured, quitting process early")
		return nil, nil, nil
	} else {
		log.Printf("found hostsubnet object with egressIP configured, continuing...")
	}

	// 3. iterate through netnamespaces
	//    - any with egressIP configured will cause an egressIP ovn resource to be created via k8s api
	//    - a corresponding namespace label will be added to match the egressIP resource's namespace selector field
	egressIpList := []*uns.Unstructured{}
	for _, nns := range netNamespaceList {
		if netNamespaceHasEgressIpConfig(*nns) {
			// config detected, translating to ovn
			// - delete cloudprivateipconfig if it exists
			// - create egressip custom resource for ovn
			if err := deleteCloudPrivateIpConfigs(ctx, client, nns.Object["egressIPs"].([]interface{})); err != nil {
				return nil, nil, err
			}

			egressIpName := fmt.Sprint("egressip-", nns.GetName())
			egressIP := unstructuredEgressIpObject(egressIpName, nns.Object["egressIPs"].([]interface{}), nns.Object["netname"])
			egressIpList = append(egressIpList, egressIP)
		}
	}

	return egressIpList, netNamespaceList, nil // success
}

func convertOvnEgressIpToSdnEgressIp(ctx context.Context, client cnoclient.Client) error {
	egressIpRollbackReady, err := sdnEgressIpResourcesReady(ctx, client)
	if !egressIpRollbackReady {
		return nil // wait for all SDN egressIP resources to be created before rollback
	} else if err != nil {
		return err
	}

	// 1. query for egressips
	egressIpList, err := cnoclient.ListAllOfSpecifiedType(gvrEgressIp, ctx, client)
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
	for _, eip := range egressIpList {
		// translate egressIPs to SDN netnamespaces
		// - delete cloudprivateipconfig if it exists
		// - create egressip to netnamespace resources for sdn

		egressIps := eip.Object["spec"].(map[string]interface{})["egressIPs"].([]interface{})

		if err := deleteCloudPrivateIpConfigs(ctx, client, egressIps); err != nil {
			return err
		}

		eipNamespace, found, err := uns.NestedString(eip.Object, "spec", "namespaceSelector", "matchLabels", "kubernetes.io/metadata.name")
		if !found || err != nil {
			return fmt.Errorf("kubernetes.io/metadata.name not found in egressIP object %s, probable underlying error: %v", eip.GetName(), err)
		}

		if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			nns, err := client.Default().Dynamic().Resource(gvrNetnamespace).Get(ctx, eipNamespace, metav1.GetOptions{})
			if err != nil {
				return err
			}

			nns.Object["egressIPs"] = egressIps
			_, err = client.Default().Dynamic().Resource(gvrNetnamespace).Update(ctx, nns, metav1.UpdateOptions{})
			return err
		}); err != nil {
			return err
		}
	}

	// 4. iterate through nodes and generate hostsubnets for any that are egress-assignable
	for _, node := range nodeList.Items {
		if node.Labels == nil {
			continue
		}
		if _, ok := node.Labels[egressAssignable]; ok {
			// generate egressCIDRs field
			// if "egressCIDRAnnotationName" node annotation exists, automatic sdn config was used and we shall reuse old config
			// else, manual sdn config was used and we shall use node's subnet CIDR (cannot restore egressIPs)
			var egressCIDRsArr []string
			if _, ok := node.Annotations[egressCIDRAnnotationName]; ok {
				ovnMigrationNodeAnnotation := OVNMigrationNodeAnnotation{}
				if err := json.Unmarshal([]byte(node.Annotations[egressCIDRAnnotationName]), &ovnMigrationNodeAnnotation); err != nil {
					return err
				}
				egressCIDRsArr = ovnMigrationNodeAnnotation.EgressCIDRs
			} else {
				egressIpConfig := node.Annotations[egressIPNodeConfig]
				egressIpConfigArr := make([]NodeEgressIpConfig, 0)
				if err := json.Unmarshal([]byte(egressIpConfig), &egressIpConfigArr); err != nil {
					return err
				}
				if len(egressIpConfigArr) <= 0 {
					return fmt.Errorf("unexpected error: egress-ipconfig annotation is empty")
				}
				nodeSubnet, ok := egressIpConfigArr[0].IfAddr["ipv4"]
				if !ok {
					return fmt.Errorf("unexpected error: egress-ipconfig annotation missing ipv4 entry")
				}
				egressCIDRsArr = []string{nodeSubnet}
			}

			if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
				hsn, err := client.Default().Dynamic().Resource(gvrHostSubnet).Get(ctx, node.Name, metav1.GetOptions{})
				if err != nil {
					return err
				}

				if err := uns.SetNestedStringSlice(hsn.Object, egressCIDRsArr, "egressCIDRs"); err != nil {
					return err
				}
				_, err = client.Default().Dynamic().Resource(gvrHostSubnet).Update(ctx, hsn, metav1.UpdateOptions{})
				return err
			}); err != nil {
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
	hostSubnetList, err := cnoclient.ListAllOfSpecifiedType(gvrHostSubnet, ctx, client)
	if err != nil {
		return false, err
	}

	for _, node := range nodeList.Items {
		found := false
		for _, hostSubnet := range hostSubnetList {
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
			if netnamespace.GetName() == namespace.Name {
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
	if hsn.Object["egressIPs"] != nil && hsn.Object["egressCIDRs"] == nil {
		return len(hsn.Object["egressIPs"].([]interface{})) > 0
	}
	return false
}

func labelNodeAndRemoveHostSubnetConfig(ctx context.Context, client cnoclient.Client, hsn *uns.Unstructured) error {
	// label node as egressassignable
	hostStr := fmt.Sprintf("%v", hsn.Object["host"])

	if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		nodeObj, err := client.Default().Kubernetes().CoreV1().Nodes().Get(ctx, hostStr, metav1.GetOptions{})
		if err != nil {
			return err
		}

		if nodeObj.Labels == nil {
			nodeObj.Labels = make(map[string]string)
		}
		if _, ok := nodeObj.Labels[egressAssignable]; !ok {
			nodeObj.Labels[egressAssignable] = ""
		}

		// copy egressCIDRs to node label if possible
		// - if egressCIDRs contains values (automatic config), on rollback this annotation will provide the respective hostsubnet with its original egressCIDRs values
		// - if egressCIDRs is empty (manual config), on rollback this label will not exist, and so we default to node's subnet for egressCIDR field.
		egressCIDRs, found, err := uns.NestedStringSlice(hsn.Object, "egressCIDRs")
		if err != nil {
			return fmt.Errorf("egressCIDRs not found in HostSubnet object %s, probable underlying error: %v", hsn.GetName(), err)
		} else if found {
			// automatic configuration
			ovnMigrationAnnotation := OVNMigrationNodeAnnotation{
				EgressCIDRs: egressCIDRs,
			}

			if nodeObj.Annotations == nil {
				nodeObj.Annotations = make(map[string]string)
			}
			if _, ok := nodeObj.Annotations[egressCIDRAnnotationName]; !ok {
				egressCIDRsText, err := json.Marshal(ovnMigrationAnnotation)
				if err != nil {
					return err
				}
				nodeObj.Annotations[egressCIDRAnnotationName] = string(egressCIDRsText)
			}
		}
		_, err = client.Default().Kubernetes().CoreV1().Nodes().Update(ctx, nodeObj, metav1.UpdateOptions{})
		return err
	}); err != nil {
		return err
	}

	if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		hsn, err := client.Default().Dynamic().Resource(gvrHostSubnet).Get(ctx, hsn.GetName(), metav1.GetOptions{})
		if err != nil {
			return err
		}
		// now remove egressIP config from hostsubnet
		hsn.Object["egressCIDRs"] = nil
		hsn.Object["egressIPs"] = nil
		_, err = client.Default().Dynamic().Resource(gvrHostSubnet).Update(ctx, hsn, metav1.UpdateOptions{})
		return err
	}); err != nil {
		return err
	}

	return nil
}

func deleteCloudPrivateIpConfigs(ctx context.Context, client cnoclient.Client, egressIps []interface{}) error {
	for _, egressIp := range egressIps {
		egressIpStr := fmt.Sprintf("%v", egressIp)
		// delete cloudprivateipconfig
		err := client.Default().Dynamic().Resource(gvrCloudPrivateIPConfig).Delete(ctx, egressIpStr, metav1.DeleteOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				continue
			} else {
				return err
			}
		}
	}
	return nil
}

func applyEgressIpList(ctx context.Context, client cnoclient.Client, egressIpList []*uns.Unstructured, netNamespaceList []*uns.Unstructured) error {
	for _, egressIp := range egressIpList {
		if err := apply.ApplyObject(ctx, client, egressIp, ""); err != nil {
			return err
		}
	}

	for _, nns := range netNamespaceList {
		if netNamespaceHasEgressIpConfig(*nns) {
			if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
				nns, err := client.Default().Dynamic().Resource(gvrNetnamespace).Get(ctx, nns.GetName(), metav1.GetOptions{})
				if err != nil {
					return err
				}
				// now remove egressIP config from netnamespace
				nns.Object["egressIPs"] = nil
				_, err = client.Default().Dynamic().Resource(gvrNetnamespace).Update(ctx, nns, metav1.UpdateOptions{})
				return err
			}); err != nil {
				return err
			}
		}
	}

	return nil
}

func unstructuredEgressIpObject(egressIpName string, egressIps []interface{}, netname interface{}) *uns.Unstructured {
	return &uns.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "k8s.ovn.org/v1",
			"kind":       "EgressIP",
			"metadata": map[string]interface{}{
				"name": egressIpName,
			},
			"spec": map[string]interface{}{
				"egressIPs": egressIps,
				"namespaceSelector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						"kubernetes.io/metadata.name": netname,
					},
				},
			},
		},
	}
}
