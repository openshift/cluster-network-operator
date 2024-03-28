package ipsec

import (
	"sync"

	operv1 "github.com/openshift/api/operator/v1"
	"k8s.io/component-base/metrics"
	"k8s.io/component-base/metrics/legacyregistry"
	"k8s.io/klog/v2"
)

var (
	registerMetrics sync.Once
	ipsecStateGauge *metrics.GaugeVec

	recordedMode, recordedFlavor string = "", ""
)

// RegisterIpsecMetrics in the global registry
func RegisterIpsecMetrics() {
	registerMetrics.Do(func() {
		ipsecStateGauge = metrics.NewGaugeVec(&metrics.GaugeOpts{
			Name: "cluster_network_ipsec_state",
			Help: "A metric with a constant '1' value labeled by the latest ipsecMode of the cluster",
		}, []string{"mode", "api_flavor"})
		legacyregistry.MustRegister(ipsecStateGauge)
	})
}

func UpdateIpsecTelemetry(ipsecConfig *operv1.IPsecConfig) {
	var flavor, mode string
	klog.V(5).Infof("IPsec Telemetry: %v", ipsecConfig)
	RegisterIpsecMetrics()
	if ipsecConfig == nil {
		mode = string(operv1.IPsecModeDisabled)
		flavor = "4.14"
	} else if ipsecConfig.Mode == "" {
		mode = string(operv1.IPsecModeFull)
		flavor = "4.14"
	} else {
		mode = string(ipsecConfig.Mode)
		flavor = "4.15"
	}
	if mode != recordedMode || flavor != recordedFlavor {
		klog.V(5).Infof("IPsec Telemetry: (%s, %s), writing to %v", mode, flavor, ipsecStateGauge)
		ipsecStateGauge.Reset()
		ipsecStateGauge.WithLabelValues(mode, flavor).Set(1)
		recordedMode = mode
		recordedFlavor = flavor
	}
}
