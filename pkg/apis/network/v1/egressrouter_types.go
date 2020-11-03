package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EgressRouter is a feature allowing the user to define an egress router
// that acts as a bridge between pods and external systems. The egress router runs
// a service that redirects egress traffic originating from a pod or a group of
// pods to a remote external system or multiple destinations as per configuration.

//  More specifically, given an EgressRouter with <name>, the CNO will create and manage:
// - A service called <name>
// - An egress pod called <name>
// - An NAD called <name>

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// EgressRouter is a single egressrouter pod configuration object.
// +k8s:openapi-gen=true
// +kubebuilder:resource:path=egressrouters,scope=Namespaced
type EgressRouter struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:validation:Required
	Spec EgressRouterSpec `json:"spec"`

	// +kubebuilder:validation:Optional
	// +optional
	Status []EgressRouterStatus `json:"status,omitempty"`
}

// EgressRouterSpec contains the configuration for an egress router.
// +k8s:openapi-gen=true
// +kubebuilder:validation:Required
type EgressRouterSpec struct {
	// The name of the network attachment definition
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:default=egress-router
	// +kubebuilder:validation:Required
	Type string `json:"type"`

	// Type of interface to create/use. e.g; ipvlan, macvlan
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Optional
	// +optional
	InterfaceType string `json:"interfaceType,omitempty"`

	// Arguments specific to the interfaceType
	// +kubebuilder:validation:Optional
	// +optional
	InterfaceArgs InterfaceArgsObject `json:"interfaceArgs,omitempty"`

	// IP configuration arguments.
	// +kubebuilder:validation:Required
	IP IPConfigObject `json:"ip"`
}

// EgressRouterStatus contains the observed status of EgressRouter. Read-only.
type EgressRouterStatus struct {
	// Name of the egress router network
	// +kubebuilder:validation:Required
	EgressRouter string `json:"egressRouter,omitempty"`

	// Observed status of the egress router pod
	// +kubebuilder:validation:Required
	Status string `json:"status,omitempty"`
}

// IPConfigObject contains the configuration for the ip arguments
type IPConfigObject struct {
	// List of IP addresses to configure on the interface.
	// +kubebuilder:validation:Required
	Addresses []IPAddress `json:"addresses"`

	// IP address of the next-hop gateway, if it cannot be automatically determined.
	// +kubebuilder:validation:Pattern=`^(([0-9]|[0-9][0-9]|1[0-9][0-9]|2[0-4][0-9]|25[0-5])\.){3}([0-9]|[0-9][0-9]|1[0-9][0-9]|2[0-4][0-9]|25[0-5])$`
	// +kubebuilder:validation:Optional
	// +optional
	Gateway string `json:"gateway"`

	// List of CIDR blocks that the pod is allowed to connect to via this interface. If not provided, the pod can connect to any destination.
	// +kubebuilder:validation:Optional
	// +optional
	Destinations []DestinationCIDR `json:"destinations"`
}

// IPAddress is the address to configure on the router's interface.
// +kubebuilder:validation:Pattern=`^(([0-9]|[0-9][0-9]|1[0-9][0-9]|2[0-4][0-9]|25[0-5])\.){3}([0-9]|[0-9][0-9]|1[0-9][0-9]|2[0-4][0-9]|25[0-5])$`
type IPAddress string

// DestinationCIDR represents the CIDR block that the pod is allowed to connect to via this interface.
// +kubebuilder:validation:Pattern=`^(([0-9]|[0-9][0-9]|1[0-9][0-9]|2[0-4][0-9]|25[0-5])\.){3}([0-9]|[0-9][0-9]|1[0-9][0-9]|2[0-4][0-9]|25[0-5])/([0-9]|[12][0-9]|3[0-2])$`
type DestinationCIDR string

// InterfaceArgsObject consists of arguments specific to the interfaceType
type InterfaceArgsObject struct {
	// Usually used for macvlan interface.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Optional
	// +optional
	Mode string `json:"mode,omitempty"`

	// Name of the master inteface. Usually used for macvlan and ipvlan. Need not be specified if it can be inferred from the IP address.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Optional
	// +optional
	Master string `json:"master,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// EgressRouterList is the list of egress router pods requested.
type EgressRouterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []EgressRouter `json:"items"`
}
