package clusterconfig

import (
	"context"

	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/openshift/cluster-network-operator/pkg/client/fake"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/names"
)

var (
	hostMTU       uint32 = 1500
	sdnClusterMTU uint32 = 1450
	ovnClusterMTU uint32 = 1400
)

type test struct {
	name             string
	clusterConfig    *configv1.Network
	operConfig       *operv1.Network
	wantedOperConfig *operv1.Network
}

func generateTest(name string, clusterConfig *configv1.Network, operConfig *operv1.Network, wantedOperConfig *operv1.Network) *test {
	return &test{
		name:             name,
		clusterConfig:    clusterConfig,
		operConfig:       operConfig,
		wantedOperConfig: wantedOperConfig,
	}
}

func withCondition(conditions []metav1.Condition, cond *metav1.Condition) []metav1.Condition {
	meta.SetStatusCondition(&conditions, *cond)
	return conditions
}

func generateStatusConditions(inProgress, targetCNIAvailable, mtuReady, targetCNIInUse, originalCNIPurged metav1.ConditionStatus) []metav1.Condition {
	return []metav1.Condition{
		{
			Type:   names.NetworkTypeMigrationInProgress,
			Status: inProgress,
		},
		{
			Type:   names.NetworkTypeMigrationTargetCNIAvailable,
			Status: targetCNIAvailable,
		},
		{
			Type:   names.NetworkTypeMigrationMTUReady,
			Status: mtuReady,
		},
		{
			Type:   names.NetworkTypeMigrationTargetCNIInUse,
			Status: targetCNIInUse,
		},
		{
			Type:   names.NetworkTypeMigrationOriginalCNIPurged,
			Status: originalCNIPurged,
		},
	}
}

