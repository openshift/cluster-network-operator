package network_test

import (
	"context"
	"os"
	"reflect"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	fakeclient "github.com/openshift/cluster-network-operator/pkg/client/fake"
	"github.com/openshift/cluster-network-operator/pkg/hypershift"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/network"
	openshifttls "github.com/openshift/controller-runtime-common/pkg/tls"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func TestBootstrap(t *testing.T) {
	// Base setup - runs for all tests
	baseOperConfig := &operv1.Network{
		ObjectMeta: metav1.ObjectMeta{Name: names.OPERATOR_CONFIG},
		Spec: operv1.NetworkSpec{
			DefaultNetwork: operv1.DefaultNetworkDefinition{
				Type: operv1.NetworkTypeOVNKubernetes,
				OVNKubernetesConfig: &operv1.OVNKubernetesConfig{
					MTU: nil,
				},
			},
		},
	}

	baseClientObjs := []crclient.Object{
		&configv1.Infrastructure{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Status: configv1.InfrastructureStatus{
				PlatformStatus: &configv1.PlatformStatus{
					Type: configv1.NonePlatformType,
				},
			},
		},
		&configv1.Proxy{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      network.CLUSTER_CONFIG_NAME,
				Namespace: network.CLUSTER_CONFIG_NAMESPACE,
			},
			Data: map[string]string{
				"install-config": "controlPlane:\n  replicas: 3\n",
			},
		},
	}

	t.Run("in standalone (non-HyperShift) mode", func(t *testing.T) {
		clientObjs := append(baseClientObjs, &configv1.APIServer{
			ObjectMeta: metav1.ObjectMeta{Name: openshifttls.APIServerName},
			Spec: configv1.APIServerSpec{
				TLSSecurityProfile: &configv1.TLSSecurityProfile{
					Type: configv1.TLSProfileCustomType,
					Custom: &configv1.CustomTLSProfile{
						TLSProfileSpec: configv1.TLSProfileSpec{
							MinTLSVersion: configv1.VersionTLS13,
							Ciphers:       []string{"TLS_AES_128_GCM_SHA256"},
						},
					},
				},
				TLSAdherence: configv1.TLSAdherencePolicyLegacyAdheringComponentsOnly,
			},
		})

		t.Run("should set the TLS profile info from the APIServer CR", func(t *testing.T) {
			client := fakeclient.NewFakeClient(clientObjs...)
			result, err := network.Bootstrap(baseOperConfig, client)
			if err != nil {
				t.Fatalf("Bootstrap failed: %v", err)
			}
			if result == nil {
				t.Fatal("Bootstrap result is nil")
			}

			if result.TLSProfile.Spec.MinTLSVersion != configv1.VersionTLS13 {
				t.Errorf("Expected MinTLSVersion %v, got %v",
					configv1.VersionTLS13, result.TLSProfile.Spec.MinTLSVersion)
			}

			expectedCiphers := []string{"TLS_AES_128_GCM_SHA256"}
			if !reflect.DeepEqual(result.TLSProfile.Spec.Ciphers, expectedCiphers) {
				t.Errorf("Expected ciphers %v, got %v",
					expectedCiphers, result.TLSProfile.Spec.Ciphers)
			}

			if result.TLSProfile.Adherence != configv1.TLSAdherencePolicyLegacyAdheringComponentsOnly {
				t.Errorf("Expected adherence %v, got %v",
					configv1.TLSAdherencePolicyLegacyAdheringComponentsOnly,
					result.TLSProfile.Adherence)
			}
		})
	})

	t.Run("in HyperShift mode", func(t *testing.T) {
		const (
			hostedClusterName      = "test-hosted-cluster"
			hostedClusterNamespace = "test-namespace"
		)

		setupHyperShift := func(t *testing.T) {
			t.Setenv("HYPERSHIFT", "true")
			t.Setenv("HOSTED_CLUSTER_NAME", hostedClusterName)
			t.Setenv("HOSTED_CLUSTER_NAMESPACE", hostedClusterNamespace)

			t.Cleanup(func() {
				os.Unsetenv("HYPERSHIFT")
				os.Unsetenv("HOSTED_CLUSTER_NAME")
				os.Unsetenv("HOSTED_CLUSTER_NAMESPACE")
			})
		}

		t.Run("when the HostedControlPlane CR exists", func(t *testing.T) {
			setupHyperShift(t)

			t.Run("should set the TLS profile info from the APIServer spec", func(t *testing.T) {
				ctx := context.Background()
				client := fakeclient.NewFakeClient(baseClientObjs...)

				// Create HostedControlPlane with TLS configuration
				hcp := &uns.Unstructured{}
				hcp.SetGroupVersionKind(hypershift.HostedControlPlaneGVK)
				hcp.SetName(hostedClusterName)
				hcp.SetNamespace(hostedClusterNamespace)
				hcp.Object["spec"] = map[string]interface{}{
					"clusterID":                    "test-cluster-id",
					"controllerAvailabilityPolicy": "SingleReplica",
					"configuration": map[string]interface{}{
						"apiServer": map[string]interface{}{
							"tlsSecurityProfile": map[string]interface{}{
								"type": string(configv1.TLSProfileModernType),
							},
							"tlsAdherence": string(configv1.TLSAdherencePolicyStrictAllComponents),
						},
					},
				}

				if err := client.ClientFor(names.ManagementClusterName).CRClient().Create(ctx, hcp); err != nil {
					t.Fatalf("Failed to create HostedControlPlane: %v", err)
				}

				result, err := network.Bootstrap(baseOperConfig, client)
				if err != nil {
					t.Fatalf("Bootstrap failed: %v", err)
				}
				if result == nil {
					t.Fatal("Bootstrap result is nil")
				}

				if result.TLSProfile.Spec.MinTLSVersion != configv1.VersionTLS13 {
					t.Errorf("Expected MinTLSVersion %v, got %v",
						configv1.VersionTLS13, result.TLSProfile.Spec.MinTLSVersion)
				}

				if len(result.TLSProfile.Spec.Ciphers) == 0 {
					t.Error("Expected ciphers to not be empty")
				}

				if result.TLSProfile.Adherence != configv1.TLSAdherencePolicyStrictAllComponents {
					t.Errorf("Expected adherence %v, got %v",
						configv1.TLSAdherencePolicyStrictAllComponents,
						result.TLSProfile.Adherence)
				}
			})

			t.Run("and the APIServer spec doesn't exist", func(t *testing.T) {
				ctx := context.Background()
				client := fakeclient.NewFakeClient(baseClientObjs...)

				// Create HostedControlPlane without APIServer spec
				hcp := &uns.Unstructured{}
				hcp.SetGroupVersionKind(hypershift.HostedControlPlaneGVK)
				hcp.SetName(hostedClusterName)
				hcp.SetNamespace(hostedClusterNamespace)
				hcp.Object["spec"] = map[string]interface{}{
					"clusterID":                    "test-cluster-id",
					"controllerAvailabilityPolicy": "SingleReplica",
				}

				if err := client.ClientFor(names.ManagementClusterName).CRClient().Create(ctx, hcp); err != nil {
					t.Fatalf("Failed to create HostedControlPlane: %v", err)
				}

				result, err := network.Bootstrap(baseOperConfig, client)
				if err != nil {
					t.Fatalf("Bootstrap failed: %v", err)
				}
				if result == nil {
					t.Fatal("Bootstrap result is nil")
				}

				if result.TLSProfile.Spec.MinTLSVersion != configv1.VersionTLS12 {
					t.Errorf("Expected MinTLSVersion %v, got %v", configv1.VersionTLS12,
						result.TLSProfile.Spec.MinTLSVersion)
				}

				if len(result.TLSProfile.Spec.Ciphers) == 0 {
					t.Error("Expected ciphers to not be empty")
				}

				if result.TLSProfile.Adherence != "" {
					t.Errorf("Expected adherence to be empty, got %v", result.TLSProfile.Adherence)
				}
			})
		})
	})
}
