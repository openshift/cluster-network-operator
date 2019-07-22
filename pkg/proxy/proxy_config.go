package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	configv1 "github.com/openshift/api/config/v1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	k8serrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// The number of times the controller will attempt to issue an http GET
	// to the endpoint specified in readinessEndpoints.
	proxyProbeMaxRetries = 3
	// clusterConfigMapName is the name of the proxy.spec.trustedCA
	// ConfigMap that contains the CA bundle certificate.
	clusterConfigMapName = "proxy-ca"
	// clusterConfigMapNamespace is the name of the namespace that hosts
	// the proxy.spec.trustedCA ConfigMap.
	clusterConfigMapNamespace = "openshift-config-managed"
	// clusterConfigMapKey is the name of the data key containing the PEM encoded
	// CA certificate trust bundle in clusterConfigMapName.
	clusterConfigMapKey = "ca-bundle.crt"
	// installerConfigMapName is the name of the ConfigMap generated
	// by the installer containing the user-provided CA certificate bundle.
	installerConfigMapName = "proxy-ca-bundle"
	// installerConfigMapNamespace is the name of the namespace that hosts the
	// ConfigMap containing the user-provided CA certificate bundle.
	installerConfigMapNamespace = "openshift-config"

	proxyHTTPScheme   = "http"
	proxyHTTPSScheme  = "https"
)

// ValidateProxyConfig ensures the proxy config is valid.
func ValidateProxyConfig(cli client.Client, proxyConfig configv1.ProxySpec) error {
	if len(proxyConfig.HTTPProxy) != 0 {
		scheme, err := validateURI(proxyConfig.HTTPProxy)
		if err != nil {
			return fmt.Errorf("invalid httpProxy URI: %v", err)
		}
		if scheme != proxyHTTPScheme {
			return fmt.Errorf("httpProxy requires a %q URI scheme", proxyHTTPScheme)
		}
	}
	if len(proxyConfig.HTTPSProxy) != 0 {
		if len(proxyConfig.TrustedCA.Name) != 0 {
			return errors.New("trustedCA is required when using httpsProxy")
		}
		scheme, err := validateURI(proxyConfig.HTTPSProxy)
		if err != nil {
			return fmt.Errorf("invalid httpsProxy URI: %v", err)
		}
		if scheme != proxyHTTPSScheme {
			return fmt.Errorf("httpsProxy requires a %q URI scheme", proxyHTTPSScheme)
		}
	}
	if len(proxyConfig.NoProxy) != 0 {
		for _, v := range strings.Split(proxyConfig.NoProxy, ",") {
			v = strings.TrimSpace(v)
			errDomain := validateDomainName(v, false)
			_, _, errCIDR := net.ParseCIDR(v)
			if errDomain != nil && errCIDR != nil {
				return fmt.Errorf("invalid noProxy: %v", v)
			}
		}
	}
	if len(proxyConfig.TrustedCA.Name) != 0 {
		if proxyConfig.TrustedCA.Name != clusterConfigMapName {
			return fmt.Errorf("invalid ConfigMap reference for TrustedCA: %s", proxyConfig.TrustedCA.Name)
		}
		cfgMap := &corev1.ConfigMap{}
		if err := cli.Get(context.TODO(), clusterCAConfigMapName(), cfgMap); err != nil {
			return err
		}
		// TODO: Have validateClusterCABundle return certBundle []byte containing the validated ca bundle.
		if err := validateClusterCABundle(cfgMap); err != nil {
			return fmt.Errorf("validation failed for trustedCA %s: %v", proxyConfig.TrustedCA.Name, err)
		}
	}
	if proxyConfig.ReadinessEndpoints != nil {
		for _, endpoint := range proxyConfig.ReadinessEndpoints {
			scheme, err := validateURI(endpoint)
			if err != nil {
				return fmt.Errorf("invalid URI for endpoint %s: %v", endpoint, err)
			}
			switch {
			case scheme == proxyHTTPScheme:
				if err := validateHTTPReadinessEndpoint(endpoint); err != nil {
					return fmt.Errorf("readinessEndpoint probe failed for endpoint %s", endpoint)
				}
			// TODO: Uncomment after validateClusterCABundle() returns a validated ca bundle.
			/*case scheme == proxyHTTPSScheme:
				if err := validateHTTPSReadinessEndpoint(caBundle, endpoint); err != nil {
					return fmt.Errorf("readinessEndpoint probe failed for endpoint %s", endpoint)
				}*/
			default:
				return fmt.Errorf("readiness endpoints requires a %q or %q URI sheme", proxyHTTPScheme, proxyHTTPSScheme)
			}
		}
	}

	return nil
}

