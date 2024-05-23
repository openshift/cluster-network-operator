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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

var (
	MasterRoleMachineConfigLabel = map[string]string{"machineconfiguration.openshift.io/role": "master"}
	WorkerRoleMachineConfigLabel = map[string]string{"machineconfiguration.openshift.io/role": "worker"}
	// When user deploys their own machine config for installing and configuring specific version of libreswan, then
	// corresponding master and worker role machine configs annotation must have `user-ipsec-machine-config: true`.
	// When CNO finds machine configs with the annotation, then it skips rendering its own IPsec machine configs
	// and reuse already deployed user machine configs for the ovn-ipsec-host daemonset.
	UserDefinedIPsecMachineConfigAnnotation = map[string]string{"user-ipsec-machine-config": "true"}
)

// isNetworkNodeIdentityEnabled determines if network node identity should be enabled.
// It checks the `enabled` key in the network-node-identity/openshift-network-operator configmap.
// If the configmap doesn't exist, it returns true (the feature is enabled by default).
func isNetworkNodeIdentityEnabled(client cnoclient.Client, infra *bootstrap.InfraStatus) (bool, error) {
	if infra.ControlPlaneTopology == configv1.ExternalTopologyMode &&
		infra.PlatformType == configv1.IBMCloudPlatformType {
		// In environments with external control plane topology, the API server is deployed out of cluster.
		// This means that CNO cannot easily predict how to deploy and enforce the node identity webhook.
		// IBMCloud uses an external control plane topology with Calico as the CNI for both HyperShift based ROKS
		// deployments and IBM ROKS Toolkit based ROKS deployments.
		klog.Infof("Network node identity is disabled on %s platorm", configv1.IBMCloudPlatformType)
		return false, nil
	}

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

	netIDEnabled, err := isNetworkNodeIdentityEnabled(client, res)
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

	masterIPsecMachineConfigs, err := findIPsecMachineConfigsWithLabel(client, MasterRoleMachineConfigLabel)
	if err != nil {
		return nil, fmt.Errorf("failed to get ipsec machine configs for master: %v", err)
	}
	res.MasterIPsecMachineConfigs = masterIPsecMachineConfigs

	workerIPsecMachineConfigs, err := findIPsecMachineConfigsWithLabel(client, WorkerRoleMachineConfigLabel)
	if err != nil {
		return nil, fmt.Errorf("failed to get ipsec machine configs for worker: %v", err)
	}
	res.WorkerIPsecMachineConfigs = workerIPsecMachineConfigs

	if res.MasterIPsecMachineConfigs != nil {
		mcpMasterStatuses, err := getMachineConfigPoolStatuses(context.TODO(), client, MasterRoleMachineConfigLabel)
		if err != nil {
			return nil, fmt.Errorf("failed to get machine config pools for master role: %v", err)
		}
		res.MasterMCPStatuses = mcpMasterStatuses
	}

	if res.WorkerIPsecMachineConfigs != nil {
		mcpWorkerStatuses, err := getMachineConfigPoolStatuses(context.TODO(), client, WorkerRoleMachineConfigLabel)
		if err != nil {
			return nil, fmt.Errorf("failed to get machine config pools for worker role: %v", err)
		}
		res.WorkerMCPStatuses = mcpWorkerStatuses
	}

	machineConfigClusterOperatorReady, err := isMachineConfigClusterOperatorReady(client)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("failed to get machine config cluster operator: %v", err)
	}
	res.MachineConfigClusterOperatorReady = machineConfigClusterOperatorReady

	return res, nil
}

func findIPsecMachineConfigsWithLabel(client cnoclient.Client, mcLabel labels.Set) ([]*mcfgv1.MachineConfig, error) {
	machineConfigs := &mcfgv1.MachineConfigList{}
	err := client.Default().CRClient().List(context.TODO(), machineConfigs, &crclient.ListOptions{LabelSelector: mcLabel.AsSelector()})
	if err != nil {
		return nil, err
	}
	var ipsecMachineConfigs []*mcfgv1.MachineConfig
	for i, machineConfig := range machineConfigs.Items {
		if sets.New(machineConfig.Spec.Extensions...).Has("ipsec") ||
			IsUserDefinedIPsecMachineConfig(&machineConfigs.Items[i]) {
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

func getMachineConfigPoolStatuses(ctx context.Context, client cnoclient.Client, mcLabel labels.Set) ([]mcfgv1.MachineConfigPoolStatus, error) {
	mcpList := &mcfgv1.MachineConfigPoolList{}
	if err := client.Default().CRClient().List(ctx, mcpList); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
	}
	var mcpStatuses []mcfgv1.MachineConfigPoolStatus
	for _, mcp := range mcpList.Items {
		mcSelector, err := metav1.LabelSelectorAsSelector(mcp.Spec.MachineConfigSelector)
		if err != nil {
			return nil, fmt.Errorf("invalid machine config label selector in %s pool", mcp.Name)
		}
		if mcSelector.Matches(mcLabel) {
			mcpStatuses = append(mcpStatuses, mcp.Status)
		}
	}
	return mcpStatuses, nil
}

// IsUserDefinedIPsecMachineConfig return true if machine config's annotation is set with
// `user-ipsec-machine-config: true`, otherwise returns false.
func IsUserDefinedIPsecMachineConfig(machineConfig *mcfgv1.MachineConfig) bool {
	if machineConfig == nil {
		return false
	}
	isSubset := func(mcAnnotations, ipsecAnnotation map[string]string) bool {
		for ipsecKey, ipsecValue := range ipsecAnnotation {
			if mcAnnotationValue, ok := mcAnnotations[ipsecKey]; !ok || mcAnnotationValue != ipsecValue {
				return false
			}
		}
		return true
	}
	return isSubset(machineConfig.Annotations, UserDefinedIPsecMachineConfigAnnotation)
}
