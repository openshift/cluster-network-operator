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
	"k8s.io/client-go/kubernetes/scheme"

	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func init() {
	if err := configv1.Install(scheme.Scheme); err != nil {
		panic(err)
	}
}

func TestEnqueueProxy(t *testing.T) {
	for _, object := range []crclient.Object{
		&configv1.Network{},
		&configv1.Infrastructure{},
	} {
		requests := enqueueProxy(context.Background(), object)
		if len(requests) != 1 {
			t.Fatalf("expected one reconcile request for %T, got %d", object, len(requests))
		}
		if requests[0].NamespacedName != names.Proxy() {
			t.Errorf("expected request %v for %T, got %v", names.Proxy(), object, requests[0].NamespacedName)
		}
	}
}

func TestReconcileUpdatesProxyStatusFromDependencies(t *testing.T) {
	const (
		initialCIDR      = "10.128.0.0/14"
		updatedCIDR      = "10.128.0.0/13"
		initialAPIServer = "api-int.initial.example.com"
		updatedAPIServer = "api-int.updated.example.com"
	)

	proxy := proxyWithSpec(configv1.ProxySpec{
		HTTPProxy:  "http://proxy.example.com:3128",
		HTTPSProxy: "http://proxy.example.com:3128",
	})
	network := networkWithClusterCIDR(initialCIDR)
	infrastructure := infrastructureWithAPIServer(initialAPIServer)
	client, reconciler := newProxyConfigReconciler(t, proxy, network, infrastructure)

	reconcileProxyConfig(t, reconciler)
	assertNoProxyContains(t, getProxyStatus(t, client).NoProxy, initialCIDR, initialAPIServer)

	network.Status.ClusterNetwork = []configv1.ClusterNetworkEntry{{CIDR: updatedCIDR}}
	if err := client.Update(context.Background(), network); err != nil {
		t.Fatalf("failed to update Network: %v", err)
	}
	reconcileMappedRequest(t, reconciler, network)

	noProxy := getProxyStatus(t, client).NoProxy
	assertNoProxyContains(t, noProxy, updatedCIDR)
	assertNoProxyExcludes(t, noProxy, initialCIDR)

	infrastructure.Status.APIServerInternalURL = "https://" + updatedAPIServer + ":6443"
	if err := client.Update(context.Background(), infrastructure); err != nil {
		t.Fatalf("failed to update Infrastructure: %v", err)
	}
	reconcileMappedRequest(t, reconciler, infrastructure)

	noProxy = getProxyStatus(t, client).NoProxy
	assertNoProxyContains(t, noProxy, updatedAPIServer)
	assertNoProxyExcludes(t, noProxy, initialAPIServer)
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

func reconcileMappedRequest(t *testing.T, reconciler *ReconcileProxyConfig, object crclient.Object) {
	t.Helper()

	requests := enqueueProxy(context.Background(), object)
	if len(requests) != 1 {
		t.Fatalf("expected one mapped reconcile request, got %d", len(requests))
	}
	reconcileRequest(t, reconciler, requests[0])
}

func reconcileProxyConfig(t *testing.T, reconciler *ReconcileProxyConfig) {
	t.Helper()
	reconcileRequest(t, reconciler, reconcile.Request{NamespacedName: names.Proxy()})
}

func reconcileRequest(t *testing.T, reconciler *ReconcileProxyConfig, request reconcile.Request) {
	t.Helper()
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
}

func getProxyStatus(t *testing.T, client crclient.Client) configv1.ProxyStatus {
	t.Helper()

	proxy := &configv1.Proxy{}
	if err := client.Get(context.Background(), names.Proxy(), proxy); err != nil {
		t.Fatalf("failed to get Proxy: %v", err)
	}
	return proxy.Status
}

func assertNoProxyContains(t *testing.T, noProxy string, values ...string) {
	t.Helper()
	for _, value := range values {
		if !strings.Contains(noProxy, value) {
			t.Errorf("expected Proxy status noProxy to contain %q, got %q", value, noProxy)
		}
	}
}

func assertNoProxyExcludes(t *testing.T, noProxy string, values ...string) {
	t.Helper()
	for _, value := range values {
		if strings.Contains(noProxy, value) {
			t.Errorf("expected Proxy status noProxy to exclude %q, got %q", value, noProxy)
		}
	}
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
