package validation

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	k8serrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/validation"
)

// DomainName checks if the given string is a valid domain name.
func DomainName(v string, acceptTrailingDot bool) error {
	v = strings.TrimPrefix(v, ".")
	if acceptTrailingDot {
		v = strings.TrimSuffix(v, ".")
	}

	return Subdomain(v)
}

// Subdomain checks if the given string is a valid subdomain name.
func Subdomain(v string) error {
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

// Host validates if host is a valid IP address or subdomain in DNS (RFC 1123).
func Host(host string) error {
	errDomain := DomainName(host, false)
	errIP := validation.IsValidIP(host)
	if errDomain != nil && errIP != nil {
		return fmt.Errorf("invalid host: %s", host)
	}

	return nil
}

// Port validates if port is a valid port number between 1-65535.
func Port(port int) error {
	invalidPorts := validation.IsValidPortNum(port)
	if invalidPorts != nil {
		return fmt.Errorf("invalid port number: %d", port)
	}

	return nil
}

// URI validates uri as being a http(s) valid url and returns the url scheme.
func URI(uri string) (string, error) {
	parsed, err := url.ParseRequestURI(uri)
	if err != nil {
		return "", err
	}
	if !parsed.IsAbs() {
		return "", fmt.Errorf("failed validating URI, no scheme for URI %q", uri)
	}
	if port := parsed.Port(); len(port) != 0 {
		intPort, err := strconv.Atoi(port)
		if err != nil {
			return "", fmt.Errorf("failed converting port to integer for URI %q: %v", uri, err)
		}
		if err := Port(intPort); err != nil {
			return "", fmt.Errorf("failed to validate port for URL %q: %v", uri, err)
		}
	}

	return parsed.Scheme, nil
}
