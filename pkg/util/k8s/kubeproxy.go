package k8s

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/ghodss/yaml"
	operv1 "github.com/openshift/api/operator/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilnet "k8s.io/apimachinery/pkg/util/net"
	kubeproxyconfig "k8s.io/kube-proxy/config/v1alpha1"
)

// MergeKubeProxyArguments merges a set of default kube-proxy command-line arguments with
// a set of overrides, keeping only the last-specified copy of each argument.
func MergeKubeProxyArguments(defaults, overrides map[string]operv1.ProxyArgumentList) map[string]operv1.ProxyArgumentList {
	args := map[string]operv1.ProxyArgumentList{}
	for key, val := range defaults {
		if len(val) > 0 {
			args[key] = []string{val[len(val)-1]}
		}
	}
	for key, val := range overrides {
		if len(val) > 0 {
			args[key] = []string{val[len(val)-1]}
		}
	}
	return args
}

// GenerateKubeProxyConfiguration takes a set of defaults and a set of overrides in the
// form of kube-proxy command-line arguments, and returns a YAML kube-proxy config file.
func GenerateKubeProxyConfiguration(args map[string]operv1.ProxyArgumentList) (string, error) {
	// We use MergeKubeProxyArguments here to force a copy
	ka := &kpcArgs{args: MergeKubeProxyArguments(args, nil)}

	kpc := &kubeproxyconfig.KubeProxyConfiguration{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "kubeproxy.config.k8s.io/v1alpha1",
			Kind:       "KubeProxyConfiguration",
		},
	}

	kpc.BindAddress = ka.getAddress("bind-address")
	kpc.HealthzBindAddress = ka.getAddressAndPort("healthz-bind-address", "healthz-port", "10256")
	kpc.MetricsBindAddress = ka.getAddressAndPort("metrics-bind-address", "metrics-port", "10249")

	kpc.ClusterCIDR = ka.getCIDR("cluster-cidr")

	kpc.IPTables.MasqueradeBit = ka.getOptInt32("iptables-masquerade-bit")
	kpc.IPTables.MasqueradeAll = ka.getBool("masquerade-all")
	kpc.IPTables.SyncPeriod.Duration = ka.getDuration("iptables-sync-period")
	kpc.IPTables.MinSyncPeriod.Duration = ka.getDuration("iptables-min-sync-period")

	kpc.IPVS.SyncPeriod.Duration = ka.getDuration("ipvs-sync-period")
	kpc.IPVS.MinSyncPeriod.Duration = ka.getDuration("ipvs-min-sync-period")
	kpc.IPVS.Scheduler = ka.getString("ipvs-scheduler")
	kpc.IPVS.ExcludeCIDRs = ka.getCIDRList("ipvs-exclude-cidrs")

	kpc.Mode = kubeproxyconfig.ProxyMode(ka.getString("proxy-mode"))

	kpc.PortRange = ka.getPortRange("proxy-port-range")

	kpc.UDPIdleTimeout.Duration = ka.getDuration("udp-timeout")

	kpc.Conntrack.MaxPerCore = ka.getOptInt32("conntrack-max-per-core")
	kpc.Conntrack.Min = ka.getOptInt32("conntrack-min")
	if duration := ka.getDuration("conntrack-tcp-timeout-established"); duration != 0 {
		kpc.Conntrack.TCPEstablishedTimeout = &metav1.Duration{Duration: duration}
	}
	if duration := ka.getDuration("conntrack-tcp-timeout-close-wait"); duration != 0 {
		kpc.Conntrack.TCPCloseWaitTimeout = &metav1.Duration{Duration: duration}
	}

	kpc.ConfigSyncPeriod.Duration = ka.getDuration("config-sync-period")

	kpc.NodePortAddresses = ka.getCIDRList("node-port-addresses")

	if err := ka.getError(); err != nil {
		return "", err
	}

	buf, err := yaml.Marshal(kpc)
	return string(buf), err
}

// kpcArgs is a helper to build the KubeProxyConfiguration. In particular, it
// keeps track of which arguments have been used, and whether an error occurred
type kpcArgs struct {
	args map[string]operv1.ProxyArgumentList
	errs []error
}

