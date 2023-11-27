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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const HyperShiftInternalRouteLabel = "hypershift.openshift.io/internal-route"
const HostedClusterLocalProxy = "socks5://127.0.0.1:8090"

func init() {
	for _, label := range strings.Split(routeLabelsRaw, ",") {
		if label == "" {
			continue
		}
		key, value, found := strings.Cut(label, "=")
		if !found {
			panic(fmt.Sprintf("label %q can not be parsed as key value pair", label))
		}
		routeLabels[key] = value
	}
}

var (
	enabled           = os.Getenv("HYPERSHIFT")
	name              = os.Getenv("HOSTED_CLUSTER_NAME")
	namespace         = os.Getenv("HOSTED_CLUSTER_NAMESPACE")
	routeHost         = os.Getenv("OVN_SBDB_ROUTE_HOST")
	routeLabels       = map[string]string{}
	routeLabelsRaw    = os.Getenv("OVN_SBDB_ROUTE_LABELS")
	runAsUser         = os.Getenv("RUN_AS_USER")
	releaseImage      = os.Getenv("OPENSHIFT_RELEASE_IMAGE")
	controlPlaneImage = os.Getenv("OVN_CONTROL_PLANE_IMAGE")
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
	Services                     []ServicePublishingStrategyMapping
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

// ServiceType defines what control plane services can be exposed from the
// management control plane.
type ServiceType string

var (
	// APIServer is the control plane API server.
	APIServer ServiceType = "APIServer"

	// Konnectivity is the control plane Konnectivity networking service.
	Konnectivity ServiceType = "Konnectivity"

	// OAuthServer is the control plane OAuth service.
	OAuthServer ServiceType = "OAuthServer"

	// OIDC is the control plane OIDC service.
	OIDC ServiceType = "OIDC"

	// Ignition is the control plane ignition service for nodes.
	Ignition ServiceType = "Ignition"

	// OVNSbDb is the optional control plane ovn southbound database service used by OVNKubernetes CNI.
	OVNSbDb ServiceType = "OVNSbDb"
)

// NodePortPublishingStrategy specifies a NodePort used to expose a service.
type NodePortPublishingStrategy struct {
	// Address is the host/ip that the NodePort service is exposed over.
	Address string `json:"address"`

	// Port is the port of the NodePort service. If <=0, the port is dynamically
	// assigned when the service is created.
	Port int32 `json:"port,omitempty"`
}

// LoadBalancerPublishingStrategy specifies setting used to expose a service as a LoadBalancer.
type LoadBalancerPublishingStrategy struct {
	// Hostname is the name of the DNS record that will be created pointing to the LoadBalancer.
	// +optional
	Hostname string `json:"hostname,omitempty"`
}

// RoutePublishingStrategy specifies options for exposing a service as a Route.
type RoutePublishingStrategy struct {
	// Hostname is the name of the DNS record that will be created pointing to the Route.
	// +optional
	Hostname string `json:"hostname,omitempty"`
}

// ServicePublishingStrategyMapping specifies how individual control plane
// services are published from the hosting cluster of a control plane.
type ServicePublishingStrategyMapping struct {
	// Service identifies the type of service being published.
	//
	// +kubebuilder:validation:Enum=APIServer;OAuthServer;OIDC;Konnectivity;Ignition;OVNSbDb
	// +immutable
	Service ServiceType `json:"service"`

	// ServicePublishingStrategy specifies how to publish Service.
	ServicePublishingStrategy `json:"servicePublishingStrategy"`
}

// ServicePublishingStrategy specfies how to publish a ServiceType.
type ServicePublishingStrategy struct {
	// Type is the publishing strategy used for the service.
	//
	// +kubebuilder:validation:Enum=LoadBalancer;NodePort;Route;None;S3
	// +immutable
	Type PublishingStrategyType `json:"type"`

	// NodePort configures exposing a service using a NodePort.
	NodePort *NodePortPublishingStrategy `json:"nodePort,omitempty"`

	// LoadBalancer configures exposing a service using a LoadBalancer.
	LoadBalancer *LoadBalancerPublishingStrategy `json:"loadBalancer,omitempty"`

	// Route configures exposing a service using a Route.
	Route *RoutePublishingStrategy `json:"route,omitempty"`
}

// PublishingStrategyType defines publishing strategies for services.
type PublishingStrategyType string

var (
	// LoadBalancer exposes a service with a LoadBalancer kube service.
	LoadBalancer PublishingStrategyType = "LoadBalancer"
	// NodePort exposes a service with a NodePort kube service.
	NodePort PublishingStrategyType = "NodePort"
	// Route exposes services with a Route + ClusterIP kube service.
	Route PublishingStrategyType = "Route"
	// S3 exposes a service through an S3 bucket
	S3 PublishingStrategyType = "S3"
	// None disables exposing the service
	None PublishingStrategyType = "None"
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
	Enabled            bool
	Name               string
	Namespace          string
	OVNSbDbRouteHost   string
	RunAsUser          string
	OVNSbDbRouteLabels map[string]string
	RelatedObjects     []RelatedObject
	ReleaseImage       string
	ControlPlaneImage  string
}

func NewHyperShiftConfig() *HyperShiftConfig {
	return &HyperShiftConfig{
		Enabled:            hyperShiftEnabled(),
		Name:               name,
		Namespace:          namespace,
		RunAsUser:          runAsUser,
		OVNSbDbRouteHost:   routeHost,
		OVNSbDbRouteLabels: routeLabels,
		ReleaseImage:       releaseImage,
		ControlPlaneImage:  controlPlaneImage,
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

	nodeSelector, _, err := unstructured.NestedStringMap(hcp.UnstructuredContent(), "spec", "nodeSelector")
	if err != nil {
		return nil, fmt.Errorf("failed extract nodeSelector: %v", err)
	}

	servicesRaw, _, err := unstructured.NestedSlice(hcp.UnstructuredContent(), "spec", "services")
	if err != nil {
		return nil, fmt.Errorf("failed extract services: %v", err)
	}

	var services []ServicePublishingStrategyMapping
	jsonData, err := json.Marshal(servicesRaw)
	if err != nil {
		return nil, fmt.Errorf("error marshalling JSON: %v\n", err)
	}
	err = json.Unmarshal(jsonData, &services)
	if err != nil {
		return nil, fmt.Errorf("error unmarshalling JSON: %v\n", err)
	}

	return &HostedControlPlane{
		ControllerAvailabilityPolicy: AvailabilityPolicy(controllerAvailabilityPolicy),
		ClusterID:                    clusterID,
		NodeSelector:                 nodeSelector,
		Services:                     services,
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
