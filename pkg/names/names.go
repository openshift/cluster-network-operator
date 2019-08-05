package names

import "k8s.io/apimachinery/pkg/types"

// some names

// OperatorConfig is the name of the CRD that defines the complete
// operator configuration
const OPERATOR_CONFIG = "cluster"

// CLUSTER_CONFIG is the name of the higher-level cluster configuration
// and status object.
const CLUSTER_CONFIG = "cluster"

// PROXY_CONFIG is the name of the default proxy object.
const PROXY_CONFIG = "cluster"

// APPLIED_PREFIX is the prefix applied to the config maps
// where we store previously applied configuration
const APPLIED_PREFIX = "applied-"

// APPLIED_NAMESPACE is the namespace where applied configuration
// configmaps are stored.
// Should match 00_namespace.yaml
const APPLIED_NAMESPACE = "openshift-network-operator"

// IgnoreObjectErrorAnnotation is an annotation we can set on objects
// to signal to the reconciler that we don't care if they fail to create
// or update. Useful when we want to make a CR for which the CRD may not exist yet.
const IgnoreObjectErrorAnnotation = "networkoperator.openshift.io/ignore-errors"

// SERVICE_CA_CONFIGMAP is the name of the ConfigMap that contains service CA bundle
// that is used in multus admission controller deployment
const SERVICE_CA_CONFIGMAP = "openshift-service-ca"

// MULTUS_VALIDATING_WEBHOOK is the name of the ValidatingWebhookConfiguration for multus-admission-controller
// that is used in multus admission controller deployment
const MULTUS_VALIDATING_WEBHOOK = "multus.openshift.io"

// PROXY_TRUSTED_CA_CONFIGMAP is the name of the proxy.spec.trustedCA
// ConfigMap that contains the trusted CA certificate bundle used for
// proxying HTTPS connections.
const PROXY_TRUSTED_CA_CONFIGMAP = "user-ca-bundle"

// PROXY_TRUSTED_CA_CONFIGMAP_NS is the namespace that hosts the
// PROXY_TRUSTED_CA_CONFIGMAP ConfigMap.
const PROXY_TRUSTED_CA_CONFIGMAP_NS = "openshift-config-managed"

// ProxyTrustedCAConfigMap returns the namespaced name of the ConfigMap
// containing the proxy trusted CA certificate bundle.
func ProxyTrustedCAConfigMap() types.NamespacedName {
	return types.NamespacedName{
		Namespace: PROXY_TRUSTED_CA_CONFIGMAP_NS,
		Name:      PROXY_TRUSTED_CA_CONFIGMAP,
	}
}
