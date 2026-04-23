package proxyconfig

import (
	"context"
	"strings"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/client/fake"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/cluster-network-operator/pkg/names"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"

	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func init() {
	configv1.AddToScheme(scheme.Scheme)
}

// TestReconcileUpdatesProxyStatusOnNetworkChange verifies that reconciliation correctly
// updates Proxy.Status.NoProxy when Network.Status.ClusterNetwork changes.
// Note: This does NOT verify that Network changes trigger reconciliation (validated on live clusters).
func TestReconcileUpdatesProxyStatusOnNetworkChange(t *testing.T) {
	initialCIDR := "10.128.0.0/14"
	expandedCIDR := "10.128.0.0/13"

	proxy := &configv1.Proxy{
		ObjectMeta: metav1.ObjectMeta{Name: names.PROXY_CONFIG},
		Spec: configv1.ProxySpec{
			HTTPProxy:  "http://proxy.example.com:3128",
			HTTPSProxy: "http://proxy.example.com:3128",
		},
	}

	network := &configv1.Network{
		ObjectMeta: metav1.ObjectMeta{Name: names.CLUSTER_CONFIG},
		Status: configv1.NetworkStatus{
			ClusterNetwork: []configv1.ClusterNetworkEntry{
				{CIDR: initialCIDR},
			},
			ServiceNetwork: []string{"172.30.0.0/16"},
		},
	}

	infra := &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{Name: names.CLUSTER_CONFIG},
		Status: configv1.InfrastructureStatus{
			APIServerInternalURL: "https://api-int.example.com:6443",
			PlatformStatus: &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
				AWS: &configv1.AWSPlatformStatus{
					Region: "us-east-1",
				},
			},
		},
	}

	clusterConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-config-v1",
			Namespace: "kube-system",
		},
		Data: map[string]string{
			"install-config": `
controlPlane:
  replicas: "3"
networking:
  machineCIDR: 10.0.0.0/16
`,
		},
	}

	fakeClient := fake.NewFakeClient(proxy, network, infra, clusterConfigMap)
	statusMgr := statusmanager.New(fakeClient, "network", names.StandAloneClusterName)

	r := &ReconcileProxyConfig{
		client: fakeClient.Default().CRClient(),
		status: statusMgr,
	}

	// Initial reconcile with /14 CIDR
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: names.PROXY_CONFIG}}
	_, err := r.Reconcile(context.TODO(), req)
	if err != nil {
		t.Fatalf("Initial Reconcile failed: %v", err)
	}

	// Verify initial proxy status includes /14 CIDR
	if err := fakeClient.Default().CRClient().Get(context.TODO(), types.NamespacedName{Name: names.PROXY_CONFIG}, proxy); err != nil {
		t.Fatalf("Failed to get proxy: %v", err)
	}
	if !strings.Contains(proxy.Status.NoProxy, initialCIDR) {
		t.Errorf("Expected proxy.Status.NoProxy to contain %s, got: %s", initialCIDR, proxy.Status.NoProxy)
	}

	// Simulate Network CIDR expansion (e.g., admin runs: oc patch network cluster ...)
	network.Status.ClusterNetwork = []configv1.ClusterNetworkEntry{
		{CIDR: expandedCIDR},
	}
	if err := fakeClient.Default().CRClient().Update(context.TODO(), network); err != nil {
		t.Fatalf("Failed to update network: %v", err)
	}

	// Reconcile again - in production, the Network watch would trigger this automatically
	_, err = r.Reconcile(context.TODO(), req)
	if err != nil {
		t.Fatalf("Reconcile after Network change failed: %v", err)
	}

	// Verify proxy status now includes the expanded CIDR
	if err := fakeClient.Default().CRClient().Get(context.TODO(), types.NamespacedName{Name: names.PROXY_CONFIG}, proxy); err != nil {
		t.Fatalf("Failed to get proxy after update: %v", err)
	}

	if !strings.Contains(proxy.Status.NoProxy, expandedCIDR) {
		t.Errorf("Expected proxy.Status.NoProxy to contain expanded CIDR %s, got: %s",
			expandedCIDR, proxy.Status.NoProxy)
	}

	if strings.Contains(proxy.Status.NoProxy, initialCIDR) {
		t.Errorf("proxy.Status.NoProxy still contains old CIDR %s, got: %s",
			initialCIDR, proxy.Status.NoProxy)
	}
}

