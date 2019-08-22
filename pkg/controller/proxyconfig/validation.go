package proxyconfig

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"k8s.io/apimachinery/pkg/types"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/util/validation"

	corev1 "k8s.io/api/core/v1"
)

const (
	proxyHTTPScheme  = "http"
	proxyHTTPSScheme = "https"
	// noProxyWildcard is the string used to as a wildcard attached to a
	// domain suffix in proxy.spec.noProxy to bypass proxying.
	noProxyWildcard = "*"
	// proxyProbeMaxRetries is the number of times to attempt an http GET
	// to a readinessEndpoints endpoint.
	proxyProbeMaxRetries = 3
	// proxyProbeWaitTime is the time to wait before retrying a failed proxy probe.
	proxyProbeWaitTime = 1 * time.Second
)

// ValidateProxyConfig ensures that httpProxy, httpsProxy and
// noProxy fields of proxyConfig are valid.
func (r *ReconcileProxyConfig) ValidateProxyConfig(proxyConfig *configv1.ProxySpec) error {
	if isSpecHTTPProxySet(proxyConfig) {
		scheme, err := validation.URI(proxyConfig.HTTPProxy)
		if err != nil {
			return fmt.Errorf("invalid httpProxy URI: %v", err)
		}
		if scheme != proxyHTTPScheme {
			return fmt.Errorf("httpProxy requires a '%s' URI scheme", proxyHTTPScheme)
		}
	}

	if isSpecHTTPSProxySet(proxyConfig) {
		scheme, err := validation.URI(proxyConfig.HTTPSProxy)
		if err != nil {
			return fmt.Errorf("invalid httpsProxy URI: %v", err)
		}
		if scheme != proxyHTTPScheme && scheme != proxyHTTPSScheme {
			return fmt.Errorf("httpsProxy requires a '%s' or '%s' URI scheme", proxyHTTPScheme, proxyHTTPSScheme)
		}
	}

	if isSpecNoProxySet(proxyConfig) {
		if proxyConfig.NoProxy != noProxyWildcard {
			for _, v := range strings.Split(proxyConfig.NoProxy, ",") {
				v = strings.TrimSpace(v)
				errDomain := validation.DomainName(v, false)
				_, _, errCIDR := net.ParseCIDR(v)
				if errDomain != nil && errCIDR != nil {
					return fmt.Errorf("invalid noProxy: %v", v)
				}
			}
		}
	}

	if isSpecReadinessEndpointsSet(proxyConfig) {
		var trustBundle []*x509.Certificate
		for _, endpoint := range proxyConfig.ReadinessEndpoints {
			scheme, err := validation.URI(endpoint)
			if err != nil {
				return fmt.Errorf("invalid URI for readinessEndpoint '%s': %v", endpoint, err)
			}
			switch {
			case scheme == proxyHTTPScheme:
				if !isSpecHTTPProxySet(proxyConfig) {
					return fmt.Errorf("httpProxy must be set when using a http proxy readinessEndpoint")
				}
				if err := validateReadinessEndpoint(trustBundle, proxyConfig.HTTPProxy, endpoint); err != nil {
					return fmt.Errorf("http readinessEndpoint probe failed for endpoint '%s': %v", endpoint, err)
				}
			case scheme == proxyHTTPSScheme:
				if !isSpecHTTPSProxySet(proxyConfig) {
					return fmt.Errorf("httpsProxy must be set when using a https proxy readinessEndpoint")
				}
				var systemData []byte
				var proxyData []byte
				if isSpecTrustedCASet(proxyConfig) {
					// TrustedCA is set, so create a combined trustedCA/system trust bundle for readinessEndpoints.
					proxyData, systemData, err = r.validateTrustedCA(proxyConfig.TrustedCA.Name)
					if err != nil {
						return fmt.Errorf("failed to get certificate data for trustedCA '%s': %v",
							proxyConfig.TrustedCA.Name, err)
					}
				} else {
					// No trustedCA is set, so use the system trust bundle for readinessEndpoints.
					systemData, err = ioutil.ReadFile(names.SYSTEM_TRUST_BUNDLE)
					if err != nil {
						return fmt.Errorf("failed to read system trust bundle '%s': %v",
							names.SYSTEM_TRUST_BUNDLE, err)
					}
				}
				// Merge the proxy trustedCA (if it exists) and system trust bundle data.
				trustBundle, err = validation.MergeCertificateData(systemData, proxyData)
				if err != nil {
					return fmt.Errorf("failed to merge system and trustedCA trust bundles: %v", err)
				}
				if err := validateReadinessEndpoint(trustBundle, proxyConfig.HTTPSProxy, endpoint); err != nil {
					return fmt.Errorf("readinessEndpoint probe failed for endpoint '%s': %v", endpoint, err)
				}
			default:
				return fmt.Errorf("a proxy readiness endpoint requires a '%s' or '%s' URI sheme",
					proxyHTTPScheme, proxyHTTPSScheme)
			}
		}
	}

	return nil
}

// validateTrustedCA validates that trustedCA is a valid ConfigMap
// reference and that the ConfigMap contains a valid trust bundle,
// returning the byte slices of the certificate data from the
// validated trustedCA and system trust bundles.
func (r *ReconcileProxyConfig) validateTrustedCA(trustedCA string) ([]byte, []byte, error) {
	cfgMap, err := r.validateConfigMapRef(trustedCA)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to validate configmap reference for proxy trustedCA '%s': %v",
			trustedCA, err)
	}

	_, bundleData, err := r.validateTrustBundle(cfgMap)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to validate trust bundle for proxy trustedCA '%s': %v",
			trustedCA, err)
	}

	systemData, err := r.validateSystemTrustBundle(names.SYSTEM_TRUST_BUNDLE)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to validate system trust bundle '%s': %v", names.SYSTEM_TRUST_BUNDLE, err)
	}

	return bundleData, systemData, nil
}

