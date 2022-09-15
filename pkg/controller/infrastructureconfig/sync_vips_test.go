package infrastructureconfig

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func Test_apiAndIngressVipsSynchronizer_VipsSynchronize(t *testing.T) {
	tests := []struct {
		name        string
		givenStatus configv1.InfrastructureStatus
		wantStatus  configv1.InfrastructureStatus
	}{
		{
			name: "`new` field is empty, `old` with value: should set `new[0]` to value from `old`",
			givenStatus: configv1.InfrastructureStatus{
				Platform: configv1.BareMetalPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					BareMetal: &configv1.BareMetalPlatformStatus{
						APIServerInternalIP: "fooA",
						IngressIP:           "fooI",
					},
				},
			},
			wantStatus: configv1.InfrastructureStatus{
				Platform: configv1.BareMetalPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					BareMetal: &configv1.BareMetalPlatformStatus{
						APIServerInternalIP:  "fooA",
						APIServerInternalIPs: []string{"fooA"},
						IngressIP:            "fooI",
						IngressIPs:           []string{"fooI"},
					},
				},
			},
		},
		{
			name: "`new` contains values, `old` is empty: should set `old` to value from `new[0]`",
			givenStatus: configv1.InfrastructureStatus{
				Platform: configv1.BareMetalPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					BareMetal: &configv1.BareMetalPlatformStatus{
						APIServerInternalIPs: []string{"fooA", "barA"},
						IngressIPs:           []string{"fooI", "barI"},
					},
				},
			},
			wantStatus: configv1.InfrastructureStatus{
				Platform: configv1.BareMetalPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					BareMetal: &configv1.BareMetalPlatformStatus{
						APIServerInternalIP:  "fooA",
						APIServerInternalIPs: []string{"fooA", "barA"},
						IngressIP:            "fooI",
						IngressIPs:           []string{"fooI", "barI"},
					},
				},
			},
		},
		{
			name: "`new` contains values, `old` contains `new[0]`: should not update anything",
			givenStatus: configv1.InfrastructureStatus{
				Platform: configv1.BareMetalPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					BareMetal: &configv1.BareMetalPlatformStatus{
						APIServerInternalIP:  "fooA",
						APIServerInternalIPs: []string{"fooA", "barA"},
						IngressIP:            "fooI",
						IngressIPs:           []string{"fooI", "barI"},
					},
				},
			},
			wantStatus: configv1.InfrastructureStatus{
				Platform: configv1.BareMetalPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					BareMetal: &configv1.BareMetalPlatformStatus{
						APIServerInternalIP:  "fooA",
						APIServerInternalIPs: []string{"fooA", "barA"},
						IngressIP:            "fooI",
						IngressIPs:           []string{"fooI", "barI"},
					},
				},
			},
		},
		{
			name: "`new` contains values, `old` contains `new[1]`: as `new[0]` contains the clusters primary IP family, new values take precedence over old values, so set `old` to value from `new[0]` |",
			givenStatus: configv1.InfrastructureStatus{
				Platform: configv1.BareMetalPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					BareMetal: &configv1.BareMetalPlatformStatus{
						APIServerInternalIP:  "barA",
						APIServerInternalIPs: []string{"fooA", "barA"},
						IngressIP:            "barI",
						IngressIPs:           []string{"fooI", "barI"},
					},
				},
			},
			wantStatus: configv1.InfrastructureStatus{
				Platform: configv1.BareMetalPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					BareMetal: &configv1.BareMetalPlatformStatus{
						APIServerInternalIP:  "fooA",
						APIServerInternalIPs: []string{"fooA", "barA"},
						IngressIP:            "fooI",
						IngressIPs:           []string{"fooI", "barI"},
					},
				},
			},
		},
		{
			name: "`new` contains values, `old` contains a value which is not included in `new`: should set `old` to value from `new[0]` (new values take precedence over old values)",
			givenStatus: configv1.InfrastructureStatus{
				Platform: configv1.BareMetalPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					BareMetal: &configv1.BareMetalPlatformStatus{
						APIServerInternalIP:  "bazA",
						APIServerInternalIPs: []string{"fooA", "barA"},
						IngressIP:            "bazI",
						IngressIPs:           []string{"fooI", "barI"},
					},
				},
			},
			wantStatus: configv1.InfrastructureStatus{
				Platform: configv1.BareMetalPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					BareMetal: &configv1.BareMetalPlatformStatus{
						APIServerInternalIP:  "fooA",
						APIServerInternalIPs: []string{"fooA", "barA"},
						IngressIP:            "fooI",
						IngressIPs:           []string{"fooI", "barI"},
					},
				},
			},
		},
		{
			name: "should work with only one VIP",
			givenStatus: configv1.InfrastructureStatus{
				Platform: configv1.BareMetalPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					BareMetal: &configv1.BareMetalPlatformStatus{
						APIServerInternalIP:  "",
						APIServerInternalIPs: []string{"fooA"},
						IngressIP:            "bazI",
						IngressIPs:           []string{"fooI"},
					},
				},
			},
			wantStatus: configv1.InfrastructureStatus{
				Platform: configv1.BareMetalPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					BareMetal: &configv1.BareMetalPlatformStatus{
						APIServerInternalIP:  "fooA",
						APIServerInternalIPs: []string{"fooA"},
						IngressIP:            "fooI",
						IngressIPs:           []string{"fooI"},
					},
				},
			},
		},
		{
			name: "should handle OpenStack platform",
			givenStatus: configv1.InfrastructureStatus{
				Platform: configv1.OpenStackPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					OpenStack: &configv1.OpenStackPlatformStatus{
						APIServerInternalIP: "fooA",
						IngressIP:           "fooI",
					},
				},
			},
			wantStatus: configv1.InfrastructureStatus{
				Platform: configv1.OpenStackPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					OpenStack: &configv1.OpenStackPlatformStatus{
						APIServerInternalIP:  "fooA",
						APIServerInternalIPs: []string{"fooA"},
						IngressIP:            "fooI",
						IngressIPs:           []string{"fooI"},
					},
				},
			},
		},
		{
			name: "should handle vSphere platform",
			givenStatus: configv1.InfrastructureStatus{
				Platform: configv1.VSpherePlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					VSphere: &configv1.VSpherePlatformStatus{
						APIServerInternalIP: "fooA",
						IngressIP:           "fooI",
					},
				},
			},
			wantStatus: configv1.InfrastructureStatus{
				Platform: configv1.VSpherePlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					VSphere: &configv1.VSpherePlatformStatus{
						APIServerInternalIP:  "fooA",
						APIServerInternalIPs: []string{"fooA"},
						IngressIP:            "fooI",
						IngressIPs:           []string{"fooI"},
					},
				},
			},
		},
		{
			name: "should handle oVirt platform",
			givenStatus: configv1.InfrastructureStatus{
				Platform: configv1.OvirtPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					Ovirt: &configv1.OvirtPlatformStatus{
						APIServerInternalIP: "fooA",
						IngressIP:           "fooI",
					},
				},
			},
			wantStatus: configv1.InfrastructureStatus{
				Platform: configv1.OvirtPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					Ovirt: &configv1.OvirtPlatformStatus{
						APIServerInternalIP:  "fooA",
						APIServerInternalIPs: []string{"fooA"},
						IngressIP:            "fooI",
						IngressIPs:           []string{"fooI"},
					},
				},
			},
		},
		{
			name: "should handle Nutanix platform",
			givenStatus: configv1.InfrastructureStatus{
				Platform: configv1.NutanixPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					Nutanix: &configv1.NutanixPlatformStatus{
						APIServerInternalIP: "fooA",
						IngressIP:           "fooI",
					},
				},
			},
			wantStatus: configv1.InfrastructureStatus{
				Platform: configv1.NutanixPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					Nutanix: &configv1.NutanixPlatformStatus{
						APIServerInternalIP:  "fooA",
						APIServerInternalIPs: []string{"fooA"},
						IngressIP:            "fooI",
						IngressIPs:           []string{"fooI"},
					},
				},
			},
		},
		{
			name: "should do nothing on non onprem platform",
			givenStatus: configv1.InfrastructureStatus{
				Platform: configv1.AWSPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					AWS: &configv1.AWSPlatformStatus{},
				},
			},
			wantStatus: configv1.InfrastructureStatus{
				Platform: configv1.AWSPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					AWS: &configv1.AWSPlatformStatus{},
				},
			},
		},
		{
			name: "should not panic on empty PlatformStatus.VSphere field (VSphere UPI doesn't populate VSphere field)",
			givenStatus: configv1.InfrastructureStatus{
				Platform: configv1.VSpherePlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					VSphere: nil,
				},
			},
			wantStatus: configv1.InfrastructureStatus{
				Platform:       configv1.VSpherePlatformType,
				PlatformStatus: &configv1.PlatformStatus{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			givenInfra := &configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Status:     tt.givenStatus,
			}

			a := &apiAndIngressVipsSynchronizer{}
			gotInfra := a.VipsSynchronize(givenInfra)

			assert.EqualValues(t, tt.wantStatus, gotInfra.Status, "should update status correctly")
		})
	}
}