// TestReconcileUpdatesProxyStatusOnInfrastructureChange verifies that reconciliation correctly
// updates Proxy.Status.NoProxy when Infrastructure.Status changes.
// Note: This does NOT verify that Infrastructure changes trigger reconciliation (validated on live clusters).
func TestReconcileUpdatesProxyStatusOnInfrastructureChange(t *testing.T) {
	initialAPIServer := "api-int.initial.example.com"
	updatedAPIServer := "api-int.updated.example.com"

	proxy := &configv1.Proxy{
		ObjectMeta: metav1.ObjectMeta{Name: names.PROXY_CONFIG},
		Spec: configv1.ProxySpec{
			HTTPProxy:  "http://proxy.example.com:3128",
			HTTPSProxy: "http://proxy.example.com:3128",
		},
	}

	network := &configv1.Network{
		ObjectMeta: metav1.ObjectMeta{Name: names.CLUSTER_CONFIG},
		Status: configv1.NetworkStatus{
			ClusterNetwork: []configv1.ClusterNetworkEntry{
				{CIDR: "10.128.0.0/14"},
			},
			ServiceNetwork: []string{"172.30.0.0/16"},
		},
	}

	infra := &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{Name: names.CLUSTER_CONFIG},
		Status: configv1.InfrastructureStatus{
			APIServerInternalURL: "https://" + initialAPIServer + ":6443",
			PlatformStatus: &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
				AWS: &configv1.AWSPlatformStatus{
					Region: "us-east-1",
				},
			},
		},
	}

	clusterConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-config-v1",
			Namespace: "kube-system",
		},
		Data: map[string]string{
			"install-config": `
controlPlane:
  replicas: "3"
networking:
  machineCIDR: 10.0.0.0/16
`,
		},
	}

	fakeClient := fake.NewFakeClient(proxy, network, infra, clusterConfigMap)
	statusMgr := statusmanager.New(fakeClient, "network", names.StandAloneClusterName)

	r := &ReconcileProxyConfig{
		client: fakeClient.Default().CRClient(),
		status: statusMgr,
	}

	// Initial reconcile
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: names.PROXY_CONFIG}}
	_, err := r.Reconcile(context.TODO(), req)
	if err != nil {
		t.Fatalf("Initial Reconcile failed: %v", err)
	}

	// Verify initial proxy status includes initial API server hostname
	if err := fakeClient.Default().CRClient().Get(context.TODO(), types.NamespacedName{Name: names.PROXY_CONFIG}, proxy); err != nil {
		t.Fatalf("Failed to get proxy: %v", err)
	}
	if !strings.Contains(proxy.Status.NoProxy, initialAPIServer) {
		t.Errorf("Expected proxy.Status.NoProxy to contain %s, got: %s", initialAPIServer, proxy.Status.NoProxy)
	}

	// Simulate Infrastructure change (e.g., API server URL update during cluster migration)
	infra.Status.APIServerInternalURL = "https://" + updatedAPIServer + ":6443"
	if err := fakeClient.Default().CRClient().Update(context.TODO(), infra); err != nil {
		t.Fatalf("Failed to update infrastructure: %v", err)
	}

	// Reconcile again - in production, the Infrastructure watch would trigger this
	_, err = r.Reconcile(context.TODO(), req)
	if err != nil {
		t.Fatalf("Reconcile after Infrastructure change failed: %v", err)
	}

	// Verify proxy status now includes the updated API server hostname
	if err := fakeClient.Default().CRClient().Get(context.TODO(), types.NamespacedName{Name: names.PROXY_CONFIG}, proxy); err != nil {
		t.Fatalf("Failed to get proxy after update: %v", err)
	}

	if !strings.Contains(proxy.Status.NoProxy, updatedAPIServer) {
		t.Errorf("Expected proxy.Status.NoProxy to contain updated API server %s, got: %s",
			updatedAPIServer, proxy.Status.NoProxy)
	}
}

