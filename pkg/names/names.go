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

// ADDL_TRUST_BUNDLE_CONFIGMAP_NS is the namespace for one or more
// ConfigMaps that contain user provided trusted CA bundles.
const ADDL_TRUST_BUNDLE_CONFIGMAP_NS = "openshift-config"

// CA_BUNDLE_CONFIGMAP_KEY is the name of the data key containing
// the PEM encoded trust bundle.
const CA_BUNDLE_CONFIGMAP_KEY = "ca-bundle.crt"

// TRUSTED_CA_BUNDLE_CONFIGMAP is the name of the ConfigMap
// containing the combined user/system trust bundle.
const TRUSTED_CA_BUNDLE_CONFIGMAP = "trusted-ca-bundle"

// TRUSTED_CA_BUNDLE_CONFIGMAP_NS is the namespace that hosts the
// ADDL_TRUST_BUNDLE_CONFIGMAP and TRUST_BUNDLE_CONFIGMAP
// ConfigMaps.
const TRUSTED_CA_BUNDLE_CONFIGMAP_NS = "openshift-config-managed"

// TRUSTED_CA_BUNDLE_CONFIGMAP_LABEL is the name of the label that
// determines whether or not to inject the combined ca certificate
const TRUSTED_CA_BUNDLE_CONFIGMAP_LABEL = "config.openshift.io/inject-trusted-cabundle"

// SYSTEM_TRUST_BUNDLE is the full path to the file containing
// the system trust bundle.
const SYSTEM_TRUST_BUNDLE = "/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem"

// Proxy returns the namespaced name "cluster" in the
// default namespace.
func Proxy() types.NamespacedName {
	return types.NamespacedName{
		Name: PROXY_CONFIG,
	}
}

// TrustedCABundleConfigMap returns the namespaced name of the ConfigMap
// openshift-config-managed/trusted-ca-bundle trust bundle.
func TrustedCABundleConfigMap() types.NamespacedName {
	return types.NamespacedName{
		Namespace: TRUSTED_CA_BUNDLE_CONFIGMAP_NS,
		Name:      TRUSTED_CA_BUNDLE_CONFIGMAP,
	}
}

// KURYR_ADMISSION_CONTROLLER_SECRET is the name of the Secret that stores the admission controller CA and Key
const KURYR_ADMISSION_CONTROLLER_SECRET = "kuryr-dns-admission-controller-secret"

// KURYR_WEB_HOOK_SECRET is the name of the secret used in the kuryr-dns-admission-controller DaemonSet
const KURYR_WEBHOOK_SECRET = "kuryr-webhook-secret"
