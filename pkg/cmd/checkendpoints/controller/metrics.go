package controller

import (
	"sync"

	"github.com/openshift/cluster-network-operator/pkg/cmd/checkendpoints/trace"
	"k8s.io/component-base/metrics"
	"k8s.io/component-base/metrics/legacyregistry"
)

var (
	registerMetrics sync.Once

	endpointCheckCounter   *metrics.CounterVec
	tcpConnectLatencyGauge *metrics.GaugeVec
	dnsResolveLatencyGauge *metrics.GaugeVec
)

// RegisterMetrics in the global registry
func RegisterMetrics() {
	registerMetrics.Do(func() {
		endpointCheckCounter = metrics.NewCounterVec(&metrics.CounterOpts{
			Name: "pod_network_connectivity_check_count",
			Help: "Report status of pod network connectivity checks over time.",
		}, []string{"component", "checkName", "targetEndpoint", "tcpConnect", "dnsResolve"})

		tcpConnectLatencyGauge = metrics.NewGaugeVec(&metrics.GaugeOpts{
			Name: "pod_network_connectivity_check_tcp_connect_latency_gauge",
			Help: "Report latency of TCP connect to target endpoint over time.",
		}, []string{"component", "checkName", "targetEndpoint"})

		dnsResolveLatencyGauge = metrics.NewGaugeVec(&metrics.GaugeOpts{
			Name: "pod_network_connectivity_check_dns_resolve_latency_gauge",
			Help: "Report latency of DNS resolve of target endpoint over time.",
		}, []string{"component", "checkName", "targetEndpoint"})
		legacyregistry.MustRegister(endpointCheckCounter)
		legacyregistry.MustRegister(tcpConnectLatencyGauge)
		legacyregistry.MustRegister(dnsResolveLatencyGauge)
	})
}

// MetricsContext updates connectivity check metrics
type MetricsContext interface {
	Update(targetEndpoint string, latency *trace.LatencyInfo, checkErr error)
}

type metricsContext struct {
	componentName string
	checkName     string
}

func NewMetricsContext(componentName, checkName string) *metricsContext {
	RegisterMetrics()
	return &metricsContext{
		componentName: componentName,
		checkName:     checkName,
	}

}

// Update the pod network connectivity check metrics for the given check results.
func (m *metricsContext) Update(targetEndpoint string, latency *trace.LatencyInfo, checkErr error) {
	endpointCheckCounter.With(m.getCounterMetricLabels(targetEndpoint, latency, checkErr)).Inc()
	if latency.Connect > 0 {
		tcpConnectLatencyGauge.With(m.getMetricLabels(targetEndpoint)).Set(float64(latency.Connect.Nanoseconds()))
	}
	if latency.DNS > 0 {
		dnsResolveLatencyGauge.With(m.getMetricLabels(targetEndpoint)).Set(float64(latency.DNS.Nanoseconds()))
	}
}

func (m *metricsContext) getCounterMetricLabels(targetEndpoint string, latency *trace.LatencyInfo, checkErr error) map[string]string {
	labels := m.getMetricLabels(targetEndpoint)
	labels["dnsResolve"] = ""
	labels["tcpConnect"] = ""
	if isDNSError(checkErr) {
		labels["dnsResolve"] = "failure"
		return labels
	}
	if latency.DNS != 0 {
		labels["dnsResolve"] = "success"
	}
	if checkErr != nil {
		labels["tcpConnect"] = "failure"
		return labels
	}
	labels["tcpConnect"] = "success"
	return labels
}

func (m *metricsContext) getMetricLabels(targetEndpoint string) map[string]string {
	return map[string]string{
		"component":      m.componentName,
		"checkName":      m.checkName,
		"targetEndpoint": targetEndpoint,
	}
}
