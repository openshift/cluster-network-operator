package hypershift

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"

	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

const HostedClusterLocalProxy = "socks5://127.0.0.1:8090"
const HostedClusterDefaultAdvertiseAddressIPV4 = "172.20.0.1"
const HostedClusterDefaultAdvertiseAddressIPV6 = "fd00::1"

const HostedClusterDefaultAdvertisePort = int64(6443)

var (
	enabled           = os.Getenv("HYPERSHIFT")
	name              = os.Getenv("HOSTED_CLUSTER_NAME")
	namespace         = os.Getenv("HOSTED_CLUSTER_NAMESPACE")
	runAsUser         = os.Getenv("RUN_AS_USER")
	releaseImage      = os.Getenv("OPENSHIFT_RELEASE_IMAGE")
	controlPlaneImage = os.Getenv("OVN_CONTROL_PLANE_IMAGE")
	caConfigMap       = os.Getenv("CA_CONFIG_MAP")
	caConfigMapKey    = os.Getenv("CA_CONFIG_MAP_KEY")
)

const (
	// ClusterIDLabel (_id) is the common label used to identify clusters in telemeter.
	// For hypershift, it will identify metrics produced by the both the control plane
	// components and guest cluster monitoring stack.
	ClusterIDLabel = "_id"
	// HyperShiftConditionTypePrefix is a cluster network operator condition type prefix in hostedControlPlane status
	HyperShiftConditionTypePrefix = "network.operator.openshift.io/"
)

type RelatedObject struct {
	configv1.ObjectReference
	ClusterName string
}

// Since using the HyperShift API directly causes a dependency hell the following code defines some fields and structs
// from the HyperShift API found here:
// https://github.com/openshift/hypershift/blob/27316d734d806a29d63f65ddf746cafd4409a1de/api/hypershift/v1beta1/hosted_controlplane.go#L28

// HostedControlPlane represents a subset of HyperShift API definition for HostedControlPlane
type HostedControlPlane struct {
	ClusterID                    string
	ControllerAvailabilityPolicy AvailabilityPolicy
	NodeSelector                 map[string]string
	Labels                       map[string]string
	Tolerations                  []string
	AdvertiseAddress             string
	AdvertisePort                int
	PriorityClass                string
}

// AvailabilityPolicy specifies a high level availability policy for components.
type AvailabilityPolicy string

const (
	// HighlyAvailable means components should be resilient to problems across
	// fault boundaries as defined by the component to which the policy is
	// attached. This usually means running critical workloads with 3 replicas and
	// with little or no toleration of disruption of the component.
	HighlyAvailable AvailabilityPolicy = "HighlyAvailable"

	// SingleReplica means components are not expected to be resilient to problems
	// across most fault boundaries associated with high availability. This
	// usually means running critical workloads with just 1 replica and with
	// toleration of full disruption of the component.
	SingleReplica AvailabilityPolicy = "SingleReplica"
)

// HostedControlPlaneGVK GroupVersionKind for HostedControlPlane
// Based on https://github.com/openshift/hypershift/blob/27316d734d806a29d63f65ddf746cafd4409a1de/api/hypershift/v1beta1/hosted_controlplane.go#L19
var HostedControlPlaneGVK = schema.GroupVersionKind{
	Group:   "hypershift.openshift.io",
	Version: "v1beta1",
	Kind:    "HostedControlPlane",
}

type HyperShiftConfig struct {
	sync.Mutex
	Enabled           bool
	Name              string
	Namespace         string
	RunAsUser         string
	RelatedObjects    []RelatedObject
	ReleaseImage      string
	ControlPlaneImage string
	CAConfigMap       string
	CAConfigMapKey    string
}

func NewHyperShiftConfig() *HyperShiftConfig {
	if caConfigMap == "" {
		caConfigMap = "openshift-service-ca.crt"
	}

	if caConfigMapKey == "" {
		caConfigMapKey = "service-ca.crt"
	}

	return &HyperShiftConfig{
		Enabled:           hyperShiftEnabled(),
		Name:              name,
		Namespace:         namespace,
		RunAsUser:         runAsUser,
		ReleaseImage:      releaseImage,
		ControlPlaneImage: controlPlaneImage,
		CAConfigMap:       caConfigMap,
		CAConfigMapKey:    caConfigMapKey,
	}
}

func hyperShiftEnabled() bool {
	return enabled == "true"
}

