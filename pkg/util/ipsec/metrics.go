package ipsec

import (
	"strconv"

	"k8s.io/component-base/metrics"
	"k8s.io/component-base/metrics/legacyregistry"
	"k8s.io/klog/v2"
)

const (
	cnoNamespace  = "openshift_network_operator"
	ipsecStateNA  = "N/A - ipsec not supported (non-OVN network)"
	ipsecDisabled = "Disabled"
)

var (
	ipsecStateGauge *metrics.GaugeVec
)

func init() {
	ipsecStateGauge = metrics.NewGaugeVec(&metrics.GaugeOpts{
		Namespace: cnoNamespace,
		Name:      "ipsec_state",
		Help: "A metric with a constant '1' value labeled by the latest ipsecMode of the cluster, " +
			"and the API that invoked it, legacy (pre OCP 4.14) or new. " +
			"In case the network doesn't support ipsec (non-OVN network), " +
			"the 'is_legacy_api' value is set to '" + ipsecStateNA + "'.",
	}, []string{"mode", "is_legacy_api"})
	legacyregistry.MustRegister(ipsecStateGauge)
}

func UpdateIPsecMetric(state string, isLegacyAPI bool) {
	klog.V(5).Infof("IPsec mode: %s, isLegacyAPI: %v",
		state, isLegacyAPI)
	ipsecStateGauge.Reset()
	ipsecStateGauge.WithLabelValues(state, strconv.FormatBool(isLegacyAPI)).Set(1)
}

func UpdateIPsecMetricNA() {
	klog.V(5).Infof("IPsec is not supported by non-OVN network (disabled)")
	ipsecStateGauge.Reset()
	ipsecStateGauge.WithLabelValues(ipsecDisabled, ipsecStateNA).Set(1)
}