func TestPrepareOperatorConfigForNetworkTypeMigration(t *testing.T) {
	tests := []*test{
		generateTest(
			"SDN live migration is triggered, deploy the target plugin",
			&configv1.Network{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
					Annotations: map[string]string{
						names.NetworkTypeMigrationAnnotation: "",
					},
				},
				Spec: configv1.NetworkSpec{
					NetworkType: "OVNKubernetes",
				},
				Status: configv1.NetworkStatus{
					NetworkType: "OpenShiftSDN",
					Conditions:  generateStatusConditions(metav1.ConditionTrue, metav1.ConditionFalse, metav1.ConditionFalse, metav1.ConditionFalse, metav1.ConditionFalse),
				},
			},
			&operv1.Network{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
				},
				Spec: operv1.NetworkSpec{
					DefaultNetwork: operv1.DefaultNetworkDefinition{
						Type: "OpenShiftSDN",
					},
				},
			},
			&operv1.Network{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
				},
				Spec: operv1.NetworkSpec{
					DefaultNetwork: operv1.DefaultNetworkDefinition{
						Type: "OpenShiftSDN",
					},
					Migration: &operv1.NetworkMigration{
						NetworkType: "OVNKubernetes",
						Mode:        operv1.LiveNetworkMigrationMode,
					},
				},
			},
		),

		generateTest(
			"The target CNI is deployed, apply the routable MTU in migration",
			&configv1.Network{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
					Annotations: map[string]string{
						names.NetworkTypeMigrationAnnotation: "",
					},
				},
				Spec: configv1.NetworkSpec{
					NetworkType: "OVNKubernetes",
				},
				Status: configv1.NetworkStatus{
					NetworkType:       "OpenShiftSDN",
					ClusterNetworkMTU: 1450,
					Conditions:        generateStatusConditions(metav1.ConditionTrue, metav1.ConditionTrue, metav1.ConditionFalse, metav1.ConditionFalse, metav1.ConditionFalse),
				},
			},
			&operv1.Network{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
				},
				Spec: operv1.NetworkSpec{
					DefaultNetwork: operv1.DefaultNetworkDefinition{
						Type: "OpenShiftSDN",
					},
				},
			},
			&operv1.Network{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
				},
				Spec: operv1.NetworkSpec{
					DefaultNetwork: operv1.DefaultNetworkDefinition{
						Type: "OpenShiftSDN",
					},
					Migration: &operv1.NetworkMigration{
						NetworkType: "OVNKubernetes",
						Mode:        operv1.LiveNetworkMigrationMode,
						MTU: &operv1.MTUMigration{
							Network: &operv1.MTUMigrationValues{
								From: &sdnClusterMTU,
								To:   &ovnClusterMTU,
							},
							Machine: &operv1.MTUMigrationValues{
								To: &hostMTU,
							},
						},
					},
				},
			},
		),

		generateTest(
			"The target CNI is deployed, apply the routable MTU and trigger CNI swapping in rollback",
			&configv1.Network{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
					Annotations: map[string]string{
						names.NetworkTypeMigrationAnnotation: "",
					},
				},
				Spec: configv1.NetworkSpec{
					NetworkType: "OpenShiftSDN",
				},
				Status: configv1.NetworkStatus{
					NetworkType:       "OVNKubernetes",
					ClusterNetworkMTU: 1400,
					Conditions:        generateStatusConditions(metav1.ConditionTrue, metav1.ConditionTrue, metav1.ConditionFalse, metav1.ConditionFalse, metav1.ConditionFalse),
				},
			},
			&operv1.Network{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
				},
				Spec: operv1.NetworkSpec{
					DefaultNetwork: operv1.DefaultNetworkDefinition{
						Type: "OpenShiftSDN",
					},
				},
			},
			&operv1.Network{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
				},
				Spec: operv1.NetworkSpec{
					DefaultNetwork: operv1.DefaultNetworkDefinition{
						Type: "OpenShiftSDN",
					},
					Migration: &operv1.NetworkMigration{
						NetworkType: "OpenShiftSDN",
						Mode:        operv1.LiveNetworkMigrationMode,
						MTU: &operv1.MTUMigration{
							Network: &operv1.MTUMigrationValues{
								From: &ovnClusterMTU,
								To:   &ovnClusterMTU,
							},
							Machine: &operv1.MTUMigrationValues{
								To: &hostMTU,
							},
						},
					},
				},
			},
		),

		generateTest(
			"The routable MTU is applied, trigger CNI swapping in migration",
			&configv1.Network{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
					Annotations: map[string]string{
						names.NetworkTypeMigrationAnnotation: "",
					},
				},
				Spec: configv1.NetworkSpec{
					NetworkType: "OVNKubernetes",
				},
				Status: configv1.NetworkStatus{
					NetworkType:       "OpenShiftSDN",
					ClusterNetworkMTU: 1450,
					Conditions:        generateStatusConditions(metav1.ConditionTrue, metav1.ConditionTrue, metav1.ConditionTrue, metav1.ConditionFalse, metav1.ConditionFalse),
				},
			},
			&operv1.Network{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
				},
				Spec: operv1.NetworkSpec{
					DefaultNetwork: operv1.DefaultNetworkDefinition{
						Type: "OVNKubernetes",
					},
					Migration: &operv1.NetworkMigration{
						NetworkType: "OVNKubernetes",
						Mode:        operv1.LiveNetworkMigrationMode,
						MTU: &operv1.MTUMigration{
							Network: &operv1.MTUMigrationValues{
								From: &sdnClusterMTU,
								To:   &ovnClusterMTU,
							},
							Machine: &operv1.MTUMigrationValues{
								To: &hostMTU,
							},
						},
					},
				},
			},
			&operv1.Network{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
				},
				Spec: operv1.NetworkSpec{
					DefaultNetwork: operv1.DefaultNetworkDefinition{
						Type: "OVNKubernetes",
					},
					Migration: &operv1.NetworkMigration{
						NetworkType: "OVNKubernetes",
						Mode:        operv1.LiveNetworkMigrationMode,
					},
				},
			},
		),

		generateTest(
			"The target CNI is in use, remove routable MTU",
			&configv1.Network{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
					Annotations: map[string]string{
						names.NetworkTypeMigrationAnnotation: "",
					},
				},
				Spec: configv1.NetworkSpec{
					NetworkType: "OpenShiftSDN",
				},
				Status: configv1.NetworkStatus{
					NetworkType:       "OVNKubernetes",
					ClusterNetworkMTU: 1450,
					Conditions:        generateStatusConditions(metav1.ConditionTrue, metav1.ConditionTrue, metav1.ConditionTrue, metav1.ConditionFalse, metav1.ConditionFalse),
				},
			},
			&operv1.Network{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
				},
				Spec: operv1.NetworkSpec{
					DefaultNetwork: operv1.DefaultNetworkDefinition{
						Type: "OpenShiftSDN",
					},
					Migration: &operv1.NetworkMigration{
						NetworkType: "OpenShiftSDN",
						Mode:        operv1.LiveNetworkMigrationMode,
					},
				},
			},
			&operv1.Network{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
				},
				Spec: operv1.NetworkSpec{
					DefaultNetwork: operv1.DefaultNetworkDefinition{
						Type: "OpenShiftSDN",
					},
					Migration: &operv1.NetworkMigration{
						NetworkType: "OpenShiftSDN",
						Mode:        operv1.LiveNetworkMigrationMode,
					},
				},
			},
		),

		generateTest(
			"The target CNI is in use, purge the original CNI",
			&configv1.Network{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
					Annotations: map[string]string{
						names.NetworkTypeMigrationAnnotation: "",
					},
				},
				Spec: configv1.NetworkSpec{
					NetworkType: "OVNKubernetes",
				},
				Status: configv1.NetworkStatus{
					NetworkType:       "OpenShiftSDN",
					ClusterNetworkMTU: 1450,
					Conditions:        generateStatusConditions(metav1.ConditionTrue, metav1.ConditionTrue, metav1.ConditionFalse, metav1.ConditionTrue, metav1.ConditionFalse),
				},
			},
			&operv1.Network{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
				},
				Spec: operv1.NetworkSpec{
					DefaultNetwork: operv1.DefaultNetworkDefinition{
						Type: "OVNKubernetes",
					},
					Migration: &operv1.NetworkMigration{
						NetworkType: "OVNKubernetes",
						Mode:        operv1.LiveNetworkMigrationMode,
					},
				},
			},
			&operv1.Network{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
				},
				Spec: operv1.NetworkSpec{
					DefaultNetwork: operv1.DefaultNetworkDefinition{
						Type: "OVNKubernetes",
					},
				},
			},
		),

		generateTest(
			"Not update the operator config if the MCPs are updating",
			&configv1.Network{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
					Annotations: map[string]string{
						names.NetworkTypeMigrationAnnotation: "",
					},
				},
				Spec: configv1.NetworkSpec{
					NetworkType: "OpenShiftSDN",
				},
				Status: configv1.NetworkStatus{
					NetworkType:       "OVNKubernetes",
					ClusterNetworkMTU: 1450,
					Conditions: withCondition(generateStatusConditions(metav1.ConditionTrue, metav1.ConditionFalse, metav1.ConditionFalse, metav1.ConditionFalse, metav1.ConditionFalse), &metav1.Condition{
						Type:   names.NetworkTypeMigrationMTUReady,
						Status: "False",
						Reason: names.MachineConfigPoolsUpdating,
					}),
				},
			},
			&operv1.Network{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
				},
				Spec: operv1.NetworkSpec{
					DefaultNetwork: operv1.DefaultNetworkDefinition{
						Type: "OpenShiftSDN",
					},
					Migration: &operv1.NetworkMigration{
						MTU: &operv1.MTUMigration{
							Network: &operv1.MTUMigrationValues{
								From: &sdnClusterMTU,
								To:   &ovnClusterMTU,
							},
							Machine: &operv1.MTUMigrationValues{
								To: &hostMTU,
							},
						},
					},
				},
			},
			&operv1.Network{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
				},
				Spec: operv1.NetworkSpec{
					DefaultNetwork: operv1.DefaultNetworkDefinition{
						Type: "OpenShiftSDN",
					},
					Migration: &operv1.NetworkMigration{
						MTU: &operv1.MTUMigration{
							Network: &operv1.MTUMigrationValues{
								From: &sdnClusterMTU,
								To:   &ovnClusterMTU,
							},
							Machine: &operv1.MTUMigrationValues{
								To: &hostMTU,
							},
						},
					},
				},
			},
		),
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.TODO()
			r := &ReconcileClusterConfig{
				client: fake.NewFakeClient(),
			}

			mtuCm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mtu",
					Namespace: "openshift-network-operator",
				},
				Data: map[string]string{
					"mtu": "1500",
				},
			}
			if err := r.client.ClientFor("").CRClient().Create(ctx, mtuCm); err != nil {
				t.Fatal(err)
			}

			err := r.prepareOperatorConfigForNetworkTypeMigration(ctx, tt.clusterConfig, tt.operConfig)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			assert.EqualValues(t, tt.wantedOperConfig.Spec, tt.operConfig.Spec, "should be equal")
		})
	}
}
