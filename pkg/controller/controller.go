package controller

import (
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// AddToManagerFuncs is a list of functions to add all Controllers to the Manager
var AddToManagerFuncs []func(manager.Manager, *statusmanager.StatusManager, cnoclient.Client, featuregates.FeatureGate) error

// AddToManager adds all Controllers to the Manager
func AddToManager(m manager.Manager, sm *statusmanager.StatusManager, c cnoclient.Client, featureGates featuregates.FeatureGate) error {
	for _, f := range AddToManagerFuncs {
		if err := f(m, sm, c, featureGates); err != nil {
			return err
		}
	}
	if err := sm.AddPodWatcher(m); err != nil {
		return err
	}
	return nil
}
