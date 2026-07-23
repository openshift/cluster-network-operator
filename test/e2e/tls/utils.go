// Package tls - Common utilities and constants for TLS profile compliance testing
package tls

import (
	"fmt"
	"strings"
	"time"
)

// Test infrastructure constants
const (
	SharedScannerNamespace = "tls-scanner-shared"
	ScannerPodName         = "tls-scanner"
	ScannerContainerName   = "scanner"

	// OpenShift-Multus namespace components
	DaemonSetLabelSelector      = "app=multus"
	NetworkMetricsDaemonSetName = "network-metrics-daemon"      // DaemonSet with kube-rbac-proxy
	DeploymentName              = "multus-admission-controller" // Deployment with kube-rbac-proxy
	KubeRBACProxyContainerName  = "kube-rbac-proxy"

	// OpenShift-Network-Operator namespace
	// NOTE: This namespace currently has NO kube-rbac-proxy containers
	// Workloads in this namespace:
	//   1. network-operator deployment - has only network-operator container (no kube-rbac-proxy)
	//   2. iptables-alerter daemonset - has only iptables-alerter container (no kube-rbac-proxy)
	// This namespace is prepared for future expansion only if components with kube-rbac-proxy are added
	NetworkOperatorNamespace  = "openshift-network-operator"
	NetworkOperatorDeployment = "network-operator"
	IptablesAlerterDaemonSet  = "iptables-alerter"

	// OpenShift-OVN-Kubernetes namespace
	// SKIPPED in tests due to product bug: TLS args hardcoded in bash scripts
	OVNKubernetesNamespace           = "openshift-ovn-kubernetes"
	OVNKubeNodeDaemonSetName         = "ovnkube-node"                // DaemonSet with 2 kube-rbac-proxy containers
	OVNKubeControlPlaneDeployment    = "ovnkube-control-plane"       // Deployment with 1 kube-rbac-proxy container
	OVNKubeRBACProxyNodeContainer    = "kube-rbac-proxy-node"        // Proxies ovn-node-metrics on port 9103→29103
	OVNKubeRBACProxyOVNContainer     = "kube-rbac-proxy-ovn-metrics" // Proxies ovn-metrics on port 9105→29105
	OVNKubeRBACProxyControlContainer = "kube-rbac-proxy"             // Control plane metrics proxy

	// Ports
	// NOTE: TLS connection testing uses tls-scanner tool which dynamically discovers
	// all TLS endpoints (--all-pods). Port numbers are NOT hardcoded - scanner finds them.
	// The constants below are legacy references only.
	DaemonSetMetricsPort  = 9301 // Legacy reference - actual ports discovered by scanner
	ControllerMetricsPort = 9300
	ControllerWebhookPort = 9443

	// Timeouts
	ScannerBuildTimeout         = 20 * time.Minute
	OperatorReadyTimeout        = 5 * time.Minute
	MCPReadyTimeout             = 60 * time.Minute
	ClusterOperatorReadyTimeout = 60 * time.Minute // OpenShift TLS test standard for cluster operator stabilization
	TLSEndpointPollTimeout      = 2 * time.Minute

	// Scanner configuration
	ScannerImage         = "registry.access.redhat.com/ubi9/ubi:latest"
	ScannerCPURequest    = "2"
	ScannerMemoryRequest = "4Gi"
	ScannerCPULimit      = "4"
	ScannerMemoryLimit   = "4Gi"
)

// Pointer helper functions
func boolPtr(b bool) *bool {
	return &b
}

func int64Ptr(i int64) *int64 {
	return &i
}

func stringPtr(s string) *string {
	return &s
}

// formatTLSVersion formats TLS version for scanner result comparison (e.g., "1.2" -> "TLSv1.2")
func formatTLSVersion(tlsVersion string) string {
	return fmt.Sprintf("TLSv%s", tlsVersion)
}

// containsFeatureGate checks if a featuregate is in the enabled list
func containsFeatureGate(enabledGates, featureName string) bool {
	// enabledGates is a space-separated list like "FeatureA FeatureB TLSAdherence"
	gates := strings.Fields(enabledGates)
	for _, gate := range gates {
		if gate == featureName {
			return true
		}
	}
	return false
}