// TestReconcileWithNoProxy verifies reconciliation when no proxy is configured.
func TestReconcileWithNoProxy(t *testing.T) {
	proxy := &configv1.Proxy{
		ObjectMeta: metav1.ObjectMeta{Name: names.PROXY_CONFIG},
		// Spec is empty - no proxy configured
	}

	network := &configv1.Network{
		ObjectMeta: metav1.ObjectMeta{Name: names.CLUSTER_CONFIG},
		Status: configv1.NetworkStatus{
			ClusterNetwork: []configv1.ClusterNetworkEntry{
				{CIDR: "10.128.0.0/14"},
			},
			ServiceNetwork: []string{"172.30.0.0/16"},
		},
	}

	infra := &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{Name: names.CLUSTER_CONFIG},
		Status: configv1.InfrastructureStatus{
			APIServerInternalURL: "https://api-int.example.com:6443",
			PlatformStatus: &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
			},
		},
	}

	clusterConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-config-v1",
			Namespace: "kube-system",
		},
		Data: map[string]string{
			"install-config": `networking:
  machineCIDR: 10.0.0.0/16`,
		},
	}

	fakeClient := fake.NewFakeClient(proxy, network, infra, clusterConfigMap)
	statusMgr := statusmanager.New(fakeClient, "network", names.StandAloneClusterName)

	r := &ReconcileProxyConfig{
		client: fakeClient.Default().CRClient(),
		status: statusMgr,
	}

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: names.PROXY_CONFIG}}
	_, err := r.Reconcile(context.TODO(), req)
	if err != nil {
		t.Fatalf("Reconcile with no proxy failed: %v", err)
	}

	// Verify proxy status is still computed (for future proxy configuration)
	if err := fakeClient.Default().CRClient().Get(context.TODO(), types.NamespacedName{Name: names.PROXY_CONFIG}, proxy); err != nil {
		t.Fatalf("Failed to get proxy: %v", err)
	}

	// When no proxy is configured, status.NoProxy should be empty
	if proxy.Status.NoProxy != "" {
		t.Errorf("Expected empty proxy.Status.NoProxy when no proxy configured, got: %s", proxy.Status.NoProxy)
	}
}

// TestReconcileHandlesMissingResources verifies error handling for missing Network or Infrastructure resources.
func TestReconcileHandlesMissingResources(t *testing.T) {
	tests := []struct {
		name          string
		objects       []crclient.Object
		expectError   bool
		errorContains string
	}{
		{
			name: "missing network resource",
			objects: []crclient.Object{
				&configv1.Proxy{
					ObjectMeta: metav1.ObjectMeta{Name: names.PROXY_CONFIG},
					Spec: configv1.ProxySpec{
						HTTPProxy: "http://proxy.example.com:3128",
					},
				},
				&configv1.Infrastructure{
					ObjectMeta: metav1.ObjectMeta{Name: names.CLUSTER_CONFIG},
					Status: configv1.InfrastructureStatus{
						APIServerInternalURL: "https://api-int.example.com:6443",
					},
				},
			},
			expectError:   true,
			errorContains: "not found",
		},
		{
			name: "missing infrastructure resource",
			objects: []crclient.Object{
				&configv1.Proxy{
					ObjectMeta: metav1.ObjectMeta{Name: names.PROXY_CONFIG},
					Spec: configv1.ProxySpec{
						HTTPProxy: "http://proxy.example.com:3128",
					},
				},
				&configv1.Network{
					ObjectMeta: metav1.ObjectMeta{Name: names.CLUSTER_CONFIG},
					Status: configv1.NetworkStatus{
						ClusterNetwork: []configv1.ClusterNetworkEntry{
							{CIDR: "10.128.0.0/14"},
						},
					},
				},
			},
			expectError:   true,
			errorContains: "not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewFakeClient(tt.objects...)
			statusMgr := statusmanager.New(fakeClient, "network", names.StandAloneClusterName)

			r := &ReconcileProxyConfig{
				client: fakeClient.Default().CRClient(),
				status: statusMgr,
			}

			req := reconcile.Request{NamespacedName: types.NamespacedName{Name: names.PROXY_CONFIG}}
			_, err := r.Reconcile(context.TODO(), req)

			if tt.expectError && err == nil {
				t.Errorf("Expected error containing '%s', got nil", tt.errorContains)
			}
			if !tt.expectError && err != nil {
				t.Errorf("Expected no error, got: %v", err)
			}
			if tt.expectError && err != nil && !strings.Contains(err.Error(), tt.errorContains) {
				t.Errorf("Expected error containing '%s', got: %v", tt.errorContains, err)
			}
		})
	}
}
