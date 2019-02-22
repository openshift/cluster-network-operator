// +build !ignore_autogenerated

// Code generated by deepcopy-gen. DO NOT EDIT.

package v1

import (
	runtime "k8s.io/apimachinery/pkg/runtime"
)

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *AdditionalNetworkDefinition) DeepCopyInto(out *AdditionalNetworkDefinition) {
	*out = *in
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new AdditionalNetworkDefinition.
func (in *AdditionalNetworkDefinition) DeepCopy() *AdditionalNetworkDefinition {
	if in == nil {
		return nil
	}
	out := new(AdditionalNetworkDefinition)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ClusterNetworkEntry) DeepCopyInto(out *ClusterNetworkEntry) {
	*out = *in
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ClusterNetworkEntry.
func (in *ClusterNetworkEntry) DeepCopy() *ClusterNetworkEntry {
	if in == nil {
		return nil
	}
	out := new(ClusterNetworkEntry)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *DefaultNetworkDefinition) DeepCopyInto(out *DefaultNetworkDefinition) {
	*out = *in
	if in.OpenShiftSDNConfig != nil {
		in, out := &in.OpenShiftSDNConfig, &out.OpenShiftSDNConfig
		*out = new(OpenShiftSDNConfig)
		(*in).DeepCopyInto(*out)
	}
	if in.OVNKubernetesConfig != nil {
		in, out := &in.OVNKubernetesConfig, &out.OVNKubernetesConfig
		*out = new(OVNKubernetesConfig)
		(*in).DeepCopyInto(*out)
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new DefaultNetworkDefinition.
func (in *DefaultNetworkDefinition) DeepCopy() *DefaultNetworkDefinition {
	if in == nil {
		return nil
	}
	out := new(DefaultNetworkDefinition)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *NetworkConfig) DeepCopyInto(out *NetworkConfig) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	out.Status = in.Status
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new NetworkConfig.
func (in *NetworkConfig) DeepCopy() *NetworkConfig {
	if in == nil {
		return nil
	}
	out := new(NetworkConfig)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *NetworkConfig) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *NetworkConfigList) DeepCopyInto(out *NetworkConfigList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	out.ListMeta = in.ListMeta
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]NetworkConfig, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new NetworkConfigList.
func (in *NetworkConfigList) DeepCopy() *NetworkConfigList {
	if in == nil {
		return nil
	}
	out := new(NetworkConfigList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *NetworkConfigList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *NetworkConfigSpec) DeepCopyInto(out *NetworkConfigSpec) {
	*out = *in
	if in.ClusterNetwork != nil {
		in, out := &in.ClusterNetwork, &out.ClusterNetwork
		*out = make([]ClusterNetworkEntry, len(*in))
		copy(*out, *in)
	}
	if in.ServiceNetwork != nil {
		in, out := &in.ServiceNetwork, &out.ServiceNetwork
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
	in.DefaultNetwork.DeepCopyInto(&out.DefaultNetwork)
	if in.AdditionalNetworks != nil {
		in, out := &in.AdditionalNetworks, &out.AdditionalNetworks
		*out = make([]AdditionalNetworkDefinition, len(*in))
		copy(*out, *in)
	}
	if in.DisableMultiNetwork != nil {
		in, out := &in.DisableMultiNetwork, &out.DisableMultiNetwork
		*out = new(bool)
		**out = **in
	}
	if in.DeployKubeProxy != nil {
		in, out := &in.DeployKubeProxy, &out.DeployKubeProxy
		*out = new(bool)
		**out = **in
	}
	if in.KubeProxyConfig != nil {
		in, out := &in.KubeProxyConfig, &out.KubeProxyConfig
		*out = new(ProxyConfig)
		(*in).DeepCopyInto(*out)
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new NetworkConfigSpec.
func (in *NetworkConfigSpec) DeepCopy() *NetworkConfigSpec {
	if in == nil {
		return nil
	}
	out := new(NetworkConfigSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *NetworkConfigStatus) DeepCopyInto(out *NetworkConfigStatus) {
	*out = *in
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new NetworkConfigStatus.
func (in *NetworkConfigStatus) DeepCopy() *NetworkConfigStatus {
	if in == nil {
		return nil
	}
	out := new(NetworkConfigStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *OVNKubernetesConfig) DeepCopyInto(out *OVNKubernetesConfig) {
	*out = *in
	if in.MTU != nil {
		in, out := &in.MTU, &out.MTU
		*out = new(uint32)
		**out = **in
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new OVNKubernetesConfig.
func (in *OVNKubernetesConfig) DeepCopy() *OVNKubernetesConfig {
	if in == nil {
		return nil
	}
	out := new(OVNKubernetesConfig)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *OpenShiftSDNConfig) DeepCopyInto(out *OpenShiftSDNConfig) {
	*out = *in
	if in.VXLANPort != nil {
		in, out := &in.VXLANPort, &out.VXLANPort
		*out = new(uint32)
		**out = **in
	}
	if in.MTU != nil {
		in, out := &in.MTU, &out.MTU
		*out = new(uint32)
		**out = **in
	}
	if in.UseExternalOpenvswitch != nil {
		in, out := &in.UseExternalOpenvswitch, &out.UseExternalOpenvswitch
		*out = new(bool)
		**out = **in
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new OpenShiftSDNConfig.
func (in *OpenShiftSDNConfig) DeepCopy() *OpenShiftSDNConfig {
	if in == nil {
		return nil
	}
	out := new(OpenShiftSDNConfig)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ProxyConfig) DeepCopyInto(out *ProxyConfig) {
	*out = *in
	if in.ProxyArguments != nil {
		in, out := &in.ProxyArguments, &out.ProxyArguments
		*out = make(map[string][]string, len(*in))
		for key, val := range *in {
			var outVal []string
			if val == nil {
				(*out)[key] = nil
			} else {
				in, out := &val, &outVal
				*out = make([]string, len(*in))
				copy(*out, *in)
			}
			(*out)[key] = outVal
		}
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ProxyConfig.
func (in *ProxyConfig) DeepCopy() *ProxyConfig {
	if in == nil {
		return nil
	}
	out := new(ProxyConfig)
	in.DeepCopyInto(out)
	return out
}
