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
	if err := configv1.Install(scheme.Scheme); err != nil {
		panic(err)
	}
}

func TestReconcileUpdatesProxyStatusOnNetworkChange(t *testing.T) {
	initialCIDR := "10.128.0.0/14"
	expandedCIDR := "10.128.0.0/13"
	proxy := proxyWithSpec(configv1.ProxySpec{
		HTTPProxy:  "http://proxy.example.com:3128",
		HTTPSProxy: "http://proxy.example.com:3128",
	})
	network := networkWithClusterCIDR(initialCIDR)
	client, reconciler := newProxyConfigReconciler(t, proxy, network, infrastructureWithAPIServer("api-int.example.com"))

	reconcileProxyConfig(t, reconciler)
	proxyStatus := getProxyStatus(t, client)
	if !strings.Contains(proxyStatus.NoProxy, initialCIDR) {
		t.Fatalf("expected proxy.Status.NoProxy to contain %s, got: %s", initialCIDR, proxyStatus.NoProxy)
	}

	network.Status.ClusterNetwork = []configv1.ClusterNetworkEntry{{CIDR: expandedCIDR}}
	if err := client.Update(context.TODO(), network); err != nil {
		t.Fatalf("failed to update network: %v", err)
	}
	reconcileRequest(t, reconciler, reconcile.Request{NamespacedName: types.NamespacedName{Name: "network-event"}})

	proxyStatus = getProxyStatus(t, client)
	if !strings.Contains(proxyStatus.NoProxy, expandedCIDR) {
		t.Errorf("expected proxy.Status.NoProxy to contain expanded CIDR %s, got: %s", expandedCIDR, proxyStatus.NoProxy)
	}
	if strings.Contains(proxyStatus.NoProxy, initialCIDR) {
		t.Errorf("proxy.Status.NoProxy still contains old CIDR %s, got: %s", initialCIDR, proxyStatus.NoProxy)
	}
}

func TestReconcileUpdatesProxyStatusOnInfrastructureChange(t *testing.T) {
	initialAPIServer := "api-int.initial.example.com"
	updatedAPIServer := "api-int.updated.example.com"
	proxy := proxyWithSpec(configv1.ProxySpec{
		HTTPProxy:  "http://proxy.example.com:3128",
		HTTPSProxy: "http://proxy.example.com:3128",
	})
	infra := infrastructureWithAPIServer(initialAPIServer)
	client, reconciler := newProxyConfigReconciler(t, proxy, networkWithClusterCIDR("10.128.0.0/14"), infra)

	reconcileProxyConfig(t, reconciler)
	proxyStatus := getProxyStatus(t, client)
	if !strings.Contains(proxyStatus.NoProxy, initialAPIServer) {
		t.Fatalf("expected proxy.Status.NoProxy to contain %s, got: %s", initialAPIServer, proxyStatus.NoProxy)
	}

	infra.Status.APIServerInternalURL = "https://" + updatedAPIServer + ":6443"
	if err := client.Update(context.TODO(), infra); err != nil {
		t.Fatalf("failed to update infrastructure: %v", err)
	}
	reconcileRequest(t, reconciler, reconcile.Request{NamespacedName: types.NamespacedName{Name: "infrastructure-event"}})

	proxyStatus = getProxyStatus(t, client)
	if !strings.Contains(proxyStatus.NoProxy, updatedAPIServer) {
		t.Errorf("expected proxy.Status.NoProxy to contain updated API server %s, got: %s", updatedAPIServer, proxyStatus.NoProxy)
	}
	if strings.Contains(proxyStatus.NoProxy, initialAPIServer) {
		t.Errorf("proxy.Status.NoProxy still contains old API server %s, got: %s", initialAPIServer, proxyStatus.NoProxy)
	}
}