func (hc *HyperShiftConfig) SetRelatedObjects(relatedObjects []RelatedObject) {
	hc.Lock()
	defer hc.Unlock()
	hc.RelatedObjects = relatedObjects
}

// ParseHostedControlPlane parses the provided unstructured argument into a HostedControlPlane struct
func ParseHostedControlPlane(hcp *unstructured.Unstructured) (*HostedControlPlane, error) {
	clusterID, _, err := unstructured.NestedString(hcp.UnstructuredContent(), "spec", "clusterID")
	if err != nil {
		return nil, fmt.Errorf("failed to extract clusterID: %v", err)
	}

	controllerAvailabilityPolicy, _, err := unstructured.NestedString(hcp.UnstructuredContent(), "spec", "controllerAvailabilityPolicy")
	if err != nil {
		return nil, fmt.Errorf("failed to extract controllerAvailabilityPolicy: %v", err)
	}

	controlPlanePriorityClassAnnotation, _, err := unstructured.NestedString(hcp.UnstructuredContent(), "metadata", "annotations", "hypershift.openshift.io/control-plane-priority-class")
	if err != nil {
		return nil, fmt.Errorf("failed to extract control plane priority class annotation: %v", err)
	}

	nodeSelector, _, err := unstructured.NestedStringMap(hcp.UnstructuredContent(), "spec", "nodeSelector")
	if err != nil {
		return nil, fmt.Errorf("failed extract nodeSelector: %v", err)
	}

	labels, _, err := unstructured.NestedStringMap(hcp.UnstructuredContent(), "spec", "labels")
	if err != nil {
		return nil, fmt.Errorf("failed to extract labels: %v", err)
	}

	var tolerations []corev1.Toleration
	var tolerationsYaml []string
	tolerationsArray, tolerationsArrayFound, err := unstructured.NestedFieldCopy(hcp.UnstructuredContent(), "spec", "tolerations")
	if err != nil {
		return nil, fmt.Errorf("failed extract tolerations: %v", err)
	}
	if tolerationsArrayFound {
		tolerationsArrayConverted, hasConverted := tolerationsArray.([]interface{})
		if hasConverted {
			for _, entry := range tolerationsArrayConverted {
				tolerationConverted, hasConverted := entry.(map[string]interface{})
				if hasConverted {
					toleration := corev1.Toleration{}
					raw, ok := tolerationConverted["key"]
					if ok {
						str, isString := raw.(string)
						if isString {
							toleration.Key = str
						}
					}
					raw, ok = tolerationConverted["operator"]
					if ok {
						op, isOperator := raw.(string)
						if isOperator {
							toleration.Operator = corev1.TolerationOperator(op)
						}
					}
					raw, ok = tolerationConverted["value"]
					if ok {
						str, isString := raw.(string)
						if isString {
							toleration.Value = str
						}
					}
					raw, ok = tolerationConverted["effect"]
					if ok {
						effect, isEffect := raw.(string)
						if isEffect {
							toleration.Effect = corev1.TaintEffect(effect)
						}
					}
					raw, ok = tolerationConverted["tolerationSeconds"]
					if ok {
						seconds, isSeconds := raw.(*int64)
						if isSeconds {
							toleration.TolerationSeconds = seconds
						}
					}
					tolerations = append(tolerations, toleration)
				}
			}
		}
		tolerationsYaml, err = tolerationsToStringSliceYaml(tolerations)
		if err != nil {
			return nil, fmt.Errorf("failed to yaml marshal tolerations: %v", err)
		}
	}

	advertiseAddress, valueFound, err := unstructured.NestedString(hcp.UnstructuredContent(), "spec", "networking", "apiServer", "advertiseAddress")
	if err != nil {
		return nil, fmt.Errorf("failed extract advertiseAddress: %v", err)
	}
	if !valueFound {
		// default to ipv4 unless we can prove it is a ipv6 cluster
		advertiseAddress = HostedClusterDefaultAdvertiseAddressIPV4
		cidrArray, cidrArrayValueFound, err := unstructured.NestedFieldCopy(hcp.UnstructuredContent(), "spec", "networking", "serviceNetwork")
		if err != nil {
			return nil, fmt.Errorf("failed extract serviceNetwork: %v", err)
		}
		if cidrArrayValueFound {
			cidrArrayConverted, hasConverted := cidrArray.([]interface{})
			if hasConverted {
				sampleCidrVal := cidrArrayConverted[0]
				sampleCidrValConverted, sampleCidrHasConverted := sampleCidrVal.(map[string]interface{})
				if sampleCidrHasConverted {
					cidrRawVal, hasCidrKey := sampleCidrValConverted["cidr"]
					if hasCidrKey {
						cidrString, isString := cidrRawVal.(string)
						if isString && strings.Count(cidrString, ":") >= 2 {
							advertiseAddress = HostedClusterDefaultAdvertiseAddressIPV6
						}
					}
				}
			}
		}
	}
	advertisePort, valueFound, err := unstructured.NestedInt64(hcp.UnstructuredContent(), "spec", "networking", "apiServer", "port")
	if err != nil {
		return nil, fmt.Errorf("failed extract advertisePort: %v", err)
	}
	if !valueFound {
		advertisePort = HostedClusterDefaultAdvertisePort
	}

	return &HostedControlPlane{
		ControllerAvailabilityPolicy: AvailabilityPolicy(controllerAvailabilityPolicy),
		ClusterID:                    clusterID,
		NodeSelector:                 nodeSelector,
		Labels:                       labels,
		Tolerations:                  tolerationsYaml,
		AdvertiseAddress:             advertiseAddress,
		AdvertisePort:                int(advertisePort),
		PriorityClass:                controlPlanePriorityClassAnnotation,
	}, nil
}

