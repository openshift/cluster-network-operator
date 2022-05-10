package statusmanager

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/openshift/cluster-network-operator/pkg/apply"

	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
)

// Some silly types so that we can actually do testing

type DaemonSetLister interface {
	List(selector labels.Selector) (ret []*appsv1.DaemonSet, err error)
}

type DeploymentLister interface {
	List(selector labels.Selector) (ret []*appsv1.Deployment, err error)
}

type StatefulSetLister interface {
	List(selector labels.Selector) (ret []*appsv1.StatefulSet, err error)
}

// just the scaffolding needed to be able to send patched annotations
// including a nil, which deletes an annotation
type patchAnnotations struct {
	Metadata md `json:"metadata"`
}
type md struct {
	Annotations map[string]interface{} `json:"annotations"`
}

func (status *StatusManager) setAnnotation(ctx context.Context, obj crclient.Object, key string, value *string) error {
	anno := obj.GetAnnotations()
	existing, set := anno[key]
	if value != nil && set && existing == *value {
		return nil
	}
	if !set && value == nil {
		return nil
	}
	patch := &patchAnnotations{
		Metadata: md{
			Annotations: map[string]interface{}{
				key: value,
			},
		},
	}
	patchData, err := json.Marshal(&patch)
	if err != nil {
		return fmt.Errorf("failed to create patch: %v", err)
	}

	return status.client.ClientFor(apply.GetClusterName(obj)).CRClient().Patch(ctx, obj, crclient.RawPatch(types.MergePatchType, patchData))
}
