package proxyconfig

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/names"
	cnovalidation "github.com/openshift/cluster-network-operator/pkg/util/validation"

	corev1 "k8s.io/api/core/v1"
)

const (
	// proxyProbeMaxRetries is the number of times to attempt an http GET
	// to a readinessEndpoints endpoint.
	proxyProbeMaxRetries = 3
	proxyHTTPScheme      = "http"
	proxyHTTPSScheme     = "https"
	// noProxyWildcard is the string used to as a wildcard attached to a
	// domain suffix in proxy.spec.noProxy to bypass proxy for httpProxy
	// and httpsProxy.
	noProxyWildcard = "*"
)

// ValidateProxyConfig ensures that proxyConfig is valid.
func (r *ReconcileProxyConfig) ValidateProxyConfig(proxyConfig *configv1.ProxySpec) error {
	if isSpecHTTPProxySet(proxyConfig) {
		scheme, err := cnovalidation.URI(proxyConfig.HTTPProxy)
		if err != nil {
			return fmt.Errorf("invalid httpProxy URI: %v", err)
		}
		if scheme != proxyHTTPScheme {
			return fmt.Errorf("httpProxy requires an %q URI scheme", proxyHTTPScheme)
		}
	}

	readinessCerts := []*x509.Certificate{}
	if isSpecHTTPSProxySet(proxyConfig) {
		scheme, err := cnovalidation.URI(proxyConfig.HTTPSProxy)
		if err != nil {
			return fmt.Errorf("invalid httpsProxy URI: %v", err)
		}
		if scheme == proxyHTTPSScheme {
			// A trusted CA bundle must be provided when using https
			// between client and proxy.
			if !isSpecTrustedCASet(proxyConfig) {
				return errors.New("trustedCA is required when using an https scheme with httpsProxy")
			}
			certBundle, err := r.validateTrustedCA(proxyConfig)
			if err != nil {
				return fmt.Errorf("failed validating TrustedCA %q: %v", proxyConfig.TrustedCA.Name, err)
			}
			for _, cert := range certBundle {
				readinessCerts = append(readinessCerts, cert)
			}
		}
	}

	if isSpecNoProxySet(proxyConfig) {
		if proxyConfig.NoProxy != noProxyWildcard {
			for _, v := range strings.Split(proxyConfig.NoProxy, ",") {
				v = strings.TrimSpace(v)
				errDomain := cnovalidation.DomainName(v, false)
				_, _, errCIDR := net.ParseCIDR(v)
				if errDomain != nil && errCIDR != nil {
					return fmt.Errorf("invalid noProxy: %v", v)
				}
			}
		}
	}

	if isSpecReadinessEndpoints(proxyConfig) {
		for _, endpoint := range proxyConfig.ReadinessEndpoints {
			scheme, err := cnovalidation.URI(endpoint)
			if err != nil {
				return fmt.Errorf("invalid URI for endpoint %s: %v", endpoint, err)
			}
			switch {
			case scheme == proxyHTTPScheme:
				if err := validateHTTPReadinessEndpoint(endpoint); err != nil {
					return fmt.Errorf("readinessEndpoint probe failed for endpoint %s", endpoint)
				}
			case scheme == proxyHTTPSScheme:
				if !isSpecTrustedCASet(proxyConfig) {
					return fmt.Errorf("readinessEndpoint with an %q scheme requires trustedCA to be set", proxyHTTPSScheme)
				}
				if err := validateHTTPSReadinessEndpoint(readinessCerts, endpoint); err != nil {
					return fmt.Errorf("readinessEndpoint probe failed for endpoint %s", endpoint)
				}
			default:
				return fmt.Errorf("readiness endpoints requires a %q or %q URI sheme", proxyHTTPScheme, proxyHTTPSScheme)
			}
		}
	}

	return nil
}

// validateHTTPReadinessEndpoint validates an http readinessEndpoint endpoint.
func validateHTTPReadinessEndpoint(endpoint string) error {
	if err := validateHTTPReadinessEndpointWithRetries(endpoint, proxyProbeMaxRetries); err != nil {
		return err
	}

	return nil
}

