package platform

import (
	"context"
	"fmt"
	"os"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/hypershift"
	"github.com/openshift/cluster-network-operator/pkg/names"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	types "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

var cloudProviderConfig = types.NamespacedName{
	Namespace: "openshift-config-managed",
	Name:      "kube-cloud-config",
}

// isNetworkNodeIdentityEnabled determines if network node identity should be enabled.
// It checks the `enabled` key in the network-node-identity/openshift-network-operator configmap.
// If the configmap doesn't exist, it returns true (the feature is enabled by default).
func isNetworkNodeIdentityEnabled(client cnoclient.Client) (bool, error) {
	nodeIdentity := &corev1.ConfigMap{}
	nodeIdentityLookup := types.NamespacedName{Name: "network-node-identity", Namespace: names.APPLIED_NAMESPACE}
	if err := client.ClientFor("").CRClient().Get(context.TODO(), nodeIdentityLookup, nodeIdentity); err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, fmt.Errorf("unable to bootstrap OVN, unable to retrieve cluster config: %s", err)
	}
	enabled, ok := nodeIdentity.Data["enabled"]
	if ok {
		return enabled == "true", nil
	}
	klog.Warningf("key `enabled` not found in the network-node-identity configmap, defaulting to enabled")
	return true, nil
}

func InfraStatus(client cnoclient.Client) (*bootstrap.InfraStatus, error) {
	infraConfig := &configv1.Infrastructure{}
	if err := client.Default().CRClient().Get(context.TODO(), types.NamespacedName{Name: "cluster"}, infraConfig); err != nil {
		return nil, fmt.Errorf("failed to get infrastructure 'cluster': %v", err)
	}

	res := &bootstrap.InfraStatus{
		PlatformType:           infraConfig.Status.PlatformStatus.Type,
		PlatformStatus:         infraConfig.Status.PlatformStatus,
		ControlPlaneTopology:   infraConfig.Status.ControlPlaneTopology,
		InfraName:              infraConfig.Status.InfrastructureName,
		InfrastructureTopology: infraConfig.Status.InfrastructureTopology,
		APIServers:             map[string]bootstrap.APIServer{},
	}

	proxy := &configv1.Proxy{}
	if err := client.Default().CRClient().Get(context.TODO(), types.NamespacedName{Name: "cluster"}, proxy); err != nil {
		return nil, fmt.Errorf("failed to get proxy 'cluster': %w", err)
	}
	res.Proxy = proxy.Status

	// Extract apiserver URLs from the kubeconfig(s) passed to the CNO
	for name, c := range client.Clients() {
		h, p := c.HostPort()
		res.APIServers[name] = bootstrap.APIServer{
			Host: h,
			Port: p,
		}
	}

	// default-local defines how the CNO connects to the APIServer. So, just copy from Default
	res.APIServers[bootstrap.APIServerDefaultLocal] = res.APIServers[bootstrap.APIServerDefault]

	// Allow overriding the "default" apiserver via the environment var APISERVER_OVERRIDE_HOST / _PORT
	// This is used by Hypershift, since the cno connects to a "local" ServiceIP, but rendered manifests
	// that run on a hosted cluster need to talk to the external URL
	if h := os.Getenv(names.EnvApiOverrideHost); h != "" {
		p := os.Getenv(names.EnvApiOverridePort)
		if p == "" {
			p = "443"
		}

		res.APIServers[bootstrap.APIServerDefault] = bootstrap.APIServer{
			Host: h,
			Port: p,
		}
	}

	if res.PlatformType == configv1.AWSPlatformType {
		res.PlatformRegion = infraConfig.Status.PlatformStatus.AWS.Region
	} else if res.PlatformType == configv1.GCPPlatformType {
		res.PlatformRegion = infraConfig.Status.PlatformStatus.GCP.Region
	}

	// AWS and OpenStack specify a CA bundle via a config map; retrieve it.
	if res.PlatformType == configv1.AWSPlatformType || res.PlatformType == configv1.OpenStackPlatformType {
		cm := &corev1.ConfigMap{}
		if err := client.Default().CRClient().Get(context.TODO(), cloudProviderConfig, cm); err != nil {
			if !apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("failed to retrieve ConfigMap %s: %w", cloudProviderConfig, err)
			}
		} else {
			res.KubeCloudConfig = cm.Data
		}
	}

	if hc := hypershift.NewHyperShiftConfig(); hc.Enabled {
		hcp := &unstructured.Unstructured{}
		hcp.SetGroupVersionKind(hypershift.HostedControlPlaneGVK)
		err := client.ClientFor(names.ManagementClusterName).CRClient().Get(context.TODO(), types.NamespacedName{Namespace: hc.Namespace, Name: hc.Name}, hcp)
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve HostedControlPlane %s: %v", types.NamespacedName{Namespace: hc.Namespace, Name: hc.Name}, err)
		}

		res.HostedControlPlane, err = hypershift.ParseHostedControlPlane(hcp)
		if err != nil {
			return nil, fmt.Errorf("failed to parsing HostedControlPlane %s: %v", types.NamespacedName{Namespace: hc.Namespace, Name: hc.Name}, err)
		}
	}

	netIDEnabled, err := isNetworkNodeIdentityEnabled(client)
	if err != nil {
		return nil, fmt.Errorf("failed to determine if network node identity should be enabled: %w", err)
	}
	res.NetworkNodeIdentityEnabled = netIDEnabled

	// Skip retrieving IPsec MachineConfig and MachineConfigPool if it's a hypershift cluster because
	// those object kinds are not supported there.
	if res.HostedControlPlane != nil {
		return res, nil
	}

	// As per instructions given in the following links:
	// https://github.com/openshift/cluster-network-operator/blob/master/docs/enabling_ns_ipsec.md#prerequsits
	// https://docs.openshift.com/container-platform/4.14/networking/ovn_kubernetes_network_provider/configuring-ipsec-ovn.html#nw-ovn-ipsec-north-south-enable_configuring-ipsec-ovn
	// The IPsecMachineConfig in 4.14 is created by user and can be created with any name and also is not managed by network operator, so find it by using the label
	// and looking for the extension.

	masterIPsecMachineConfigs, err := findIPsecMachineConfigsWithLabel(client, "machineconfiguration.openshift.io/role=master")
	if err != nil {
		return nil, fmt.Errorf("failed to get ipsec machine configs for master: %v", err)
	}
	res.MasterIPsecMachineConfigs = masterIPsecMachineConfigs

	workerIPsecMachineConfigs, err := findIPsecMachineConfigsWithLabel(client, "machineconfiguration.openshift.io/role=worker")
	if err != nil {
		return nil, fmt.Errorf("failed to get ipsec machine configs for worker: %v", err)
	}
	res.WorkerIPsecMachineConfigs = workerIPsecMachineConfigs

	if res.MasterIPsecMachineConfigs != nil {
		mcpMaster := &mcfgv1.MachineConfigPool{}
		if err := client.Default().CRClient().Get(context.TODO(), types.NamespacedName{Name: "master"}, mcpMaster); err != nil {
			if !apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("failed to get machine config pool for master: %v", err)
			}
		}
		res.MasterMCPStatus = mcpMaster.Status
	}

	if res.WorkerIPsecMachineConfigs != nil {
		mcpWorker := &mcfgv1.MachineConfigPool{}
		if err := client.Default().CRClient().Get(context.TODO(), types.NamespacedName{Name: "worker"}, mcpWorker); err != nil {
			if !apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("failed to get machine config pool for worker: %v", err)
			}
		}
		res.WorkerMCPStatus = mcpWorker.Status
	}

	machineConfigClusterOperatorReady, err := isMachineConfigClusterOperatorReady(client)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("failed to get machine config cluster operator: %v", err)
	}
	res.MachineConfigClusterOperatorReady = machineConfigClusterOperatorReady

	return res, nil
}

