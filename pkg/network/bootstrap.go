package network

import (
	"context"

	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/platform"

	operv1 "github.com/openshift/api/operator/v1"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// Bootstrap creates resources required by SDN on the cloud.
func Bootstrap(conf *operv1.Network, client cnoclient.Client) (*bootstrap.BootstrapResult, error) {
	out := &bootstrap.BootstrapResult{}

	infraStatus, err := platform.InfraStatus(client)
	if err != nil {
		return nil, err
	}
	out.Infra = *infraStatus

	if conf.Spec.DefaultNetwork.Type == operv1.NetworkTypeOVNKubernetes || (conf.Spec.Migration != nil && conf.Spec.Migration.Mode == operv1.LiveNetworkMigrationMode) {
		o, err := bootstrapOVN(conf, client, infraStatus)
		if err != nil {
			return nil, err
		}
		out.OVN = *o
	}

	out.IPTablesAlerter = iptablesAlerterBootstrap(client.ClientFor("").CRClient())

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
