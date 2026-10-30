package platform

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/client/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestInfraStatusOpenStackMaxAllowedAddressPairs(t *testing.T) {
	t.Setenv("HYPERSHIFT", "false")
	for _, tc := range []struct {
		name      string
		platform  configv1.PlatformType
		openStack *configv1.OpenStackPlatformSpec
		want      int32
	}{
		{name: "configured", platform: configv1.OpenStackPlatformType, openStack: &configv1.OpenStackPlatformSpec{MaxAllowedAddressPairs: 42}, want: 42},
		{name: "unset limit", platform: configv1.OpenStackPlatformType, openStack: &configv1.OpenStackPlatformSpec{}},
		{name: "missing OpenStack spec", platform: configv1.OpenStackPlatformType},
		{name: "other platform ignores OpenStack spec", platform: configv1.NonePlatformType, openStack: &configv1.OpenStackPlatformSpec{MaxAllowedAddressPairs: 42}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			infra := &configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{PlatformSpec: configv1.PlatformSpec{
					Type: tc.platform, OpenStack: tc.openStack,
				}},
				Status: configv1.InfrastructureStatus{PlatformStatus: &configv1.PlatformStatus{Type: tc.platform}},
			}
			client := fake.NewFakeClient(infra, &configv1.Proxy{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}})
			status, err := InfraStatus(t.Context(), client)
			if err != nil {
				t.Fatalf("failed to bootstrap infrastructure status: %v", err)
			}
			if status.OpenStackMaxAllowedAddressPairs != tc.want {
				t.Errorf("OpenStackMaxAllowedAddressPairs = %d, want %d", status.OpenStackMaxAllowedAddressPairs, tc.want)
			}
		})
	}
}