// validateHTTPReadinessEndpointWithRetries tries to validate an http
// endpoint in a finite loop and returns the last result if it never succeeds.
func validateHTTPReadinessEndpointWithRetries(endpoint string, retries int) error {
	var err error
	for i := 0; i < retries; i++ {
		err = runHTTPReadinessProbe(endpoint)
		if err == nil {
			return nil
		}
	}

	return err
}

// runHTTPReadinessProbe issues an http GET request to endpoint and returns
// an error if a 2XX or 3XX http status code is not returned.
func runHTTPReadinessProbe(endpoint string) error {
	resp, err := http.Get(endpoint)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusBadRequest {
		return nil
	}

	return fmt.Errorf("HTTP probe failed with statuscode: %d", resp.StatusCode)
}

// validateHTTPSReadinessEndpoint validates endpoint using certBundle as trusted CAs.
func validateHTTPSReadinessEndpoint(certBundle []*x509.Certificate, endpoint string) error {
	if err := validateHTTPSReadinessEndpointWithRetries(certBundle, endpoint, proxyProbeMaxRetries); err != nil {
		return err
	}

	return nil
}

// validateHTTPSReadinessEndpointWithRetries tries to validate endpoint
// by using certBundle as trusted CAs to create a TLS connection using a
// finite loop based on retries, returning an error if it never succeeds.
func validateHTTPSReadinessEndpointWithRetries(certBundle []*x509.Certificate, endpoint string, retries int) error {
	for i := 0; i < retries; i++ {
		if err := runHTTPSReadinessProbe(certBundle, endpoint); err != nil {
			return err
		}
	}

	return nil
}

// runHTTPSReadinessProbe tries connecting to endpoint by using certBundle as
// trusted CAs to create a TLS connection, returning an error if it never succeeds.
func runHTTPSReadinessProbe(certBundle []*x509.Certificate, endpoint string) error {
	parsedURL, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("failed parsing URL for endpoint: %s", endpoint)
	}
	certPool := x509.NewCertPool()
	for _, cert := range certBundle {
		certPool.AddCert(cert)
	}
	port := parsedURL.Port()
	if len(port) == 0 {
		parsedURL.Host += ":" + port
	}
	conn, err := tls.Dial("tcp", parsedURL.String(), &tls.Config{
		RootCAs:    certPool,
		ServerName: endpoint,
	})
	if err != nil {
		return fmt.Errorf("failed to connect to endpoint %q: %v", endpoint, err)
	}

	return conn.Close()
}

// validateTrustedCA validates that trustedCA of proxyConfig is a
// valid ConfigMap reference and that the configmap contains a
// valid trust bundle, returning a byte[] of the trust bundle
// data upon success.
func (r *ReconcileProxyConfig) validateTrustedCA(proxyConfig *configv1.ProxySpec) ([]*x509.Certificate, error) {
	cfgMap, err := r.validateTrustedCAConfigMap(proxyConfig)
	if err != nil {
		return nil, err
	}

	certBundle, _, err := cnovalidation.TrustBundleConfigMap(cfgMap)
	if err != nil {
		return nil, err
	}

	return certBundle, nil
}

// validateTrustedCAConfigMap validates that proxyConfig is a
// valid ConfigMap reference.
func (r *ReconcileProxyConfig) validateTrustedCAConfigMap(proxyConfig *configv1.ProxySpec) (*corev1.ConfigMap, error) {
	if !isTrustedCAConfigMap(proxyConfig) {
		return nil, fmt.Errorf("invalid ConfigMap reference for TrustedCA: %s", proxyConfig.TrustedCA.Name)
	}
	cfgMap := &corev1.ConfigMap{}
	if err := r.client.Get(context.TODO(), names.MergedTrustBundleName(), cfgMap); err != nil {
		return nil, err
	}

	return cfgMap, nil
}
