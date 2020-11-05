package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EgressRouter is a feature allowing the user to define an egress router
// that acts as a bridge between pods and external systems. The egress router runs
// a service that redirects egress traffic originating from a pod or a group of
// pods to a remote external system or multiple destinations as per configuration.
//
//  More specifically, given an EgressRouter CR with <name>, the CNO will create and manage:
// - A service called <name>
// - An egress pod called <name>
// - A NAD called <name>
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
//
// EgressRouter is a single egressrouter pod configuration object.
// +k8s:openapi-gen=true
// +kubebuilder:resource:path=egressrouters,scope=Namespaced
// +kubebuilder:printcolumn:name="Condition",type=string,JSONPath=".status.conditions[*].type"
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=".status.conditions[*].status"
type EgressRouter struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinProperties:=4
	// +kubebuilder:validation:MaxProperties:=4
	Spec EgressRouterSpec `json:"spec"`

	// +kubebuilder:validation:Optional
	// +optional
	Status EgressRouterStatus `json:"status,omitempty"`
}

// EgressRouterSpec contains the configuration for an egress router.
// Mode, networkInterface and addresses fields must be specified along with exactly one "Config" that matches the mode.
// Each config consists of parameters specific to that mode.
// +k8s:openapi-gen=true
// +kubebuilder:validation:Required
type EgressRouterSpec struct {
	// Mode depicts the mode that is used for the egress router. The default mode is "Redirect" and is the only supported mode currently.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum="Redirect"
	// +kubebuilder:default:="Redirect"
	Mode EgressRouterMode `json:"mode"`

	// Redirect represents the configuration parameters specific to redirect mode.
	// +kubebuilder:validation:Optional
	Redirect *RedirectConfig `json:"redirect,omitempty"`

	// Specification of interface to create/use. The default is macvlan.
	// Currently only macvlan is supported.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinProperties:=1
	// +kubebuilder:validation:MaxProperties:=1
	// +kubebuilder:default:={macvlan: {mode: Bridge}}
	NetworkInterface EgressRouterInterface `json:"networkInterface"`

	// List of IP addresses to configure on the pod's secondary interface.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems:=1
	Addresses []EgressRouterAddress `json:"addresses"`
}

// EgressRouterMode defines the different types of modes that are supported for the egress router interface.
// The default mode is "Redirect" and is the only supported mode currently.
type EgressRouterMode string

const (
	// EgressRouterModeRedirect creates an egress router that sets up iptables rules to redirect traffic
	// from its own IP address to one or more remote destination IP addresses.
	EgressRouterModeRedirect EgressRouterMode = "Redirect"
)

// RedirectConfig represents the configuration parameters specific to redirect mode.
type RedirectConfig struct {
	// List of L4 parameters that define the destinations that the pod will connect to in redirect mode.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinItems:=1
	RedirectRules []L4RedirectRule `json:"redirectRules,omitempty"`

	// FallbackIP specifies the remote destination's IP address. Can be IPv4 or IPv6.
	// If no redirect rules are specified, all traffic from the router are redirected to this IP.
	// If redirect rules are specified, then any connections on any other port (undefined in the rules) on the router will be redirected to this IP.
	// If redirect rules are specified and no fallback IP is provided, connections on other ports will simply be rejected.
	// +kubebuilder:validation:Optional
	FallbackIP CIDR `json:"fallbackIP,omitempty"`
}

// L4RedirectRule consists of L4parameters used to define a single remote destination in the redirect mode.
type L4RedirectRule struct {
	// IP specifies the remote destination's IP address. Can be IPv4 or IPv6.
	// +kubebuilder:validation:Required
	DestinationIP CIDR `json:"destinationIP"`

	// Port allows specifying the port number on the router.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Maximum:=65535
	// +kubebuilder:validation:Minimum:=1
	Port uint16 `json:"port"`

	// Protocol can be TCP, SCTP or UDP.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum="TCP";"UDP";"SCTP"
	Protocol ProtocolType `json:"protocol"`

	// TargetPort allows specifying the port number on the remote destination to which the traffic gets redirected to.
	// If unspecified, the traffic is redirected to the destination IP on all ports.
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Maximum:=65535
	// +kubebuilder:validation:Minimum:=1
	TargetPort uint16 `json:"targetPort,omitempty"`
}

// ProtocolType defines the protocol types that are supported
type ProtocolType string

const (
	// ProtocolTypeTCP refers to the TCP protocol
	ProtocolTypeTCP ProtocolType = "TCP"

	// ProtocolTypeUDP refers to the UDP protocol
	ProtocolTypeUDP ProtocolType = "UDP"

	// ProtocolTypeSCTP refers to the SCTP protocol
	ProtocolTypeSCTP ProtocolType = "SCTP"
)

// EgressRouterInterface contains the configuration of interface to create/use.
type EgressRouterInterface struct {
	// Arguments specific to the interfaceType macvlan
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:={mode: Bridge}
	Macvlan MacvlanConfig `json:"macvlan"`
}

// MacvlanMode defines the different types of modes that are supported for the macvlan interface.
// source: https://man7.org/linux/man-pages/man8/ip-link.8.html
type MacvlanMode string

