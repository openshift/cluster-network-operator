package network

import (
	"github.com/prometheus/client_golang/prometheus"
	operv1 "github.com/openshift/api/operator/v1"
)

func SetIPsecMode(newMode operv1.IPsecMode) {
	buildInfo := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "cluster_network_ipsec_mode",
			Help: "A metric with a constant '1' value labeled by the latest ipsecMode of the cluster",
		},
		[]string{"ipsecMode"},
	)
	buildInfo.WithLabelValues(string(newMode)).Set(1)

	prometheus.MustRegister(buildInfo)
}
/*****

{__name__="up"} or 
{__name__="cluster_version"} or 
{__name__="cluster_version_available_updates"} or 
{__name__="cluster_operator_up"} or 
{__name__="cluster_operator_conditions"} or 
{__name__="cluster_version_payload"} or 
{__name__="cluster_version_payload_errors"} or 
{__name__="instance:etcd_object_counts:sum"} or 
{__name__="ALERTS",alertstate="firing"} or 
{__name__="code:apiserver_request_count:rate:sum"} or 
{__name__="kube_pod_status_ready:etcd:sum"} or 
{__name__="kube_pod_status_ready:image_registry:sum"} or 
{__name__="cluster:capacity_cpu_cores:sum"} or 
{__name__="cluster:capacity_memory_bytes:sum"} or 
{__name__="cluster:cpu_usage_cores:sum"} or 
{__name__="cluster:memory_usage_bytes:sum"} or 
{__name__="openshift:cpu_usage_cores:sum"} or 
{__name__="openshift:memory_usage_bytes:sum"} or 
{__name__="cluster:node_instance_type_count:sum"}

*****/