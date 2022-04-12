package network

import (
	"os"
	"sync"

	configv1 "github.com/openshift/api/config/v1"
)

var (
	enabled   = os.Getenv("HYPERSHIFT")
	name      = os.Getenv("HOSTED_CLUSTER_NAME")
	namespace = os.Getenv("HOSTED_CLUSTER_NAMESPACE")
)

const (
	// ClusterIDLabel (_id) is the common label used to identify clusters in telemeter.
	// For hypershift, it will identify metrics produced by the both the control plane
	// components and guest cluster monitoring stack.
	ClusterIDLabel = "_id"
	// NetworkOperatorStatusTypeProgressing indicates Progressing condition in hostedControlPlane status
	NetworkOperatorStatusTypeProgressing = "network.operator.openshift.io/Progressing"
	// NetworkOperatorStatusTypeDegraded indicates Degraded condition in hostedControlPlane status
	NetworkOperatorStatusTypeDegraded = "network.operator.openshift.io/Degraded"
)

type RelatedObject struct {
	configv1.ObjectReference
	ClusterName string
}

type HyperShiftConfig struct {
	sync.Mutex
	Enabled        bool
	Name           string
	Namespace      string
	RelatedObjects []RelatedObject
}

func NewHyperShiftConfig() *HyperShiftConfig {
	return &HyperShiftConfig{
		Enabled:   hyperShiftEnabled(),
		Name:      name,
		Namespace: namespace,
	}
}

func hyperShiftEnabled() bool {
	return enabled == "true"
}

func (hc *HyperShiftConfig) SetRelatedObjects(relatedObjects []RelatedObject) {
	hc.Lock()
	defer hc.Unlock()
	hc.RelatedObjects = relatedObjects
}
