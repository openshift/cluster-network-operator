package proxyconfig

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/util/validation"
)

const (
	proxyHTTPScheme  = "http"
	proxyHTTPSScheme = "https"
	// proxyProbeMaxRetries is the number of times to attempt an http GET
	// to a readinessEndpoints endpoint.
	proxyProbeMaxRetries = 3
	// proxyProbeWaitTime is the time to wait before retrying a failed proxy probe.
	proxyProbeWaitTime = 1 * time.Second
	// noProxyWildcard is the string used to as a wildcard attached to a
	// domain suffix in proxy.spec.noProxy to bypass proxying.
	noProxyWildcard = "*"
)

// ValidateProxyConfig ensures that proxyConfig is valid.
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
		for _, endpoint := range proxyConfig.ReadinessEndpoints {
			scheme, err := validation.URI(endpoint)
			if err != nil {
				return fmt.Errorf("invalid URI for endpoint '%s': %v", endpoint, err)
			}
			switch {
			case scheme == proxyHTTPScheme:
				// TODO: Add case for proxyHTTPSScheme when CA support is merged.
				if err := validateHTTPReadinessEndpoint(proxyConfig.HTTPProxy, endpoint); err != nil {
					return fmt.Errorf("readinessEndpoint probe failed for endpoint '%s'", endpoint)
				}
			default:
				// TODO: Update error to include proxyHTTPSScheme when CA support is merged.
				return fmt.Errorf("readiness endpoints requires a '%s' URI sheme", proxyHTTPScheme)
			}
		}
	}

	return nil
}

// validateHTTPReadinessEndpoint validates an http readinessEndpoint endpoint.
func validateHTTPReadinessEndpoint(httpProxy, endpoint string) error {
	if err := validateHTTPReadinessEndpointWithRetries(httpProxy, endpoint, proxyProbeMaxRetries); err != nil {
		return err
	}

	return nil
}

// validateHTTPReadinessEndpointWithRetries tries to validate an http
// endpoint in a finite loop and returns the last result if it never succeeds.
func validateHTTPReadinessEndpointWithRetries(httpProxy, endpoint string, retries int) error {
	var err error
	for i := 0; i < retries; i++ {
		err = runHTTPReadinessProbe(httpProxy, endpoint)
		if err == nil {
			return nil
		}
		time.Sleep(proxyProbeWaitTime)
	}

	return err
}

// runHTTPReadinessProbe issues an http GET request to endpoint and returns
// an error if a 2XX or 3XX http status code is not returned. The request
// is proxied if proxy environment variables exist.
func runHTTPReadinessProbe(httpProxy, endpoint string) error {
	proxyURL, err := url.Parse(httpProxy)
	if err != nil {
		return fmt.Errorf("failed to parse httpProxy url '%s': %v", httpProxy, err)
	}

	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
	}

	client := &http.Client{
		Transport: transport,
	}

	request, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to create http request for '%s': %v", endpoint, err)
	}

	resp, err := client.Do(request)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusBadRequest {
		return nil
	}

	return fmt.Errorf("http probe failed with statuscode: %d", resp.StatusCode)
}