// SetHostedControlPlaneConditions updates the hcp status.conditions based on the provided operStatus
// Returns an updated list of conditions and an error. If there are no changes, the returned list is empty.
func SetHostedControlPlaneConditions(hcp *unstructured.Unstructured, operStatus *operv1.NetworkStatus) ([]metav1.Condition, error) {
	conditionsRaw, _, err := unstructured.NestedSlice(hcp.UnstructuredContent(), "status", "conditions")
	if err != nil {
		return nil, fmt.Errorf("failed extract conditions: %v", err)
	}

	var conditions []metav1.Condition
	jsonData, err := json.Marshal(conditionsRaw)
	if err != nil {
		return nil, fmt.Errorf("error marshalling JSON: %v\n", err)
	}
	err = json.Unmarshal(jsonData, &conditions)
	if err != nil {
		return nil, fmt.Errorf("error unmarshalling JSON: %v\n", err)
	}

	oldConditions := make([]metav1.Condition, len(conditions))
	copy(oldConditions, conditions)

	if operStatus == nil {
		meta.SetStatusCondition(&conditions, metav1.Condition{
			Type:    "NetworkOperatorAvailable",
			Status:  metav1.ConditionUnknown,
			Reason:  "NoNetworkOperConfig",
			Message: "No networks.operator.openshift.io cluster found",
		})
	} else {
		for _, cond := range operStatus.Conditions {
			reason := "AsExpected"
			if cond.Reason != "" {
				reason = cond.Reason
			}

			newCondition := metav1.Condition{
				Type:    HyperShiftConditionTypePrefix + cond.Type,
				Status:  metav1.ConditionStatus(cond.Status),
				Reason:  reason,
				Message: cond.Message,
			}
			meta.SetStatusCondition(&conditions, newCondition)
		}
	}

	if reflect.DeepEqual(oldConditions, conditions) {
		return nil, nil
	}

	// Set the conditions directly instead of using SetNestedField
	// because it does a DeepCopy and metav1.Condition doesn't implement it
	hcp.Object["status"].(map[string]interface{})["conditions"] = conditions
	return conditions, nil
}

// tolerationsToStringSliceYaml converts a slice of tolerations into a slice of
// strings that represent the toleration in yaml syntax where each string
// is a line of yaml. The resulting string slice can be easily used in
// yaml manifest templating.
func tolerationsToStringSliceYaml(tolerations []corev1.Toleration) ([]string, error) {
	if len(tolerations) == 0 {
		return nil, nil
	}

	yamlBytes, err := yaml.Marshal(tolerations)
	if err != nil {
		return nil, err
	}

	yamlStrs := []string{}
	for _, arg := range strings.Split(string(yamlBytes), "\n") {

		// filter out null and empty strings
		if strings.Contains(arg, ": null") || strings.Contains(arg, ": \"\"") {
			continue
		}
		yamlStrs = append(yamlStrs, arg)
	}
	return yamlStrs, nil
}