// validateConfigMapRef validates that trustedCA is a valid ConfigMap reference,
// returning the validated ConfigMap.
func (r *ReconcileProxyConfig) validateConfigMapRef(trustedCA string) (*corev1.ConfigMap, error) {
	cfgMap := &corev1.ConfigMap{}
	ns := names.ADDL_TRUST_BUNDLE_CONFIGMAP_NS
	if trustedCA == names.TRUSTED_CA_BUNDLE_CONFIGMAP {
		ns = names.TRUSTED_CA_BUNDLE_CONFIGMAP_NS
	}
	if err := r.client.Get(context.TODO(), types.NamespacedName{Namespace: ns, Name: trustedCA}, cfgMap); err != nil {
		return nil, fmt.Errorf("failed to get trustedCA configmap for proxy %s: %v", names.PROXY_CONFIG, err)
	}

	return cfgMap, nil
}

// validateTrustBundle is a wrapper for validation.TrustBundleConfigMap(), which
// validates that cfgMap contains a data key named "ca-bundle.crt" and the value
// of the key is one or more valid PEM encoded certificates, returning slices of
// the validated certificates and certificate data.
func (r *ReconcileProxyConfig) validateTrustBundle(cfgMap *corev1.ConfigMap) ([]*x509.Certificate, []byte, error) {
	certBundle, bundleData, err := validation.TrustBundleConfigMap(cfgMap)
	if err != nil {
		return nil, nil, err
	}

	return certBundle, bundleData, nil
}

// validateSystemTrustBundle reads the trustBundle file, ensuring each
// PEM block is type "CERTIFICATE" and the block can be parsed as an
// x509 CA certificate, returning the parsed certificates as a []byte.
func (r *ReconcileProxyConfig) validateSystemTrustBundle(trustBundle string) ([]byte, error) {
	bundleData, err := ioutil.ReadFile(trustBundle)
	if err != nil {
		return nil, err
	}
	if _, _, err := validation.CertificateData(bundleData); err != nil {
		return nil, err
	}

	return bundleData, nil
}

// validateReadinessEndpoint validates endpoint using proxy. If caBundle
// is not nil, TLS is used for the probe with caBundle as the cert pool.
func validateReadinessEndpoint(caBundle []*x509.Certificate, proxy, endpoint string) error {
	proxyURL, err := url.Parse(proxy)
	if err != nil {
		return fmt.Errorf("failed to parse proxy url '%s': %v", proxy, err)
	}

	endpointURL, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("failed to parse endpoint url '%s': %v", endpoint, err)
	}

	if endpointURL.Scheme == proxyHTTPScheme && proxyURL.Scheme == proxyHTTPSScheme {
		return fmt.Errorf("endpoint '%s' requires a `%s` proxy scheme", endpoint, proxyHTTPScheme)
	}

	if endpointURL.Scheme == proxyHTTPSScheme && proxyURL.Scheme == proxyHTTPScheme {
		return fmt.Errorf("endpoint '%s' requires a `%s` proxy scheme", endpoint, proxyHTTPSScheme)
	}

	if proxyURL.Scheme == proxyHTTPSScheme && len(caBundle) == 0 {
		return fmt.Errorf("https proxy probe requires at least one CA certificate")
	}

	if err := validateReadinessEndpointWithRetries(caBundle, proxyURL, endpointURL, proxyProbeMaxRetries); err != nil {
		return err
	}

	return nil
}

// validateReadinessEndpointWithRetries tries to validate endpoint in a
// finite loop using proxy and returns the last result if it never succeeds.
func validateReadinessEndpointWithRetries(caBundle []*x509.Certificate, proxy, endpoint *url.URL, retries int) error {
	var err error
	for i := 0; i < retries; i++ {
		err = runReadinessProbe(caBundle, proxy, endpoint)
		if err == nil {
			return nil
		}
		time.Sleep(proxyProbeWaitTime)
	}

	return err
}

// runReadinessProbe issues an GET request to endpoint using proxy and
// returns an error if a 2XX or 3XX http status code is not returned.
// If proxy has a https scheme and caBundle contains at least one
// valid CA certificate, TLS transport will be used by the client.
func runReadinessProbe(caBundle []*x509.Certificate, proxy, endpoint *url.URL) error {
	transport := &http.Transport{
		Proxy: http.ProxyURL(proxy),
	}

	if proxy.Scheme == proxyHTTPSScheme {
		caPool := x509.NewCertPool()
		for _, cert := range caBundle {
			caPool.AddCert(cert)
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: caPool}
	}

	client := &http.Client{
		Transport: transport,
	}

	request, err := http.NewRequest("GET", endpoint.String(), nil)
	if err != nil {
		return fmt.Errorf("failed to create request for '%s' using proxy '%s': %v", endpoint.String(),
			proxy.String(), err)
	}

	resp, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("endpoint probe failed for endpoint '%s' using proxy '%s': %v",
			endpoint.String(), proxy.String(), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusBadRequest {
		return nil
	}

	return fmt.Errorf("endpoint probe failed with statuscode '%d' for endpoint '%s' using proxy '%s' ",
		resp.StatusCode, endpoint.String(), proxy.String())
}
