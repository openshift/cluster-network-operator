package egress_router

import (
	"errors"
	"os"
	"strings"
	"testing"

	netopv1 "github.com/openshift/api/networkoperator/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// makeRouter builds an EgressRouter with the given IP and gateway for use in
// validation tests. It avoids repeating the full EgressRouter construction.
func makeRouter(ip, gateway string) *netopv1.EgressRouter {
	return &netopv1.EgressRouter{
		Spec: netopv1.EgressRouterSpec{
			Addresses: []netopv1.EgressRouterAddress{{IP: ip, Gateway: gateway}},
			Mode:      netopv1.EgressRouterModeRedirect,
			Redirect:  &netopv1.RedirectConfig{RedirectRules: []netopv1.L4RedirectRule{}},
		},
	}
}

// TestEnsureEgressRouterValidation confirms that ensureEgressRouter itself
// returns errors rather than silently skipping invalid fields, which was the
// original bug this change fixes.
func TestEnsureEgressRouterValidation(t *testing.T) {
	reconciler := &EgressRouterReconciler{}
	ctx := t.Context()
	ownerRefs := []metav1.OwnerReference{}

	tests := []struct {
		name        string
		router      *netopv1.EgressRouter
		errContains string
	}{
		{
			name: "empty addresses",
			router: &netopv1.EgressRouter{
				Spec: netopv1.EgressRouterSpec{
					Addresses: []netopv1.EgressRouterAddress{},
					Mode:      netopv1.EgressRouterModeRedirect,
					Redirect:  &netopv1.RedirectConfig{},
				},
			},
			errContains: "router without addresses",
		},
		{
			name:        "bare IP without CIDR mask",
			router:      makeRouter("172.25.250.88", "172.25.250.254"),
			errContains: "spec.addresses[0].ip is invalid",
		},
		{
			name:        "CIDR with invalid prefix length",
			router:      makeRouter("172.25.250.88/33", "172.25.250.254"),
			errContains: "spec.addresses[0].ip is invalid",
		},
		{
			name:        "invalid gateway",
			router:      makeRouter("172.25.250.88/24", "not-a-valid-ip"),
			errContains: "spec.addresses[0].gateway is invalid",
		},
		{
			name:        "gateway in CIDR notation",
			router:      makeRouter("172.25.250.88/24", "172.25.250.254/24"),
			errContains: "spec.addresses[0].gateway is invalid",
		},
		{
			name:        "omitted gateway passes validation",
			router:      makeRouter("172.25.250.88/24", ""),
			errContains: "",
		},
		{
			name:        "valid CIDR and gateway passes validation",
			router:      makeRouter("172.25.250.88/24", "172.25.250.254"),
			errContains: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := reconciler.ensureEgressRouter(ctx, "testdata/nonexistent", "test-ns", tt.router, ownerRefs)
			if tt.errContains == "" {
				if err == nil {
					t.Fatalf("expected render error for nonexistent manifest dir, got nil")
				}
				if !errors.Is(err, os.ErrNotExist) {
					t.Errorf("expected render directory-not-found error, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.errContains)
			}
			if !strings.Contains(err.Error(), tt.errContains) {
				t.Errorf("expected error containing %q, got: %v", tt.errContains, err)
			}
		})
	}
}
