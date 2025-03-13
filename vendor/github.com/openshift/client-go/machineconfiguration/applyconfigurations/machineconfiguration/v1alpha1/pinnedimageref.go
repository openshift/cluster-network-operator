// Code generated by applyconfiguration-gen. DO NOT EDIT.

package v1alpha1

// PinnedImageRefApplyConfiguration represents a declarative configuration of the PinnedImageRef type for use
// with apply.
type PinnedImageRefApplyConfiguration struct {
	Name *string `json:"name,omitempty"`
}

// PinnedImageRefApplyConfiguration constructs a declarative configuration of the PinnedImageRef type for use with
// apply.
func PinnedImageRef() *PinnedImageRefApplyConfiguration {
	return &PinnedImageRefApplyConfiguration{}
}

// WithName sets the Name field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Name field is set to the value of the last call.
func (b *PinnedImageRefApplyConfiguration) WithName(value string) *PinnedImageRefApplyConfiguration {
	b.Name = &value
	return b
}
