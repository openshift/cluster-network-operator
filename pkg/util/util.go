package util

import (
	"context"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
)

const OVNNamespace = "openshift-ovn-kubernetes"
const OVNControlPlane = "ovnkube-control-plane"
const OVNNode = "ovnkube-node"
const MTUConfigMapNamespace = "openshift-network-operator"
const MTUConfigMapName = "mtu"
const OVNNBDBName = "nbdb"

func ReadMTUConfigMap(ctx context.Context, client cnoclient.Client) (int, error) {
	klog.V(4).Infof("Looking for ConfigMap %s/%s", MTUConfigMapNamespace, MTUConfigMapName)
	cm := &corev1.ConfigMap{}
	err := client.Default().CRClient().Get(ctx, types.NamespacedName{Namespace: MTUConfigMapNamespace, Name: MTUConfigMapName}, cm)
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
