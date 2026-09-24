package egress_router

import (
	"context"
	"strings"
	"testing"

	netopv1 "github.com/openshift/api/networkoperator/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIsItValidCidr(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name:  "valid IPv4 CIDR",
			input: "172.25.250.88/24",
			want:  true,
		},
		{
			name:  "valid IPv4 CIDR with /32",
			input: "10.0.0.1/32",
			want:  true,
		},
		{
			name:  "valid IPv6 CIDR",
			input: "fd00::1/64",
			want:  true,
		},
		{
			name:  "bare IPv4 address without mask",
			input: "172.25.250.88",
			want:  false,
		},
		{
			name:  "bare IPv6 address without mask",
			input: "fd00::1",
			want:  false,
		},
		{
			name:  "empty string",
			input: "",
			want:  false,
		},
		{
			name:  "garbage input",
			input: "not-an-ip",
			want:  false,
		},
		{
			name:  "CIDR with invalid prefix length",
			input: "172.25.250.88/33",
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isItValidCidr(tt.input)
			if got != tt.want {
				t.Errorf("isItValidCidr(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsItValidIPAddress(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name:  "valid IPv4 address",
			input: "172.25.250.254",
			want:  true,
		},
		{
			name:  "valid IPv6 address",
			input: "fd00::1",
			want:  true,
		},
		{
			name:  "valid loopback address",
			input: "127.0.0.1",
			want:  true,
		},
		{
			name:  "IPv4 with CIDR notation",
			input: "172.25.250.254/24",
			want:  false,
		},
		{
			name:  "empty string",
			input: "",
			want:  false,
		},
		{
			name:  "garbage input",
			input: "not-an-ip",
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isItValidIPAddress(tt.input)
			if got != tt.want {
				t.Errorf("isItValidIPAddress(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

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
			name: "empty addresses returns error",
			router: &netopv1.EgressRouter{
				Spec: netopv1.EgressRouterSpec{
					Addresses: []netopv1.EgressRouterAddress{},
					Mode:      netopv1.EgressRouterModeRedirect,
					Redirect: netopv1.RedirectConfig{
						RedirectRules: []netopv1.L4RedirectRule{},
					},
				},
			},
			errContains: "router without addresses",
		},
		{
			name: "bare IP without CIDR mask returns error",
			router: &netopv1.EgressRouter{
				Spec: netopv1.EgressRouterSpec{
					Addresses: []netopv1.EgressRouterAddress{
						{IP: "172.25.250.88", Gateway: "172.25.250.254"},
					},
					Mode: netopv1.EgressRouterModeRedirect,
					Redirect: netopv1.RedirectConfig{
						RedirectRules: []netopv1.L4RedirectRule{},
					},
				},
			},
			errContains: "spec.addresses[0].ip is invalid",
		},
		{
			name: "CIDR with invalid prefix length returns error",
			router: &netopv1.EgressRouter{
				Spec: netopv1.EgressRouterSpec{
					Addresses: []netopv1.EgressRouterAddress{
						{IP: "172.25.250.88/33", Gateway: "172.25.250.254"},
					},
					Mode: netopv1.EgressRouterModeRedirect,
					Redirect: netopv1.RedirectConfig{
						RedirectRules: []netopv1.L4RedirectRule{},
					},
				},
			},
			errContains: "spec.addresses[0].ip is invalid",
		},
		{
			name: "valid CIDR with invalid gateway returns error",
			router: &netopv1.EgressRouter{
				Spec: netopv1.EgressRouterSpec{
					Addresses: []netopv1.EgressRouterAddress{
						{IP: "172.25.250.88/24", Gateway: "not-a-valid-ip"},
					},
					Mode: netopv1.EgressRouterModeRedirect,
					Redirect: netopv1.RedirectConfig{
						RedirectRules: []netopv1.L4RedirectRule{},
					},
				},
			},
			errContains: "spec.addresses[0].gateway is invalid",
		},
		{
			name: "valid CIDR with gateway in CIDR notation returns error",
			router: &netopv1.EgressRouter{
				Spec: netopv1.EgressRouterSpec{
					Addresses: []netopv1.EgressRouterAddress{
						{IP: "172.25.250.88/24", Gateway: "172.25.250.254/24"},
					},
					Mode: netopv1.EgressRouterModeRedirect,
					Redirect: netopv1.RedirectConfig{
						RedirectRules: []netopv1.L4RedirectRule{},
					},
				},
			},
			errContains: "spec.addresses[0].gateway is invalid",
		},
		{
			name: "valid CIDR and valid gateway passes validation",
			router: &netopv1.EgressRouter{
				Spec: netopv1.EgressRouterSpec{
					Addresses: []netopv1.EgressRouterAddress{
						{IP: "172.25.250.88/24", Gateway: "172.25.250.254"},
					},
					Mode: netopv1.EgressRouterModeRedirect,
					Redirect: netopv1.RedirectConfig{
						RedirectRules: []netopv1.L4RedirectRule{
							{DestinationIP: "172.25.250.220", Port: 8080, Protocol: "TCP"},
						},
					},
				},
			},
			errContains: "", // no validation error; will fail later at render
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := reconciler.ensureEgressRouter(ctx, "testdata/nonexistent", "test-ns", tt.router, ownerRefs)
			if tt.errContains == "" {
				// Valid spec passes validation; expect a render/path error, not a validation error
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
