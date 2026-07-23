package network

import (
	"context"
	"fmt"
	"strconv"

	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/platform"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// Bootstrap creates resources required by the network plugin on the cloud.
func Bootstrap(conf *operv1.Network, client cnoclient.Client) (*bootstrap.BootstrapResult, error) {
	out := &bootstrap.BootstrapResult{}

	infraStatus, err := platform.InfraStatus(client)
	if err != nil {
		return nil, err
	}
	out.Infra = *infraStatus

	if conf.Spec.DefaultNetwork.Type == operv1.NetworkTypeOVNKubernetes {
		o, err := bootstrapOVN(conf, client, infraStatus)
		if err != nil {
			return nil, err
		}
		out.OVN = *o
	}

	out.IPTablesAlerter = iptablesAlerterBootstrap(client.ClientFor("").CRClient())

	if infraStatus.PlatformType == configv1.OpenStackPlatformType {
		cnc, err := cloudNetworkConfigBootstrap(client.ClientFor("").CRClient())
		if err != nil {
			return nil, err
		}
		out.CloudNetworkConfig = cnc
	}

	out.TLSProfile, err = GetTLSProfile(client, infraStatus.HostedControlPlane)
	if err != nil {
		return nil, err
	}

	return out, nil
}

func iptablesAlerterBootstrap(cl crclient.Reader) bootstrap.IPTablesAlerterBootstrapResult {
	result := bootstrap.IPTablesAlerterBootstrapResult{
		Enabled: true,
	}

	cm := &corev1.ConfigMap{}
	if err := cl.Get(context.TODO(), types.NamespacedName{
		Namespace: "openshift-network-operator",
		Name:      "iptables-alerter-config",
	}, cm); err != nil {
		if !apierrors.IsNotFound(err) {
			klog.Warningf("Error fetching iptables-alerter-config configmap: %v", err)
		}
		return result
	}

	enabled := cm.Data["enabled"]
	if enabled == "false" {
		result.Enabled = false
	} else if enabled != "true" {
		klog.Warningf("Ignoring unexpected iptables-alerter-config value enabled=%q", enabled)
	}

	return result
}

func cloudNetworkConfigBootstrap(cl crclient.Reader) (bootstrap.CloudNetworkConfigBootstrapResult, error) {
	result := bootstrap.CloudNetworkConfigBootstrapResult{}

	cm := &corev1.ConfigMap{}
	if err := cl.Get(context.TODO(), types.NamespacedName{
		Namespace: "openshift-network-operator",
		Name:      "cloud-network-config",
	}, cm); err != nil {
		if !apierrors.IsNotFound(err) {
			return result, fmt.Errorf("error fetching cloud-network-config configmap: %w", err)
		}
		return result, nil
	}

	raw, ok := cm.Data["platform-os-max-allowed-address-pairs"]
	if !ok {
		return result, nil
	}

	result.OSMaxAllowedAddressPairs.IsSet = true
	result.OSMaxAllowedAddressPairs.RawValue = raw

	val, err := strconv.Atoi(raw)
	if err != nil {
		klog.Warningf("Error parsing cloud-network-config platform-os-max-allowed-address-pairs=%q: %v", raw, err)
		return result, nil
	}

	result.OSMaxAllowedAddressPairs.Value = val
	return result, nil
}
