package connectivitycheck

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

// TestListAddressesForKubeAPIServerServiceEndpoints verifies endpoint selection from the shared kube-apiserver Endpoints object.
// It checks that sidecar ports are excluded and that a missing TCP 6443 listener is reported.
func TestListAddressesForKubeAPIServerServiceEndpoints(t *testing.T) {
	nodeName := "master-0"
	testCases := []struct {
		name    string
		ports   []corev1.EndpointPort
		want    []endpointInfo
		wantErr bool
	}{
		{
			name: "only returns kube-apiserver HTTPS port",
			ports: []corev1.EndpointPort{
				{Name: "insecure-readyz", Port: 6080, Protocol: corev1.ProtocolTCP},
				{Name: "https", Port: 6443, Protocol: corev1.ProtocolTCP},
				{Name: "check-endpoints", Port: 17697, Protocol: corev1.ProtocolTCP},
			},
			want: []endpointInfo{{hostName: "192.0.2.10", port: "6443", nodeName: nodeName}},
		},
		{
			name: "accepts kube-apiserver HTTPS port with unspecified protocol",
			ports: []corev1.EndpointPort{
				{Name: "https", Port: 6443},
			},
			want: []endpointInfo{{hostName: "192.0.2.10", port: "6443", nodeName: nodeName}},
		},
		{
			name: "rejects kube-apiserver HTTPS port with UDP protocol",
			ports: []corev1.EndpointPort{
				{Name: "https", Port: 6443, Protocol: corev1.ProtocolUDP},
			},
			wantErr: true,
		},
		{
			name: "errors if kube-apiserver HTTPS port is missing",
			ports: []corev1.EndpointPort{
				{Name: "insecure-readyz", Port: 6080, Protocol: corev1.ProtocolTCP},
				{Name: "check-endpoints", Port: 17697, Protocol: corev1.ProtocolTCP},
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			endpoints := &corev1.Endpoints{ //nolint:staticcheck // This controller still consumes core/v1 Endpoints.
				ObjectMeta: metav1.ObjectMeta{Namespace: "openshift-kube-apiserver", Name: "apiserver"},
				Subsets: []corev1.EndpointSubset{{ //nolint:staticcheck // This controller still consumes core/v1 Endpoints.
					Addresses: []corev1.EndpointAddress{{IP: "192.0.2.10", NodeName: &nodeName}},
					Ports:     tc.ports,
				}},
			}
			indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
			if err := indexer.Add(endpoints); err != nil {
				t.Fatalf("failed to add kube-apiserver endpoints to indexer: %v", err)
			}

			provider := connectivityCheckTemplateProvider{
				kubeAPIServerEndpointsLister: corev1listers.NewEndpointsLister(indexer),
			}
			got, err := provider.listAddressesForKubeAPIServerServiceEndpoints()
			if (err != nil) != tc.wantErr {
				t.Fatalf("listAddressesForKubeAPIServerServiceEndpoints() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("expected %d endpoints, got %d: %#v", len(tc.want), len(got), got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("expected endpoint %#v, got %#v", tc.want[i], got[i])
				}
			}
		})
	}
}

// TestNodeNameForLabel covers FQDN truncation and preserves valid IPs in label-safe form.
func TestNodeNameForLabel(t *testing.T) {
	testCases := []struct {
		name     string
		nodeName string
		expected string
	}{
		{
			name:     "FQDN returns only the segment before the first dot",
			nodeName: "node1.example.com",
			expected: "node1",
		},
		{
			name:     "simple name with no dots is returned unchanged",
			nodeName: "node999",
			expected: "node999",
		},
		{
			name:     "valid IPv6 address has colons replaced with dashes",
			nodeName: "fd00::1",
			expected: "fd00--1",
		},
		{
			name:     "valid IPv4 address has dots replaced with dashes",
			nodeName: "192.168.0.1",
			expected: "192-168-0-1",
		},
		{
			name:     "invalid IPv4 address (octet out of range) is treated as an FQDN",
			nodeName: "192.168.0.300",
			expected: "192",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			actual := nodeNameForLabel(tc.nodeName)
			if actual != tc.expected {
				t.Errorf("nodeNameForLabel(%q): expected %q, got %q", tc.nodeName, tc.expected, actual)
			}
		})
	}
}
