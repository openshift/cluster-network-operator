package controller

import (
	"github.com/openshift/cluster-network-operator/pkg/controller/clusterconfig"
	"github.com/openshift/cluster-network-operator/pkg/controller/configmap_ca_injector"
	"github.com/openshift/cluster-network-operator/pkg/controller/operconfig"
	"github.com/openshift/cluster-network-operator/pkg/controller/proxyconfig"
)

func init() {
	// AddToManagerFuncs is a list of functions to create controllers and add them to a manager.
	AddToManagerFuncs = append(AddToManagerFuncs,
		proxyconfig.Add,
		operconfig.Add,
		clusterconfig.Add,
		operconfig.AddConfigMapReconciler,
		configmapcainjector.Add,
	)
}
