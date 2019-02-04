package controller

import (
	"github.com/openshift/cluster-network-operator/pkg/util/clusteroperator"
	operatorversion "github.com/openshift/cluster-network-operator/version"

	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// AddToManagerFuncs is a list of functions to add all Controllers to the Manager
var AddToManagerFuncs []func(manager.Manager, *clusteroperator.StatusManager) error

// AddToManager adds all Controllers to the Manager
func AddToManager(m manager.Manager) error {
	status := clusteroperator.NewStatusManager(m.GetClient(), "network", operatorversion.Version)

	for _, f := range AddToManagerFuncs {
		if err := f(m, status); err != nil {
			return err
		}
	}
	return nil
}
