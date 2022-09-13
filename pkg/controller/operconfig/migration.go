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
	"k8s.io/client-go/util/retry"

	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/apply"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
)

const defaultEgressFirewallName = "default"
const multicastEnabledSDN = "netnamespace.network.openshift.io/multicast-enabled"
const multicastEnabledOVN = "k8s.ovn.org/multicast-enabled"

var gvrEgressFirewall = schema.GroupVersionResource{Group: "k8s.ovn.org", Version: "v1", Resource: "egressfirewalls"}
var gvrEgressNetworkPolicy = schema.GroupVersionResource{Group: "network.openshift.io", Version: "v1", Resource: "egressnetworkpolicies"}
var gvrNetnamespace = schema.GroupVersionResource{Group: "network.openshift.io", Version: "v1", Resource: "netnamespaces"}

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
