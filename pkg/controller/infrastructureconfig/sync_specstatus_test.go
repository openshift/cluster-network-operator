package infrastructureconfig

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func Test_SpecStatusSynchronizer(t *testing.T) {
	tests := []struct {
		name        string
		givenInfra  configv1.Infrastructure
		wantedInfra configv1.Infrastructure
		wantedErr   string
	}{
		{
			name: "succeed: spec is empty, status with value: should propagate spec from status",
			givenInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						BareMetal: &configv1.BareMetalPlatformSpec{},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.BareMetalPlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "BareMetal",
						BareMetal: &configv1.BareMetalPlatformStatus{
							APIServerInternalIPs: []string{"224.0.0.1"},
							IngressIPs:           []string{"224.0.0.2"},
						},
					},
				},
			},
			wantedInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						BareMetal: &configv1.BareMetalPlatformSpec{
							APIServerInternalIPs: []configv1.IP{"224.0.0.1"},
							IngressIPs:           []configv1.IP{"224.0.0.2"},
							MachineNetworks:      []configv1.CIDR{},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.BareMetalPlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "BareMetal",
						BareMetal: &configv1.BareMetalPlatformStatus{
							APIServerInternalIPs: []string{"224.0.0.1"},
							IngressIPs:           []string{"224.0.0.2"},
							MachineNetworks:      []configv1.CIDR{},
						},
					},
				},
			},
		},
		{
			name: "succeed: no changes, spec equals status",
			givenInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						BareMetal: &configv1.BareMetalPlatformSpec{
							APIServerInternalIPs: []configv1.IP{"224.0.0.1"},
							IngressIPs:           []configv1.IP{"224.0.0.2"},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.BareMetalPlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "BareMetal",
						BareMetal: &configv1.BareMetalPlatformStatus{
							APIServerInternalIPs: []string{"224.0.0.1"},
							IngressIPs:           []string{"224.0.0.2"},
						},
					},
				},
			},
			wantedInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						BareMetal: &configv1.BareMetalPlatformSpec{
							APIServerInternalIPs: []configv1.IP{"224.0.0.1"},
							IngressIPs:           []configv1.IP{"224.0.0.2"},
							MachineNetworks:      []configv1.CIDR{},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.BareMetalPlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "BareMetal",
						BareMetal: &configv1.BareMetalPlatformStatus{
							APIServerInternalIPs: []string{"224.0.0.1"},
							IngressIPs:           []string{"224.0.0.2"},
							MachineNetworks:      []configv1.CIDR{},
						},
					},
				},
			},
		},
		{
			name: "succeed: add additional pair of vips; empty machine networks",
			givenInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						BareMetal: &configv1.BareMetalPlatformSpec{
							APIServerInternalIPs: []configv1.IP{"224.0.0.1", "2001:0DB8::1"},
							IngressIPs:           []configv1.IP{"224.0.0.2", "2001:0DB8::2"},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.BareMetalPlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "BareMetal",
						BareMetal: &configv1.BareMetalPlatformStatus{
							APIServerInternalIPs: []string{"224.0.0.1"},
							IngressIPs:           []string{"224.0.0.2"},
						},
					},
				},
			},
			wantedInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						BareMetal: &configv1.BareMetalPlatformSpec{
							APIServerInternalIPs: []configv1.IP{"224.0.0.1", "2001:0DB8::1"},
							IngressIPs:           []configv1.IP{"224.0.0.2", "2001:0DB8::2"},
							MachineNetworks:      []configv1.CIDR{},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.BareMetalPlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "BareMetal",
						BareMetal: &configv1.BareMetalPlatformStatus{
							APIServerInternalIPs: []string{"224.0.0.1", "2001:0DB8::1"},
							IngressIPs:           []string{"224.0.0.2", "2001:0DB8::2"},
							MachineNetworks:      []configv1.CIDR{},
						},
					},
				},
			},
		},
		{
			name: "fail: modifying first pair of vips forbidden",
			givenInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						BareMetal: &configv1.BareMetalPlatformSpec{
							APIServerInternalIPs: []configv1.IP{"2001:0DB8::1"},
							IngressIPs:           []configv1.IP{"2001:0DB8::2"},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.BareMetalPlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "BareMetal",
						BareMetal: &configv1.BareMetalPlatformStatus{
							APIServerInternalIPs: []string{"224.0.0.1"},
							IngressIPs:           []string{"224.0.0.2"},
						},
					},
				},
			},
			wantedErr: "first VIP cannot be modified",
		},
		{
			name: "fail: removing machine networks from spec is forbidden",
			givenInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						BareMetal: &configv1.BareMetalPlatformSpec{
							APIServerInternalIPs: []configv1.IP{"2001:0DB8::1"},
							IngressIPs:           []configv1.IP{"2001:0DB8::2"},
							MachineNetworks:      []configv1.CIDR{},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.BareMetalPlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "BareMetal",
						BareMetal: &configv1.BareMetalPlatformStatus{
							APIServerInternalIPs: []string{"2001:0DB8::1"},
							IngressIPs:           []string{"2001:0DB8::2"},
							MachineNetworks:      []configv1.CIDR{"2001:0DB8::0/64"},
						},
					},
				},
			},
			wantedErr: "removing machine networks is forbidden",
		},
		{
			name: "fail: multiple vips of the same IP stack forbidden",
			givenInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						BareMetal: &configv1.BareMetalPlatformSpec{
							APIServerInternalIPs: []configv1.IP{"2001:0DB8::1", "2001:0DB8::10"},
							IngressIPs:           []configv1.IP{"2001:0DB8::2", "2001:0DB8::20"},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.BareMetalPlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "BareMetal",
						BareMetal: &configv1.BareMetalPlatformStatus{
							APIServerInternalIPs: []string{"224.0.0.1"},
							IngressIPs:           []string{"224.0.0.2"},
						},
					},
				},
			},
			wantedErr: "at least one from each IP family is required",
		},
		{
			name: "fail: vip from outside of machine network",
			givenInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						BareMetal: &configv1.BareMetalPlatformSpec{
							APIServerInternalIPs: []configv1.IP{"224.0.1.1"},
							IngressIPs:           []configv1.IP{"224.0.1.2"},
							MachineNetworks:      []configv1.CIDR{"224.0.0.1/24", "224.1.0.1/24"},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.BareMetalPlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "BareMetal",
						BareMetal: &configv1.BareMetalPlatformStatus{
							APIServerInternalIPs: []string{"224.0.0.1"},
							IngressIPs:           []string{"224.0.0.2"},
							MachineNetworks:      []configv1.CIDR{"224.0.0.1/24", "224.1.0.1/24"},
						},
					},
				},
			},
			wantedErr: "cannot be found in any machine network",
		},
		{
			name: "fail: duplicate vips without ELB",
			givenInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						BareMetal: &configv1.BareMetalPlatformSpec{
							APIServerInternalIPs: []configv1.IP{"224.0.1.1"},
							IngressIPs:           []configv1.IP{"224.0.1.1"},
							MachineNetworks:      []configv1.CIDR{"224.0.0.1/24", "224.0.1.1/24"},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.BareMetalPlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "BareMetal",
						BareMetal: &configv1.BareMetalPlatformStatus{
							APIServerInternalIPs: []string{"224.0.0.1"},
							IngressIPs:           []string{"224.0.0.2"},
							MachineNetworks:      []configv1.CIDR{"224.0.0.1/24"},
						},
					},
				},
			},
			wantedErr: "VIPs cannot be equal",
		},
		{
			name: "succeed: duplicate vips with ELB",
			givenInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						BareMetal: &configv1.BareMetalPlatformSpec{
							APIServerInternalIPs: []configv1.IP{"224.0.0.1"},
							IngressIPs:           []configv1.IP{"224.0.0.1"},
							MachineNetworks:      []configv1.CIDR{"224.0.0.1/24", "224.0.1.1/24"},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.BareMetalPlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "BareMetal",
						BareMetal: &configv1.BareMetalPlatformStatus{
							APIServerInternalIPs: []string{"224.0.0.1"},
							IngressIPs:           []string{"224.0.0.1"},
							MachineNetworks:      []configv1.CIDR{"224.0.0.1/24"},
							LoadBalancer:         &configv1.BareMetalPlatformLoadBalancer{Type: configv1.LoadBalancerTypeUserManaged},
						},
					},
				},
			},
			wantedInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						BareMetal: &configv1.BareMetalPlatformSpec{
							APIServerInternalIPs: []configv1.IP{"224.0.0.1"},
							IngressIPs:           []configv1.IP{"224.0.0.1"},
							MachineNetworks:      []configv1.CIDR{"224.0.0.1/24", "224.0.1.1/24"},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.BareMetalPlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "BareMetal",
						BareMetal: &configv1.BareMetalPlatformStatus{
							APIServerInternalIPs: []string{"224.0.0.1"},
							IngressIPs:           []string{"224.0.0.1"},
							MachineNetworks:      []configv1.CIDR{"224.0.0.1/24", "224.0.1.1/24"},
							LoadBalancer:         &configv1.BareMetalPlatformLoadBalancer{Type: configv1.LoadBalancerTypeUserManaged},
						},
					},
				},
			},
		},
		{
			name: "fail: non-equal number of vips",
			givenInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						BareMetal: &configv1.BareMetalPlatformSpec{
							APIServerInternalIPs: []configv1.IP{"224.0.1.1", "2001:0DB8::1"},
							IngressIPs:           []configv1.IP{"224.0.1.2"},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.BareMetalPlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "BareMetal",
						BareMetal: &configv1.BareMetalPlatformStatus{
							APIServerInternalIPs: []string{"224.0.0.1"},
							IngressIPs:           []string{"224.0.0.2"},
						},
					},
				},
			},
			wantedErr: "does not match number",
		},
		{
			name: "fail: too many vips",
			givenInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						BareMetal: &configv1.BareMetalPlatformSpec{
							APIServerInternalIPs: []configv1.IP{"224.0.1.1", "2001:0DB8::1", "2001:0DB8::10"},
							IngressIPs:           []configv1.IP{"224.0.1.2", "2001:0DB8::2", "2001:0DB8::20"},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.BareMetalPlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "BareMetal",
						BareMetal: &configv1.BareMetalPlatformStatus{
							APIServerInternalIPs: []string{"224.0.0.1"},
							IngressIPs:           []string{"224.0.0.2"},
						},
					},
				},
			},
			wantedErr: "number of API VIPs needs to be less or equal to 2",
		},
		{
			name: "succeed: add pair of IPv6 vips; non-empty machine networks",
			givenInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						BareMetal: &configv1.BareMetalPlatformSpec{
							APIServerInternalIPs: []configv1.IP{"10.0.0.1", "fe80:1:2:3::1"},
							IngressIPs:           []configv1.IP{"10.0.0.2", "fe80:1:2:3::2"},
							MachineNetworks:      []configv1.CIDR{"10.0.0.0/24", "fe80:1:2:3::/64"},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.BareMetalPlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "BareMetal",
						BareMetal: &configv1.BareMetalPlatformStatus{
							APIServerInternalIPs: []string{"10.0.0.1"},
							IngressIPs:           []string{"10.0.0.2"},
							MachineNetworks:      []configv1.CIDR{"10.0.0.0/24"},
						},
					},
				},
			},
			wantedInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						BareMetal: &configv1.BareMetalPlatformSpec{
							APIServerInternalIPs: []configv1.IP{"10.0.0.1", "fe80:1:2:3::1"},
							IngressIPs:           []configv1.IP{"10.0.0.2", "fe80:1:2:3::2"},
							MachineNetworks:      []configv1.CIDR{"10.0.0.0/24", "fe80:1:2:3::/64"},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.BareMetalPlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "BareMetal",
						BareMetal: &configv1.BareMetalPlatformStatus{
							APIServerInternalIPs: []string{"10.0.0.1", "fe80:1:2:3::1"},
							IngressIPs:           []string{"10.0.0.2", "fe80:1:2:3::2"},
							MachineNetworks:      []configv1.CIDR{"10.0.0.0/24", "fe80:1:2:3::/64"},
						},
					},
				},
			},
		},
		{
			name: "succeed: delete 2nd pair of vips",
			givenInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						BareMetal: &configv1.BareMetalPlatformSpec{
							APIServerInternalIPs: []configv1.IP{"224.0.0.1"},
							IngressIPs:           []configv1.IP{"224.0.0.2"},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.BareMetalPlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "BareMetal",
						BareMetal: &configv1.BareMetalPlatformStatus{
							APIServerInternalIPs: []string{"224.0.0.1", "2001:0DB8::1"},
							IngressIPs:           []string{"224.0.0.2", "2001:0DB8::2"},
						},
					},
				},
			},
			wantedInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						BareMetal: &configv1.BareMetalPlatformSpec{
							APIServerInternalIPs: []configv1.IP{"224.0.0.1"},
							IngressIPs:           []configv1.IP{"224.0.0.2"},
							MachineNetworks:      []configv1.CIDR{},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.BareMetalPlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "BareMetal",
						BareMetal: &configv1.BareMetalPlatformStatus{
							APIServerInternalIPs: []string{"224.0.0.1"},
							IngressIPs:           []string{"224.0.0.2"},
							MachineNetworks:      []configv1.CIDR{},
						},
					},
				},
			},
		},
		{
			name: "should handle OpenStack platform: add additional pair of vips",
			givenInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						OpenStack: &configv1.OpenStackPlatformSpec{
							APIServerInternalIPs: []configv1.IP{"224.0.0.1", "2001:0DB8::1"},
							IngressIPs:           []configv1.IP{"224.0.0.2", "2001:0DB8::2"},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.OpenStackPlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "OpenStack",
						OpenStack: &configv1.OpenStackPlatformStatus{
							APIServerInternalIPs: []string{"224.0.0.1"},
							IngressIPs:           []string{"224.0.0.2"},
						},
					},
				},
			},
			wantedInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						OpenStack: &configv1.OpenStackPlatformSpec{
							APIServerInternalIPs: []configv1.IP{"224.0.0.1", "2001:0DB8::1"},
							IngressIPs:           []configv1.IP{"224.0.0.2", "2001:0DB8::2"},
							MachineNetworks:      []configv1.CIDR{},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.OpenStackPlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "OpenStack",
						OpenStack: &configv1.OpenStackPlatformStatus{
							APIServerInternalIPs: []string{"224.0.0.1", "2001:0DB8::1"},
							IngressIPs:           []string{"224.0.0.2", "2001:0DB8::2"},
							MachineNetworks:      []configv1.CIDR{},
						},
					},
				},
			},
		},
		{
			name: "should handle vSphere platform: add additional pair of vips",
			givenInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						VSphere: &configv1.VSpherePlatformSpec{
							APIServerInternalIPs: []configv1.IP{"224.0.0.1", "2001:0DB8::1"},
							IngressIPs:           []configv1.IP{"224.0.0.2", "2001:0DB8::2"},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.VSpherePlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "VSphere",
						VSphere: &configv1.VSpherePlatformStatus{
							APIServerInternalIPs: []string{"224.0.0.1"},
							IngressIPs:           []string{"224.0.0.2"},
						},
					},
				},
			},
			wantedInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						VSphere: &configv1.VSpherePlatformSpec{
							APIServerInternalIPs: []configv1.IP{"224.0.0.1", "2001:0DB8::1"},
							IngressIPs:           []configv1.IP{"224.0.0.2", "2001:0DB8::2"},
							MachineNetworks:      []configv1.CIDR{},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.VSpherePlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "VSphere",
						VSphere: &configv1.VSpherePlatformStatus{
							APIServerInternalIPs: []string{"224.0.0.1", "2001:0DB8::1"},
							IngressIPs:           []string{"224.0.0.2", "2001:0DB8::2"},
							MachineNetworks:      []configv1.CIDR{},
						},
					},
				},
			},
		},
		{
			name: "should handle missing baremetal platform spec: create and propagate from status",
			givenInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						BareMetal: nil,
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.BareMetalPlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "BareMetal",
						BareMetal: &configv1.BareMetalPlatformStatus{
							APIServerInternalIPs: []string{"224.0.0.1"},
							IngressIPs:           []string{"224.0.0.2"},
						},
					},
				},
			},
			wantedInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						BareMetal: &configv1.BareMetalPlatformSpec{
							APIServerInternalIPs: []configv1.IP{"224.0.0.1"},
							IngressIPs:           []configv1.IP{"224.0.0.2"},
							MachineNetworks:      []configv1.CIDR{},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.BareMetalPlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "BareMetal",
						BareMetal: &configv1.BareMetalPlatformStatus{
							APIServerInternalIPs: []string{"224.0.0.1"},
							IngressIPs:           []string{"224.0.0.2"},
							MachineNetworks:      []configv1.CIDR{},
						},
					},
				},
			},
		},
		{
			name: "should handle missing vsphere platform spec: create and propagate from status",
			givenInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						VSphere: nil,
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.VSpherePlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "VSphere",
						VSphere: &configv1.VSpherePlatformStatus{
							APIServerInternalIPs: []string{"224.0.0.1"},
							IngressIPs:           []string{"224.0.0.2"},
						},
					},
				},
			},
			wantedInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						VSphere: &configv1.VSpherePlatformSpec{
							APIServerInternalIPs: []configv1.IP{"224.0.0.1"},
							IngressIPs:           []configv1.IP{"224.0.0.2"},
							MachineNetworks:      []configv1.CIDR{},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.VSpherePlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "VSphere",
						VSphere: &configv1.VSpherePlatformStatus{
							APIServerInternalIPs: []string{"224.0.0.1"},
							IngressIPs:           []string{"224.0.0.2"},
							MachineNetworks:      []configv1.CIDR{},
						},
					},
				},
			},
		},
		{
			name: "should handle missing openstack platform spec: create and propagate from status",
			givenInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						OpenStack: nil,
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.OpenStackPlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "OpenStack",
						OpenStack: &configv1.OpenStackPlatformStatus{
							APIServerInternalIPs: []string{"224.0.0.1"},
							IngressIPs:           []string{"224.0.0.2"},
						},
					},
				},
			},
			wantedInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						OpenStack: &configv1.OpenStackPlatformSpec{
							APIServerInternalIPs: []configv1.IP{"224.0.0.1"},
							IngressIPs:           []configv1.IP{"224.0.0.2"},
							MachineNetworks:      []configv1.CIDR{},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.OpenStackPlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "OpenStack",
						OpenStack: &configv1.OpenStackPlatformStatus{
							APIServerInternalIPs: []string{"224.0.0.1"},
							IngressIPs:           []string{"224.0.0.2"},
							MachineNetworks:      []configv1.CIDR{},
						},
					},
				},
			},
		},
		{
			name: "should handle missing platform status: unreachable path but should not crash",
			givenInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{},
				},
				Status: configv1.InfrastructureStatus{
					PlatformStatus: nil,
				},
			},
			wantedInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{},
				},
				Status: configv1.InfrastructureStatus{
					PlatformStatus: nil,
				},
			},
		},
		{
			name: "should handle missing baremetal platform status: unreachable path but should not crash",
			givenInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						BareMetal: &configv1.BareMetalPlatformSpec{
							APIServerInternalIPs: []configv1.IP{"224.0.0.1"},
							IngressIPs:           []configv1.IP{"224.0.0.2"},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.BareMetalPlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type:      "BareMetal",
						BareMetal: nil,
					},
				},
			},
			wantedInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						BareMetal: &configv1.BareMetalPlatformSpec{
							APIServerInternalIPs: []configv1.IP{"224.0.0.1"},
							IngressIPs:           []configv1.IP{"224.0.0.2"},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.BareMetalPlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type:      "BareMetal",
						BareMetal: nil,
					},
				},
			},
		},
		{
			name: "should handle missing openstack platform status: unreachable path but should not crash",
			givenInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						OpenStack: &configv1.OpenStackPlatformSpec{
							APIServerInternalIPs: []configv1.IP{"224.0.0.1"},
							IngressIPs:           []configv1.IP{"224.0.0.2"},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.OpenStackPlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type:      "OpenStack",
						OpenStack: nil,
					},
				},
			},
			wantedInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						OpenStack: &configv1.OpenStackPlatformSpec{
							APIServerInternalIPs: []configv1.IP{"224.0.0.1"},
							IngressIPs:           []configv1.IP{"224.0.0.2"},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.OpenStackPlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type:      "OpenStack",
						OpenStack: nil,
					},
				},
			},
		},
		{
			name: "should handle vSphere UPI: platform status is never populated",
			givenInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						VSphere: &configv1.VSpherePlatformSpec{
							APIServerInternalIPs: []configv1.IP{"224.0.0.1"},
							IngressIPs:           []configv1.IP{"224.0.0.2"},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.VSpherePlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type:    "VSphere",
						VSphere: nil,
					},
				},
			},
			wantedInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						VSphere: &configv1.VSpherePlatformSpec{
							APIServerInternalIPs: []configv1.IP{"224.0.0.1"},
							IngressIPs:           []configv1.IP{"224.0.0.2"},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.VSpherePlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type:    "VSphere",
						VSphere: nil,
					},
				},
			},
		},
		{
			name: "should handle vSphere UPI: empty spec and status should not fail",
			givenInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						VSphere: &configv1.VSpherePlatformSpec{},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.VSpherePlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type:    "VSphere",
						VSphere: nil,
					},
				},
			},
			wantedInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						VSphere: &configv1.VSpherePlatformSpec{},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.VSpherePlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type:    "VSphere",
						VSphere: nil,
					},
				},
			},
		},
		{
			name: "should handle vSphere UPI: nil spec and status should not fail",
			givenInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						VSphere: nil,
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.VSpherePlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type:    "VSphere",
						VSphere: nil,
					},
				},
			},
			wantedInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						VSphere: nil,
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.VSpherePlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type:    "VSphere",
						VSphere: nil,
					},
				},
			},
		},
		{
			name: "should handle vSphere UPI: empty unset apiServerInternalIPs and ingressIPs",
			givenInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						VSphere: &configv1.VSpherePlatformSpec{
							APIServerInternalIPs: []configv1.IP{},
							IngressIPs:           []configv1.IP{},
							MachineNetworks:      []configv1.CIDR{"224.0.0.1/24", "224.0.1.1/24"},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.VSpherePlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "VSphere",
						VSphere: &configv1.VSpherePlatformStatus{
							MachineNetworks: []configv1.CIDR{"224.0.0.1/24", "224.0.1.1/24"},
						},
					},
				},
			},
			wantedInfra: configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.InfrastructureSpec{
					PlatformSpec: configv1.PlatformSpec{
						VSphere: &configv1.VSpherePlatformSpec{
							APIServerInternalIPs: []configv1.IP{},
							IngressIPs:           []configv1.IP{},
							MachineNetworks:      []configv1.CIDR{"224.0.0.1/24", "224.0.1.1/24"},
						},
					},
				},
				Status: configv1.InfrastructureStatus{
					Platform: configv1.VSpherePlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: "VSphere",
						VSphere: &configv1.VSpherePlatformStatus{
							APIServerInternalIPs: []string{},
							IngressIPs:           []string{},
							MachineNetworks:      []configv1.CIDR{"224.0.0.1/24", "224.0.1.1/24"},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &synchronizer{}
			gotInfra, err := a.SpecStatusSynchronize(&tt.givenInfra)

			if tt.wantedErr == "" {
				assert.Equal(t, err, nil)
				assert.EqualValues(t, &tt.wantedInfra, gotInfra, "should update infra correctly")
			} else {
				assert.Contains(t, err.Error(), tt.wantedErr)
			}
		})
	}
}
