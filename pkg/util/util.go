package util

import (
	"context"
	"fmt"
	"strconv"

	v1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
)

const OVN_INTERCONNECT_CONFIGMAP_NAME = "ovn-interconnect-configuration"
const OVN_NAMESPACE = "openshift-ovn-kubernetes"
const OVN_CONTROL_PLANE = "ovnkube-control-plane"
const OVN_NODE = "ovnkube-node"
const OVN_CONTROLLER = "ovnkube-controller"
const SDN_NAMESPACE = "openshift-sdn"
const MTU_CM_NAMESPACE = "openshift-network-operator"
const MTU_CM_NAME = "mtu"
const OVN_NBDB = "nbdb"
const STANDALONE_MANAGED_CLUSTER_NAMESPACE = "dedicated-admin"      // namespace required for standalone managed clusters (excluding hypershift and ARO)
const STANDALONE_ARO_CLUSTER_NAMESPACE = "openshift-azure-operator" // namespace required for standalone ARO clusters

func GetInterConnectConfigMap(kubeClient kubernetes.Interface) (*corev1.ConfigMap, error) {
	return kubeClient.CoreV1().ConfigMaps(OVN_NAMESPACE).Get(context.TODO(), OVN_INTERCONNECT_CONFIGMAP_NAME, metav1.GetOptions{})
}

func ReadMTUConfigMap(ctx context.Context, client cnoclient.Client) (int, error) {
	klog.V(4).Infof("Looking for ConfigMap %s/%s", MTU_CM_NAMESPACE, MTU_CM_NAME)
	cm := &corev1.ConfigMap{}
	err := client.Default().CRClient().Get(ctx, types.NamespacedName{Namespace: MTU_CM_NAMESPACE, Name: MTU_CM_NAME}, cm)
	if err != nil {
		return 0, err
	}
	mtu, err := strconv.Atoi(cm.Data["mtu"])
	if err != nil || mtu == 0 {
		return 0, fmt.Errorf("format error")
	}

	klog.V(2).Infof("Found mtu %d", mtu)
	return mtu, nil
}

// IsStandaloneManagedCluster returns true if the operator is running in a managed cluster that isn't managed by HyperShift.
// It checks for the existence of the openshift-azure-operator namespace on azure and dedicated-admin namespace otherwise.
func IsStandaloneManagedCluster(ctx context.Context, client cnoclient.Client, platform v1.PlatformType) (bool, error) {
	// TODO(martinkennelly): replace detection of a standalone managed cluster with a metric instead of a namespace when that metric
	// becomes available.
	namespace := STANDALONE_MANAGED_CLUSTER_NAMESPACE
	if platform == v1.AzurePlatformType {
		namespace = STANDALONE_ARO_CLUSTER_NAMESPACE
	}
	err := client.Default().CRClient().Get(ctx, types.NamespacedName{Name: namespace}, &corev1.Namespace{})
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
