// Code generated by lister-gen. DO NOT EDIT.

package v1beta1

import (
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	labels "k8s.io/apimachinery/pkg/labels"
	listers "k8s.io/client-go/listers"
	cache "k8s.io/client-go/tools/cache"
)

// MachineSetLister helps list MachineSets.
// All objects returned here must be treated as read-only.
type MachineSetLister interface {
	// List lists all MachineSets in the indexer.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*machinev1beta1.MachineSet, err error)
	// MachineSets returns an object that can list and get MachineSets.
	MachineSets(namespace string) MachineSetNamespaceLister
	MachineSetListerExpansion
}

// machineSetLister implements the MachineSetLister interface.
type machineSetLister struct {
	listers.ResourceIndexer[*machinev1beta1.MachineSet]
}

// NewMachineSetLister returns a new MachineSetLister.
func NewMachineSetLister(indexer cache.Indexer) MachineSetLister {
	return &machineSetLister{listers.New[*machinev1beta1.MachineSet](indexer, machinev1beta1.Resource("machineset"))}
}

// MachineSets returns an object that can list and get MachineSets.
func (s *machineSetLister) MachineSets(namespace string) MachineSetNamespaceLister {
	return machineSetNamespaceLister{listers.NewNamespaced[*machinev1beta1.MachineSet](s.ResourceIndexer, namespace)}
}

// MachineSetNamespaceLister helps list and get MachineSets.
// All objects returned here must be treated as read-only.
type MachineSetNamespaceLister interface {
	// List lists all MachineSets in the indexer for a given namespace.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*machinev1beta1.MachineSet, err error)
	// Get retrieves the MachineSet from the indexer for a given namespace and name.
	// Objects returned here must be treated as read-only.
	Get(name string) (*machinev1beta1.MachineSet, error)
	MachineSetNamespaceListerExpansion
}

// machineSetNamespaceLister implements the MachineSetNamespaceLister
// interface.
type machineSetNamespaceLister struct {
	listers.ResourceIndexer[*machinev1beta1.MachineSet]
}
