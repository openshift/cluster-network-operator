// Code generated by informer-gen. DO NOT EDIT.

package v1alpha1

import (
	internalinterfaces "github.com/openshift/client-go/machineconfiguration/informers/externalversions/internalinterfaces"
)

// Interface provides access to all the informers in this group version.
type Interface interface {
	// MachineConfigNodes returns a MachineConfigNodeInformer.
	MachineConfigNodes() MachineConfigNodeInformer
	// MachineOSBuilds returns a MachineOSBuildInformer.
	MachineOSBuilds() MachineOSBuildInformer
	// MachineOSConfigs returns a MachineOSConfigInformer.
	MachineOSConfigs() MachineOSConfigInformer
	// PinnedImageSets returns a PinnedImageSetInformer.
	PinnedImageSets() PinnedImageSetInformer
}

type version struct {
	factory          internalinterfaces.SharedInformerFactory
	namespace        string
	tweakListOptions internalinterfaces.TweakListOptionsFunc
}

// New returns a new Interface.
func New(f internalinterfaces.SharedInformerFactory, namespace string, tweakListOptions internalinterfaces.TweakListOptionsFunc) Interface {
	return &version{factory: f, namespace: namespace, tweakListOptions: tweakListOptions}
}

// MachineConfigNodes returns a MachineConfigNodeInformer.
func (v *version) MachineConfigNodes() MachineConfigNodeInformer {
	return &machineConfigNodeInformer{factory: v.factory, tweakListOptions: v.tweakListOptions}
}

// MachineOSBuilds returns a MachineOSBuildInformer.
func (v *version) MachineOSBuilds() MachineOSBuildInformer {
	return &machineOSBuildInformer{factory: v.factory, tweakListOptions: v.tweakListOptions}
}

// MachineOSConfigs returns a MachineOSConfigInformer.
func (v *version) MachineOSConfigs() MachineOSConfigInformer {
	return &machineOSConfigInformer{factory: v.factory, tweakListOptions: v.tweakListOptions}
}

// PinnedImageSets returns a PinnedImageSetInformer.
func (v *version) PinnedImageSets() PinnedImageSetInformer {
	return &pinnedImageSetInformer{factory: v.factory, tweakListOptions: v.tweakListOptions}
}
