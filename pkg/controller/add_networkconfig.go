package controller

import (
	"github.com/openshift/cluster-network-operator/pkg/controller/clusterconfig"
	"github.com/openshift/cluster-network-operator/pkg/controller/operconfig"
)

func init() {
	// AddToManagerFuncs is a list of functions to create controllers and add them to a manager.
	AddToManagerFuncs = append(AddToManagerFuncs,
		operconfig.Add,
		clusterconfig.Add,
	)
}
