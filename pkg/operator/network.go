package operator

// Functions to actually generate network configurations

import (
	"github.com/pkg/errors"

	"github.com/openshift/openshift-network-operator/pkg/apis/networkoperator/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ValidateDefaultNetwork validates whichever network is specified
// as the default network.
func (h *Handler) ValidateDefaultNetwork() []error {
	conf := &h.config.Spec
	switch conf.DefaultNetwork.Type {
	case v1.NetworkTypeOpenshiftSDN:
		return h.validateOpenshiftSDN()
	default:
		return []error{errors.Errorf("unknown or unsupported NetworkType: %s", conf.DefaultNetwork.Type)}
	}
}

// RenderDefaultNetwork generates the manifests corresponding to the requested
// default network
func (h *Handler) RenderDefaultNetwork() ([]*uns.Unstructured, error) {
	dn := h.config.Spec.DefaultNetwork
	if errs := h.ValidateDefaultNetwork(); len(errs) > 0 {
		return nil, errors.Errorf("invalid Default Network configuration: %v", errs)
	}

	switch dn.Type {
	case v1.NetworkTypeOpenshiftSDN:
		return h.renderOpenshiftSDN(dn.OpenshiftSDNConfig)
	}

	return nil, errors.Errorf("unknown or unsupported NetworkType: %s", dn.Type)
}
