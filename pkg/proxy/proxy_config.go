package proxy

import (
	configv1 "github.com/openshift/api/config/v1"
)

// ValidateProxyConfig ensures the proxy config is valid.
func ValidateProxyConfig(proxyConfig configv1.ProxySpec) error {
	// TODO: Validate proxy spec according to design and write status.
}