const (
	// MacvlanModeBridge connects all endpoints directly to each other, communication is not redirected through the physical interface's peer.
	MacvlanModeBridge MacvlanMode = "Bridge"

	// MacvlanModePrivate does not allow communication between macvlan instances on the same physical interface,
	// even if the external switch supports hairpin mode.
	MacvlanModePrivate MacvlanMode = "Private"

	// MacvlanModeVEPA is the Virtual Ethernet Port Aggregator mode. Data from one macvlan instance to the other on the
	// same physical interface is transmitted over the physical interface. Either the attached switch needs
	// to support hairpin mode, or there must be a TCP/IP router forwarding the packets in order to allow
	// communication. This is the default mode.
	MacvlanModeVEPA MacvlanMode = "VEPA"

	// MacvlanModePassthru mode gives more power to a single endpoint, usually in macvtap mode.
	// It is not allowed for more than one endpoint on the same physical interface. All traffic will be forwarded
	// to this endpoint, allowing virtio guests to change MAC address or set promiscuous mode in order to bridge the
	// interface or create vlan interfaces on top of it.
	MacvlanModePassthru MacvlanMode = "Passthru"
)

// MacvlanConfig consists of arguments specific to the macvlan EgressRouterInterfaceType
type MacvlanConfig struct {
	// Mode depicts the mode that is used for the macvlan interface; one of Bridge|Private|VEPA|Passthru. The default mode is "Bridge".
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum="Bridge";"Private";"VEPA";"Passthru"
	// +kubebuilder:default:="Bridge"
	Mode MacvlanMode `json:"mode"`

	// Name of the master interface. Need not be specified if it can be inferred from the IP address.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Optional
	// +optional
	Master string `json:"master,omitempty"`
}

// EgressRouterAddress contains a pair of IP CIDR and gateway to be configured on the router's interface
// +kubebuilder:validation:Required
type EgressRouterAddress struct {
	// IP is the address to configure on the router's interface. Can be IPv4 or IPv6.
	// +kubebuilder:validation:Required
	IP CIDR `json:"ip"`
	// IP address of the next-hop gateway, if it cannot be automatically determined. Can be IPv4 or IPv6.
	// +kubebuilder:validation:Optional
	// +optional
	Gateway string `json:"gateway,omitempty"`
}

// CIDR represents an IPv4 or IPV6 CIDR block.
// +kubebuilder:validation:MinLength:=1
type CIDR string

// EgressRouterStatusConditionType is an aspect of the router's state.
type EgressRouterStatusConditionType string

const (
	// EgressRouterAvailable indicates that the EgressRouter (the associated pod, service, NAD), is functional and available in the cluster.
	EgressRouterAvailable EgressRouterStatusConditionType = "Available"

	// EgressRouterProgressing indicates that the router is actively rolling out new code,
	// propagating config changes, or otherwise moving from one steady state to
	// another.
	EgressRouterProgressing EgressRouterStatusConditionType = "Progressing"

	// EgressRouterDegraded indicates that the router's current state does not match its
	// desired state over a period of time resulting in a lower quality of service.
	EgressRouterDegraded EgressRouterStatusConditionType = "Degraded"
)

// ConditionStatus defines the status of each of EgressRouterStatusConditionType.
type ConditionStatus string

// These are valid condition statuses. "ConditionTrue" means a resource is in the condition.
// "ConditionFalse" means a resource is not in the condition. "ConditionUnknown" means kubernetes
// can't decide if a resource is in the condition or not. In the future, we could add other
// intermediate conditions, e.g. ConditionDegraded.
const (
	ConditionTrue    ConditionStatus = "True"
	ConditionFalse   ConditionStatus = "False"
	ConditionUnknown ConditionStatus = "Unknown"
)

// EgressRouterStatusCondition represents the state of the egress router's
// managed and monitored components.
// +k8s:deepcopy-gen=true
type EgressRouterStatusCondition struct {
	// Type specifies the aspect reported by this condition; one of Available, Progressing, Degraded
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum="Available";"Progressing";"Degraded"
	// +required
	Type EgressRouterStatusConditionType `json:"type"`

	// Status of the condition, one of True, False, Unknown.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum="True";"False";"Unknown"
	// +required
	Status ConditionStatus `json:"status"`

	// LastTransitionTime is the time of the last update to the current status property.
	// +kubebuilder:validation:Required
	// +required
	LastTransitionTime metav1.Time `json:"lastTransitionTime"`

	// Reason is the CamelCase reason for the condition's current status.
	// +optional
	Reason string `json:"reason,omitempty"`

	// Message provides additional information about the current condition.
	// This is only to be consumed by humans.  It may contain Line Feed
	// characters (U+000A), which should be rendered as new lines.
	// +optional
	Message string `json:"message,omitempty"`
}

// EgressRouterStatus contains the observed status of EgressRouter. Read-only.
type EgressRouterStatus struct {
	// Observed status of the egress router
	// +kubebuilder:validation:Required
	Conditions []EgressRouterStatusCondition `json:"conditions,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// EgressRouterList is the list of egress router pods requested.
type EgressRouterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []EgressRouter `json:"items"`
}