func findIPsecMachineConfigsWithLabel(client cnoclient.Client, selector string) ([]*mcfgv1.MachineConfig, error) {
	lSelector, err := labels.Parse(selector)
	if err != nil {
		return nil, err
	}
	machineConfigs := &mcfgv1.MachineConfigList{}
	err = client.Default().CRClient().List(context.TODO(), machineConfigs, &crclient.ListOptions{LabelSelector: lSelector})
	if err != nil {
		return nil, err
	}
	var ipsecMachineConfigs []*mcfgv1.MachineConfig
	for i, machineConfig := range machineConfigs.Items {
		if sets.New(machineConfig.Spec.Extensions...).Has("ipsec") {
			ipsecMachineConfigs = append(ipsecMachineConfigs, &machineConfigs.Items[i])
		}
	}
	return ipsecMachineConfigs, nil
}

func isMachineConfigClusterOperatorReady(client cnoclient.Client) (bool, error) {
	machineConfigClusterOperator := &configv1.ClusterOperator{}
	if err := client.Default().CRClient().Get(context.TODO(), types.NamespacedName{Name: "machine-config"}, machineConfigClusterOperator); err != nil {
		return false, err
	}
	available, degraded, progressing := false, true, true
	for _, condition := range machineConfigClusterOperator.Status.Conditions {
		isConditionTrue := condition.Status == configv1.ConditionTrue
		switch condition.Type {
		case configv1.OperatorAvailable:
			available = isConditionTrue
		case configv1.OperatorDegraded:
			degraded = isConditionTrue
		case configv1.OperatorProgressing:
			progressing = isConditionTrue
		}
	}
	machineConfigClusterOperatorReady := available && !degraded && !progressing
	return machineConfigClusterOperatorReady, nil
}
