package controller

import (
	"github.com/openshift/cluster-network-operator/pkg/controller/allowlist"
	"github.com/openshift/cluster-network-operator/pkg/controller/clusterconfig"
	configmapcainjector "github.com/openshift/cluster-network-operator/pkg/controller/configmap_ca_injector"
	"github.com/openshift/cluster-network-operator/pkg/controller/dashboards"
	"github.com/openshift/cluster-network-operator/pkg/controller/egress_router"
	"github.com/openshift/cluster-network-operator/pkg/controller/infrastructureconfig"
	"github.com/openshift/cluster-network-operator/pkg/controller/ingressconfig"
	"github.com/openshift/cluster-network-operator/pkg/controller/observability"
	"github.com/openshift/cluster-network-operator/pkg/controller/operconfig"
	"github.com/openshift/cluster-network-operator/pkg/controller/pki"
	"github.com/openshift/cluster-network-operator/pkg/controller/proxyconfig"
	signer "github.com/openshift/cluster-network-operator/pkg/controller/signer"
)

func init() {
	// AddToManagerFuncs is a list of functions to create controllers and add them to a manager.
	AddToManagerFuncs = append(AddToManagerFuncs,
		pki.Add,
		egress_router.Add,
		proxyconfig.Add,
		operconfig.Add,
		clusterconfig.Add,
		configmapcainjector.Add,
		signer.Add,
		ingressconfig.Add,
		infrastructureconfig.Add,
		allowlist.Add,
		dashboards.Add,
		observability.Add,
	)
}
