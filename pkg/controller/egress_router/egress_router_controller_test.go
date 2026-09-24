package egress_router

import (
	"context"
	"strings"
	"testing"

	netopv1 "github.com/openshift/api/networkoperator/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestIsItValidCidr guards the boundary between CIDR notation and bare IPs
// that net.ParseCIDR does not distinguish via its error alone.
func TestIsItValidCidr(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"valid IPv4 CIDR", "172.25.250.88/24", true},
		{"valid IPv4 CIDR with /32", "10.0.0.1/32", true},
		{"valid IPv6 CIDR", "fd00::1/64", true},
		{"bare IPv4 address without mask", "172.25.250.88", false},
		{"bare IPv6 address without mask", "fd00::1", false},
		{"empty string", "", false},
		{"garbage input", "not-an-ip", false},
		{"CIDR with invalid prefix length", "172.25.250.88/33", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isItValidCidr(tt.input); got != tt.want {
				t.Errorf("isItValidCidr(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// TestIsItValidIPAddress ensures CIDR notation is rejected for gateway fields
// where only a bare IP is expected.
func TestIsItValidIPAddress(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"valid IPv4 address", "172.25.250.254", true},
		{"valid IPv6 address", "fd00::1", true},
		{"valid loopback address", "127.0.0.1", true},
		{"IPv4 with CIDR notation", "172.25.250.254/24", false},
		{"empty string", "", false},
		{"garbage input", "not-an-ip", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isItValidIPAddress(tt.input); got != tt.want {
				t.Errorf("isItValidIPAddress(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

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
	ctx := context.Background()
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
				if err != nil && (strings.Contains(err.Error(), "spec.addresses") || strings.Contains(err.Error(), "router without addresses")) {
					t.Errorf("expected no validation error, got: %v", err)
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
