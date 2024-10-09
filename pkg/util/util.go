package util

import (
	"context"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
)

const OVN_INTERCONNECT_CONFIGMAP_NAME = "ovn-interconnect-configuration"
const OVN_NAMESPACE = "openshift-ovn-kubernetes"
const OVN_MASTER = "ovnkube-master"
const OVN_CONTROL_PLANE = "ovnkube-control-plane"
const OVN_NODE = "ovnkube-node"
const OVN_CONTROLLER = "ovnkube-controller"
const OVN_IPSEC = "ovn-ipsec"                             // 4.13 ipsec daemonset
const OVN_IPSEC_HOST = "ovn-ipsec-host"                   // 4.14 ipsec daemonset
const OVN_IPSEC_CONTAINERIZED = "ovn-ipsec-containerized" // 4.14 ipsec daemonset
const SDN_NAMESPACE = "openshift-sdn"
const MTU_CM_NAMESPACE = "openshift-network-operator"
const MTU_CM_NAME = "mtu"

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
