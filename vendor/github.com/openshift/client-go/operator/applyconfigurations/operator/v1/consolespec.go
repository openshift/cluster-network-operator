// Code generated by applyconfiguration-gen. DO NOT EDIT.

package v1

import (
	operatorv1 "github.com/openshift/api/operator/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
)

// ConsoleSpecApplyConfiguration represents a declarative configuration of the ConsoleSpec type for use
// with apply.
type ConsoleSpecApplyConfiguration struct {
	OperatorSpecApplyConfiguration `json:",inline"`
	Customization                  *ConsoleCustomizationApplyConfiguration `json:"customization,omitempty"`
	Providers                      *ConsoleProvidersApplyConfiguration     `json:"providers,omitempty"`
	Route                          *ConsoleConfigRouteApplyConfiguration   `json:"route,omitempty"`
	Plugins                        []string                                `json:"plugins,omitempty"`
	Ingress                        *IngressApplyConfiguration              `json:"ingress,omitempty"`
}

// ConsoleSpecApplyConfiguration constructs a declarative configuration of the ConsoleSpec type for use with
// apply.
func ConsoleSpec() *ConsoleSpecApplyConfiguration {
	return &ConsoleSpecApplyConfiguration{}
}

// WithManagementState sets the ManagementState field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the ManagementState field is set to the value of the last call.
func (b *ConsoleSpecApplyConfiguration) WithManagementState(value operatorv1.ManagementState) *ConsoleSpecApplyConfiguration {
	b.OperatorSpecApplyConfiguration.ManagementState = &value
	return b
}

// WithLogLevel sets the LogLevel field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the LogLevel field is set to the value of the last call.
func (b *ConsoleSpecApplyConfiguration) WithLogLevel(value operatorv1.LogLevel) *ConsoleSpecApplyConfiguration {
	b.OperatorSpecApplyConfiguration.LogLevel = &value
	return b
}

// WithOperatorLogLevel sets the OperatorLogLevel field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the OperatorLogLevel field is set to the value of the last call.
func (b *ConsoleSpecApplyConfiguration) WithOperatorLogLevel(value operatorv1.LogLevel) *ConsoleSpecApplyConfiguration {
	b.OperatorSpecApplyConfiguration.OperatorLogLevel = &value
	return b
}

// WithUnsupportedConfigOverrides sets the UnsupportedConfigOverrides field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the UnsupportedConfigOverrides field is set to the value of the last call.
func (b *ConsoleSpecApplyConfiguration) WithUnsupportedConfigOverrides(value runtime.RawExtension) *ConsoleSpecApplyConfiguration {
	b.OperatorSpecApplyConfiguration.UnsupportedConfigOverrides = &value
	return b
}

// WithObservedConfig sets the ObservedConfig field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the ObservedConfig field is set to the value of the last call.
func (b *ConsoleSpecApplyConfiguration) WithObservedConfig(value runtime.RawExtension) *ConsoleSpecApplyConfiguration {
	b.OperatorSpecApplyConfiguration.ObservedConfig = &value
	return b
}

// WithCustomization sets the Customization field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Customization field is set to the value of the last call.
func (b *ConsoleSpecApplyConfiguration) WithCustomization(value *ConsoleCustomizationApplyConfiguration) *ConsoleSpecApplyConfiguration {
	b.Customization = value
	return b
}

// WithProviders sets the Providers field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Providers field is set to the value of the last call.
func (b *ConsoleSpecApplyConfiguration) WithProviders(value *ConsoleProvidersApplyConfiguration) *ConsoleSpecApplyConfiguration {
	b.Providers = value
	return b
}

// WithRoute sets the Route field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Route field is set to the value of the last call.
func (b *ConsoleSpecApplyConfiguration) WithRoute(value *ConsoleConfigRouteApplyConfiguration) *ConsoleSpecApplyConfiguration {
	b.Route = value
	return b
}

// WithPlugins adds the given value to the Plugins field in the declarative configuration
// and returns the receiver, so that objects can be build by chaining "With" function invocations.
// If called multiple times, values provided by each call will be appended to the Plugins field.
func (b *ConsoleSpecApplyConfiguration) WithPlugins(values ...string) *ConsoleSpecApplyConfiguration {
	for i := range values {
		b.Plugins = append(b.Plugins, values[i])
	}
	return b
}

// WithIngress sets the Ingress field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Ingress field is set to the value of the last call.
func (b *ConsoleSpecApplyConfiguration) WithIngress(value *IngressApplyConfiguration) *ConsoleSpecApplyConfiguration {
	b.Ingress = value
	return b
}