func (ka *kpcArgs) getError() error {
	if len(ka.errs) != 0 {
		return utilerrors.NewAggregate(ka.errs)
	} else if len(ka.args) != 0 {
		unused := ""
		for key := range ka.args {
			if len(unused) > 0 {
				unused += ", "
			}
			unused += key
		}
		return fmt.Errorf("unused arguments: %s", unused)
	} else {
		return nil
	}
}

func (ka *kpcArgs) get(argName string) string {
	val := ka.args[argName]
	if len(val) == 0 {
		return ""
	}
	delete(ka.args, argName)
	return val[0]
}

// getAddressAndPort returns a combined IP address and port
func (ka *kpcArgs) getAddressAndPort(addressKey, portKey, defaultPort string) string {
	address := ka.get(addressKey)
	port := ka.get(portKey)

	if address == "" && port == "" {
		return ""
	}

	if address != "" {
		if net.ParseIP(address) == nil {
			ka.errs = append(ka.errs, fmt.Errorf("invalid %s %q (not an IP address)", addressKey, address))
			return ""
		}
	} else {
		// default to 0.0.0.0
		address = "0.0.0.0"
	}

	if port != "" {
		if _, err := strconv.ParseUint(port, 10, 16); err != nil {
			ka.errs = append(ka.errs, fmt.Errorf("invalid %s %q (%v)", portKey, port, err))
			return ""
		}
	} else {
		port = defaultPort
	}

	return net.JoinHostPort(address, port)
}

// getString returns an uninterpreted string
func (ka *kpcArgs) getString(key string) string {
	return ka.get(key)
}

// getAddress returns an IP address as a string
func (ka *kpcArgs) getAddress(key string) string {
	value := ka.get(key)
	if value == "" {
		return ""
	}
	if net.ParseIP(value) == nil {
		ka.errs = append(ka.errs, fmt.Errorf("invalid %s %q (not an IP address)", key, value))
		return ""
	}
	return value
}

// getCIDR returns a CIDR block as a string
func (ka *kpcArgs) getCIDR(key string) string {
	value := ka.get(key)
	if value == "" {
		return ""
	}
	if _, _, err := net.ParseCIDR(value); err != nil {
		ka.errs = append(ka.errs, fmt.Errorf("invalid %s %q (%v)", key, value, err))
		return ""
	}
	return value
}

// getCIDRList parses a comma-separate list of CIDR blocks and returns an array of strings
func (ka *kpcArgs) getCIDRList(key string) []string {
	value := ka.get(key)
	if value == "" {
		return nil
	}
	values := strings.Split(value, ",")
	for _, v := range values {
		if _, _, err := net.ParseCIDR(v); err != nil {
			ka.errs = append(ka.errs, fmt.Errorf("invalid %s %q (%v)", key, value, err))
			return nil
		}
	}
	return values
}

// getOptInt32 returns an optional int32
func (ka *kpcArgs) getOptInt32(key string) *int32 {
	value := ka.get(key)
	if value == "" {
		return nil
	}
	intval, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		ka.errs = append(ka.errs, fmt.Errorf("invalid %s %q (%v)", key, value, err))
		return nil
	}
	int32val := int32(intval)
	return &int32val
}

// getBool returns a boolean
func (ka *kpcArgs) getBool(key string) bool {
	value := ka.get(key)
	if value == "" {
		return false
	}
	bval, err := strconv.ParseBool(value)
	if err != nil {
		ka.errs = append(ka.errs, fmt.Errorf("invalid %s %q (%v)", key, value, err))
		return false
	}
	return bval
}

// getDuration returns a time.Duration
func (ka *kpcArgs) getDuration(key string) time.Duration {
	value := ka.get(key)
	if value == "" {
		return 0
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		ka.errs = append(ka.errs, fmt.Errorf("invalid %s %q (%v)", key, value, err))
		return 0
	}
	return duration
}

// getPortRange returns a "port range"
func (ka *kpcArgs) getPortRange(key string) string {
	value := ka.get(key)
	if value == "" {
		return ""
	}
	if _, err := utilnet.ParsePortRange(value); err != nil {
		ka.errs = append(ka.errs, fmt.Errorf("invalid %s %q (%v)", key, value, err))
		return ""
	}
	return value
}