func TestReconcileWithNoProxy(t *testing.T) {
	client, reconciler := newProxyConfigReconciler(
		t,
		proxyWithSpec(configv1.ProxySpec{}),
		networkWithClusterCIDR("10.128.0.0/14"),
		infrastructureWithAPIServer("api-int.example.com"),
	)

	reconcileProxyConfig(t, reconciler)
	proxyStatus := getProxyStatus(t, client)
	if proxyStatus.NoProxy != "" {
		t.Errorf("expected empty proxy.Status.NoProxy when no proxy is configured, got: %s", proxyStatus.NoProxy)
	}
}

func TestReconcileHandlesMissingResources(t *testing.T) {
	tests := []struct {
		name          string
		objects       []crclient.Object
		errorContains string
	}{
		{
			name: "missing network resource",
			objects: []crclient.Object{
				proxyWithSpec(configv1.ProxySpec{HTTPProxy: "http://proxy.example.com:3128"}),
				infrastructureWithAPIServer("api-int.example.com"),
			},
			errorContains: "not found",
		},
		{
			name: "missing infrastructure resource",
			objects: []crclient.Object{
				proxyWithSpec(configv1.ProxySpec{HTTPProxy: "http://proxy.example.com:3128"}),
				networkWithClusterCIDR("10.128.0.0/14"),
			},
			errorContains: "not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewFakeClient(tt.objects...)
			reconciler := &ReconcileProxyConfig{
				client: fakeClient.Default().CRClient(),
				status: statusmanager.New(fakeClient, "network", names.StandAloneClusterName),
			}

			_, err := reconciler.Reconcile(context.TODO(), reconcile.Request{NamespacedName: names.Proxy()})
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.errorContains)
			}
			if !strings.Contains(err.Error(), tt.errorContains) {
				t.Fatalf("expected error containing %q, got: %v", tt.errorContains, err)
			}
		})
	}
}

func newProxyConfigReconciler(t *testing.T, objects ...crclient.Object) (crclient.Client, *ReconcileProxyConfig) {
	t.Helper()
	objects = append(objects, clusterConfigMap())
	fakeClient := fake.NewFakeClient(objects...)
	return fakeClient.Default().CRClient(), &ReconcileProxyConfig{
		client: fakeClient.Default().CRClient(),
		status: statusmanager.New(fakeClient, "network", names.StandAloneClusterName),
	}
}

func reconcileProxyConfig(t *testing.T, reconciler *ReconcileProxyConfig) {
	t.Helper()
	reconcileRequest(t, reconciler, reconcile.Request{NamespacedName: names.Proxy()})
}

func reconcileRequest(t *testing.T, reconciler *ReconcileProxyConfig, request reconcile.Request) {
	t.Helper()
	_, err := reconciler.Reconcile(context.TODO(), request)
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
}

func getProxyStatus(t *testing.T, client crclient.Client) configv1.ProxyStatus {
	t.Helper()
	proxy := &configv1.Proxy{}
	if err := client.Get(context.TODO(), names.Proxy(), proxy); err != nil {
		t.Fatalf("failed to get proxy: %v", err)
	}
	return proxy.Status
}

func proxyWithSpec(spec configv1.ProxySpec) *configv1.Proxy {
	return &configv1.Proxy{
		ObjectMeta: metav1.ObjectMeta{Name: names.PROXY_CONFIG},
		Spec:       spec,
	}
}

func networkWithClusterCIDR(cidr string) *configv1.Network {
	return &configv1.Network{
		ObjectMeta: metav1.ObjectMeta{Name: names.CLUSTER_CONFIG},
		Status: configv1.NetworkStatus{
			ClusterNetwork: []configv1.ClusterNetworkEntry{{CIDR: cidr}},
			ServiceNetwork: []string{"172.30.0.0/16"},
		},
	}
}

func infrastructureWithAPIServer(host string) *configv1.Infrastructure {
	return &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{Name: names.CLUSTER_CONFIG},
		Status: configv1.InfrastructureStatus{
			APIServerInternalURL: "https://" + host + ":6443",
			PlatformStatus: &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
				AWS: &configv1.AWSPlatformStatus{
					Region: "us-east-1",
				},
			},
		},
	}
}

func clusterConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
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
}
