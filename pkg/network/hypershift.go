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