// validateURI validates if url is a valid absolute URI and returns
// the url scheme.
func validateURI(uri string) (string, error) {
	parsed, err := url.Parse(uri)
	if err != nil {
		return "", err
	}
	if !parsed.IsAbs() {
		return "", fmt.Errorf("failed validating URI, no scheme for URI %q", uri)
	}
	host := parsed.Hostname()
	if err := validateHost(host); err != nil {
		return "", fmt.Errorf("failed validating URI %q: %v", uri, err)
	}
	if port := parsed.Port(); len(port) != 0 {
		intPort, err := strconv.Atoi(port)
		if err != nil {
			return "", fmt.Errorf("failed converting port to integer for URI %q: %v", uri, err)
		}
		if err := validatePort(intPort); err != nil {
			return "", fmt.Errorf("failed to validate port for URL %q: %v", uri, err)
		}
	}

	return parsed.Scheme, nil
}

// validateHost validates if host is a valid IP address or subdomain in DNS (RFC 1123).
func validateHost(host string) error {
	errDomain := validateDomainName(host, false)
	errIP := validation.IsValidIP(host)
	if errDomain != nil && errIP != nil {
		return fmt.Errorf("invalid host: %s", host)
	}

	return nil
}

// validatePort validates if port is a valid port number between 1-65535.
func validatePort(port int) error {
	invalidPorts := validation.IsValidPortNum(port)
	if invalidPorts != nil {
		return fmt.Errorf("invalid port number: %d", port)
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
// endpoint in a finite loop based on the scheme type, it returns the
// last result if it never succeeds.
func validateHTTPReadinessEndpointWithRetries(endpoint string, retries int) error {
	for i := 0; i < retries; i++ {
		if err := runHTTPReadinessProbe(endpoint); err != nil {
			return err
		}
	}

	return nil
}

// runHTTPReadinessProbe issues an http GET request to an http endpoint
// and returns an error if a 2XX or 3XX http status code is not returned.
func runHTTPReadinessProbe(endpoint string) error {
	resp, err := http.Get(endpoint)
	if err != nil {
		return err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			fmt.Errorf("failed to close connection: %v", err)
		}
	}()

	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusBadRequest {
		return nil
	}

	return fmt.Errorf("HTTP probe failed with statuscode: %d", resp.StatusCode)
}

// validateHTTPSReadinessEndpoint validates an https readinessEndpoint endpoint.
func validateHTTPSReadinessEndpoint(certBundle []byte, endpoint string) error {
	if err := validateHTTPSReadinessEndpointWithRetries(certBundle, endpoint, proxyProbeMaxRetries); err != nil {
		return err
	}

	return nil
}

// validateHTTPSReadinessEndpointWithRetries tries to validate an endpoint
// by using certBundle to attempt a TLS handshake in a finite loop returning
// the last result if it never succeeds.
func validateHTTPSReadinessEndpointWithRetries(certBundle []byte, endpoint string, retries int) error {
	for i := 0; i < retries; i++ {
		if err := runHTTPSReadinessProbe(certBundle, endpoint); err != nil {
			return err
		}
	}

	return nil
}

// runHTTPSReadinessProbe tries connecting to endpoint by using certBundle
// to attempt a TLS handshake.
func runHTTPSReadinessProbe(certBundle []byte, endpoint string) error {
	parsedURL, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("failed parsing URL for endpoint: %s", endpoint)
	}
	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(certBundle) {
		return fmt.Errorf("failed to parse CA certificate bundle")
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
	defer func() {
		if err := conn.Close(); err != nil {
			fmt.Errorf("failed to close connection: %v", err)
		}
	}()

	return nil
}

// validateDomainName checks if the given string is a valid domain name and returns an error if not.
func validateDomainName(v string, acceptTrailingDot bool) error {
	if acceptTrailingDot {
		v = strings.TrimSuffix(v, ".")
	}
	return validateSubdomain(v)
}

// validateSubdomain checks if the given string is a valid subdomain name and returns an error if not.
func validateSubdomain(v string) error {
	validationMessages := validation.IsDNS1123Subdomain(v)
	if len(validationMessages) == 0 {
		return nil
	}

	errs := make([]error, len(validationMessages))
	for i, m := range validationMessages {
		errs[i] = errors.New(m)
	}
	return k8serrors.NewAggregate(errs)
}

// validateClusterCABundle validates that configMap contains a
// CA certificate bundle named clusterConfigMapKey and that
// clusterConfigMapKey contains a valid x.509 certificate.
func validateClusterCABundle(configMap *corev1.ConfigMap) error {
	if _, ok := configMap.Data[clusterConfigMapKey]; !ok {
		return fmt.Errorf("ConfigMap %q is missing %q", clusterConfigMapName, clusterConfigMapKey)
	}
	_, err := x509.ParseCertificates([]byte(configMap.Data[clusterConfigMapKey]))
	if err != nil {
		return err
	}

	return nil
}

// installerCAConfigMapName returns the namespaced name of the ConfigMap
// containing the installer-generated CA certificate bundle.
func installerCAConfigMapName() types.NamespacedName {
	return types.NamespacedName{
		Namespace: installerConfigMapNamespace,
		Name:      installerConfigMapName,
	}
}

// clusterCAConfigMapName returns the namespaced name of the ConfigMap
// containing the cluster CA certificate bundle.
func clusterCAConfigMapName() types.NamespacedName {
	return types.NamespacedName{
		Namespace: clusterConfigMapNamespace,
		Name:      clusterConfigMapName,
	}
}
