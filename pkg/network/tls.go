package network

import (
	"context"
	"fmt"
	"strings"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/hypershift"
	openshifttls "github.com/openshift/controller-runtime-common/pkg/tls"
	"github.com/openshift/library-go/pkg/crypto"
	"k8s.io/apimachinery/pkg/types"
)

const (
	// UseTLSProfileKey is the template data key indicating whether to use the cluster TLS profile
	UseTLSProfileKey = "UseTLSProfile"
	// TLSMinVersionKey is the template data key for the minimum TLS version
	TLSMinVersionKey = "TLSMinVersion"
	// TLSCipherSuitesKey is the template data key for the comma-separated cipher suites
	TLSCipherSuitesKey = "TLSCipherSuites"
)

// addTLSInfoToRenderData adds TLS-related template data to the render data.
func addTLSInfoToRenderData(data map[string]interface{}, bootstrapResult *bootstrap.BootstrapResult, respectAdherence bool) {
	if respectAdherence && !crypto.ShouldHonorClusterTLSProfile(bootstrapResult.TLSProfile.Adherence) {
		data[UseTLSProfileKey] = false
		return
	}

	data[TLSMinVersionKey] = bootstrapResult.TLSProfile.Spec.MinTLSVersion
	data[TLSCipherSuitesKey] = strings.Join(bootstrapResult.TLSProfile.Spec.Ciphers, ",")
	data[UseTLSProfileKey] = true
}

// GetTLSProfile fetches the TLS profile from either the APIServer (standalone) or HostedControlPlane (HyperShift)
func GetTLSProfile(client cnoclient.Client, hcp *hypershift.HostedControlPlane) (bootstrap.TLSProfile, error) {
	// For HyperShift, read TLS profile from the already-parsed HostedControlPlane
	if hcp != nil {
		if hcp.APIServerSpec == nil {
			// No APIServer spec, use defaults
			return toTLSProfile(&configv1.APIServerSpec{})
		}
		return toTLSProfile(hcp.APIServerSpec)
	}

	// For non-HyperShift, read from APIServer CR in the default cluster
	apiServer := &configv1.APIServer{}
	if err := client.Default().CRClient().Get(context.TODO(), types.NamespacedName{Name: openshifttls.APIServerName}, apiServer); err != nil {
		return bootstrap.TLSProfile{}, fmt.Errorf("failed to fetch apiserver.config.openshift.io/%s: %w", openshifttls.APIServerName, err)
	}

	return toTLSProfile(&apiServer.Spec)
}

func toTLSProfile(apiServerSpec *configv1.APIServerSpec) (bootstrap.TLSProfile, error) {
	profileSpec, err := openshifttls.GetTLSProfileSpec(apiServerSpec.TLSSecurityProfile)
	if err != nil {
		return bootstrap.TLSProfile{}, fmt.Errorf("failed to get TLS profile spec: %w", err)
	}

	return bootstrap.TLSProfile{
		Spec:      profileSpec,
		Adherence: apiServerSpec.TLSAdherence,
	}, nil
}
