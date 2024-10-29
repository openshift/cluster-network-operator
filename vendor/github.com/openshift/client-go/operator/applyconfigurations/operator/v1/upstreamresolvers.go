// Code generated by applyconfiguration-gen. DO NOT EDIT.

package v1

import (
	operatorv1 "github.com/openshift/api/operator/v1"
)

// UpstreamResolversApplyConfiguration represents a declarative configuration of the UpstreamResolvers type for use
// with apply.
type UpstreamResolversApplyConfiguration struct {
	Upstreams        []UpstreamApplyConfiguration          `json:"upstreams,omitempty"`
	Policy           *operatorv1.ForwardingPolicy          `json:"policy,omitempty"`
	TransportConfig  *DNSTransportConfigApplyConfiguration `json:"transportConfig,omitempty"`
	ProtocolStrategy *operatorv1.ProtocolStrategy          `json:"protocolStrategy,omitempty"`
}

// UpstreamResolversApplyConfiguration constructs a declarative configuration of the UpstreamResolvers type for use with
// apply.
func UpstreamResolvers() *UpstreamResolversApplyConfiguration {
	return &UpstreamResolversApplyConfiguration{}
}

// WithUpstreams adds the given value to the Upstreams field in the declarative configuration
// and returns the receiver, so that objects can be build by chaining "With" function invocations.
// If called multiple times, values provided by each call will be appended to the Upstreams field.
func (b *UpstreamResolversApplyConfiguration) WithUpstreams(values ...*UpstreamApplyConfiguration) *UpstreamResolversApplyConfiguration {
	for i := range values {
		if values[i] == nil {
			panic("nil value passed to WithUpstreams")
		}
		b.Upstreams = append(b.Upstreams, *values[i])
	}
	return b
}

// WithPolicy sets the Policy field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Policy field is set to the value of the last call.
func (b *UpstreamResolversApplyConfiguration) WithPolicy(value operatorv1.ForwardingPolicy) *UpstreamResolversApplyConfiguration {
	b.Policy = &value
	return b
}

// WithTransportConfig sets the TransportConfig field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the TransportConfig field is set to the value of the last call.
func (b *UpstreamResolversApplyConfiguration) WithTransportConfig(value *DNSTransportConfigApplyConfiguration) *UpstreamResolversApplyConfiguration {
	b.TransportConfig = value
	return b
}

// WithProtocolStrategy sets the ProtocolStrategy field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the ProtocolStrategy field is set to the value of the last call.
func (b *UpstreamResolversApplyConfiguration) WithProtocolStrategy(value operatorv1.ProtocolStrategy) *UpstreamResolversApplyConfiguration {
	b.ProtocolStrategy = &value
	return b
}
