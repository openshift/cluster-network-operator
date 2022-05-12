package proxyconfig

import (
	"context"
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/util/proxyconfig"

	corev1 "k8s.io/api/core/v1"
)

// syncProxyStatus computes the current status of proxy and
// updates status of any changes since last sync.
func (r *ReconcileProxyConfig) syncProxyStatus(proxy *configv1.Proxy, infra *configv1.Infrastructure, network *configv1.Network, cluster *corev1.ConfigMap) error {
	var err error
	var noProxy string
	updated := proxy.DeepCopy()

	if isSpecNoProxySet(&proxy.Spec) || isSpecHTTPProxySet(&proxy.Spec) || isSpecHTTPSProxySet(&proxy.Spec) {
		if proxy.Spec.NoProxy == noProxyWildcard {
			noProxy = proxy.Spec.NoProxy
		} else {
			noProxy, err = proxyconfig.MergeUserSystemNoProxy(proxy, infra, network, cluster)
			if err != nil {
				return fmt.Errorf("failed to merge user/system noProxy settings: %v", err)
			}
		}
	}

	updated.Status.ConfigType = proxy.Spec.ConfigType
	if proxy.Spec.ConfigType == configv1.ExplicitProxy {
		updated.Status.HTTPProxy = proxy.Spec.HTTPProxy
		updated.Status.HTTPSProxy = proxy.Spec.HTTPSProxy
		updated.Status.NoProxy = noProxy
	}

	if proxy.Spec.ConfigType == configv1.TransparentProxy {
		updated.Status.HTTPProxy = ""
		updated.Status.HTTPSProxy = ""
		updated.Status.NoProxy = ""
	}

	if !proxyStatusesEqual(proxy.Status, updated.Status) {
		if err := r.client.Status().Update(context.TODO(), updated); err != nil {
			return fmt.Errorf("failed to update proxy status: %v", err)
		}
	}

	return nil
}

// proxyStatusesEqual compares two ProxyStatus values. Returns true if the
// provided values should be considered equal for the purpose of determining
// whether an update is necessary, false otherwise.
func proxyStatusesEqual(a, b configv1.ProxyStatus) bool {
	if a.HTTPProxy != b.HTTPProxy || a.HTTPSProxy != b.HTTPSProxy || a.NoProxy != b.NoProxy {
		return false
	}

	return true
}
