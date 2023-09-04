package platform

import (
	"fmt"
	"os"
	"strings"
	"sync"

	configv1 "github.com/openshift/api/config/v1"
)

const HyperShiftInternalRouteLabel = "hypershift.openshift.io/internal-route"
const HostedClusterLocalProxy = "socks5://127.0.0.1:8090"

func init() {
	for _, label := range strings.Split(routeLabelsRaw, ",") {
		if label == "" {
			continue
		}
		key, value, found := strings.Cut(label, "=")
		if !found {
			panic(fmt.Sprintf("label %q can not be parsed as key value pair", label))
		}
		routeLabels[key] = value
	}
}

var (
	enabled           = os.Getenv("HYPERSHIFT")
	name              = os.Getenv("HOSTED_CLUSTER_NAME")
	namespace         = os.Getenv("HOSTED_CLUSTER_NAMESPACE")
	routeHost         = os.Getenv("OVN_SBDB_ROUTE_HOST")
	routeLabels       = map[string]string{}
	routeLabelsRaw    = os.Getenv("OVN_SBDB_ROUTE_LABELS")
	runAsUser         = os.Getenv("RUN_AS_USER")
	releaseImage      = os.Getenv("OPENSHIFT_RELEASE_IMAGE")
	controlPlaneImage = os.Getenv("OVN_CONTROL_PLANE_IMAGE")
)

const (
	// ClusterIDLabel (_id) is the common label used to identify clusters in telemeter.
	// For hypershift, it will identify metrics produced by the both the control plane
	// components and guest cluster monitoring stack.
	ClusterIDLabel = "_id"
	// HyperShiftConditionTypePrefix is a cluster network operator condition type prefix in hostedControlPlane status
	HyperShiftConditionTypePrefix = "network.operator.openshift.io/"
)

type RelatedObject struct {
	configv1.ObjectReference
	ClusterName string
}

type HyperShiftConfig struct {
	sync.Mutex
	Enabled            bool
	Name               string
	Namespace          string
	OVNSbDbRouteHost   string
	RunAsUser          string
	OVNSbDbRouteLabels map[string]string
	RelatedObjects     []RelatedObject
	ReleaseImage       string
	ControlPlaneImage  string
}

func NewHyperShiftConfig() *HyperShiftConfig {
	return &HyperShiftConfig{
		Enabled:            hyperShiftEnabled(),
		Name:               name,
		Namespace:          namespace,
		RunAsUser:          runAsUser,
		OVNSbDbRouteHost:   routeHost,
		OVNSbDbRouteLabels: routeLabels,
		ReleaseImage:       releaseImage,
		ControlPlaneImage:  controlPlaneImage,
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
