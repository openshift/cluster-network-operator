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

var gvrEgressFirewall = schema.GroupVersionResource{Group: "k8s.ovn.org", Version: "v1", Resource: "egressfirewalls"}
var gvrEgressNetworkPolicy = schema.GroupVersionResource{Group: "network.openshift.io", Version: "v1", Resource: "egressnetworkpolicies"}

func migrateEgressFirewallCRs(ctx context.Context, operConfig *operv1.Network, client cnoclient.Client) error {
	switch operConfig.Spec.Migration.NetworkType {
	case string(operv1.NetworkTypeOVNKubernetes):
		return convertEgressNetworkPolicyToEgressFirewall(ctx, client)
	case string(operv1.NetworkTypeOpenShiftSDN):
		return convertEgressFirewallToEgressNetworkPolicy(ctx, client)
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
					"name":      enp.GetName(),
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
