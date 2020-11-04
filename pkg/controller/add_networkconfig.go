package controller

import (
	"github.com/openshift/cluster-network-operator/pkg/controller/clusterconfig"
	configmapcainjector "github.com/openshift/cluster-network-operator/pkg/controller/configmap_ca_injector"
	"github.com/openshift/cluster-network-operator/pkg/controller/operconfig"
	"github.com/openshift/cluster-network-operator/pkg/controller/pki"
	"github.com/openshift/cluster-network-operator/pkg/controller/proxyconfig"
	ipsec "github.com/openshift/cluster-network-operator/pkg/controller/ipsec"
)

func init() {
	// AddToManagerFuncs is a list of functions to create controllers and add them to a manager.
	AddToManagerFuncs = append(AddToManagerFuncs,
		pki.Add,
		proxyconfig.Add,
		operconfig.Add,
		clusterconfig.Add,
		configmapcainjector.Add,
		ipsec.Add,
	)
}
