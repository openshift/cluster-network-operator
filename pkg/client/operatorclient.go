package client

import (
	"context"
	"fmt"
	v1 "github.com/openshift/client-go/operator/applyconfigurations/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/library-go/pkg/apiserver/jsonpatch"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"

	operatorv1 "github.com/openshift/api/operator/v1"
	operatorclientv1 "github.com/openshift/client-go/operator/clientset/versioned/typed/operator/v1"
	operatorinformerv1 "github.com/openshift/client-go/operator/informers/externalversions/operator/v1"
)

// OperatorHelperClient is used by the generic library-go operator controllers; it provides
// accessors to the operator state.
type OperatorHelperClient struct {
	informer operatorinformerv1.NetworkInformer
	client   operatorclientv1.NetworkInterface
}

// OperatorHelperClient implements the v1helpers OperatorClient interface
var _ v1helpers.OperatorClient = &OperatorHelperClient{}

func (c *OperatorHelperClient) Informer() cache.SharedIndexInformer {
	return c.informer.Informer()
}

func (c *OperatorHelperClient) GetOperatorState() (*operatorv1.OperatorSpec, *operatorv1.OperatorStatus, string, error) {
	instance, err := c.informer.Lister().Get(names.OPERATOR_CONFIG)
	if err != nil {
		return nil, nil, "", err
	}

	return &instance.Spec.OperatorSpec, &instance.Status.OperatorStatus, instance.ResourceVersion, nil
}

func (c *OperatorHelperClient) GetOperatorStateWithQuorum(ctx context.Context) (*operatorv1.OperatorSpec, *operatorv1.OperatorStatus, string, error) {
	instance, err := c.client.Get(ctx, names.OPERATOR_CONFIG, metav1.GetOptions{})
	if err != nil {
		return nil, nil, "", err
	}

	return &instance.Spec.OperatorSpec, &instance.Status.OperatorStatus, instance.ResourceVersion, nil
}

func (c *OperatorHelperClient) GetObjectMeta() (*metav1.ObjectMeta, error) {
	instance, err := c.informer.Lister().Get(names.OPERATOR_CONFIG)
	if err != nil {
		return nil, err
	}
	return &instance.ObjectMeta, nil
}

func (c *OperatorHelperClient) UpdateOperatorSpec(ctx context.Context, resourceVersion string, spec *operatorv1.OperatorSpec) (*operatorv1.OperatorSpec, string, error) {
	original, err := c.informer.Lister().Get(names.OPERATOR_CONFIG)
	if err != nil {
		return nil, "", err
	}
	updated := original.DeepCopy()
	updated.ResourceVersion = resourceVersion
	updated.Spec.OperatorSpec = *spec

	ret, err := c.client.Update(ctx, updated, metav1.UpdateOptions{})
	if err != nil {
		return nil, "", err
	}

	return &ret.Spec.OperatorSpec, ret.ResourceVersion, nil
}

func (c *OperatorHelperClient) UpdateOperatorStatus(ctx context.Context, resourceVersion string, status *operatorv1.OperatorStatus) (*operatorv1.OperatorStatus, error) {
	original, err := c.informer.Lister().Get(names.OPERATOR_CONFIG)
	if err != nil {
		return nil, err
	}
	updated := original.DeepCopy()
	updated.ResourceVersion = resourceVersion
	updated.Status.OperatorStatus = *status

	ret, err := c.client.UpdateStatus(ctx, updated, metav1.UpdateOptions{})
	if err != nil {
		return nil, err
	}

	return &ret.Status.OperatorStatus, nil
}

func (c *OperatorHelperClient) ApplyOperatorSpec(ctx context.Context, fieldManager string, desiredConfiguration *v1.OperatorSpecApplyConfiguration) error {
	if desiredConfiguration == nil {
		return fmt.Errorf("desiredConfiguration must have a value")
	}

	desiredSpec := &v1.NetworkSpecApplyConfiguration{
		OperatorSpecApplyConfiguration: *desiredConfiguration,
	}
	desired := v1.Network(names.CLUSTER_CONFIG)
	desired.WithSpec(desiredSpec)

	_, err := c.client.Apply(ctx, desired, metav1.ApplyOptions{
		Force:        true,
		FieldManager: fieldManager,
	})
	if err != nil {
		return fmt.Errorf("unable to Apply network operator using fieldManager %q: %w", fieldManager, err)
	}

	return nil
}

func (c *OperatorHelperClient) ApplyOperatorStatus(ctx context.Context, fieldManager string, desiredStatus *v1.OperatorStatusApplyConfiguration) error {
	if desiredStatus == nil {
		return fmt.Errorf("desiredStatus must have a value")
	}

	desired := v1.Network(names.CLUSTER_CONFIG)

	_, err := c.client.ApplyStatus(ctx, desired, metav1.ApplyOptions{
		Force:        true,
		FieldManager: fieldManager,
	})
	if err != nil {
		return fmt.Errorf("unable to Apply Status for network operator using fieldManager %q: %w", fieldManager, err)
	}

	return nil
}

func (c *OperatorHelperClient) PatchOperatorStatus(ctx context.Context, jsonPatch *jsonpatch.PatchSet) (err error) {
	jsonPatchBytes, err := jsonPatch.Marshal()
	if err != nil {
		return err
	}
	_, err = c.client.Patch(ctx, names.CLUSTER_CONFIG, types.JSONPatchType, jsonPatchBytes, metav1.PatchOptions{}, "/status")
	return err
}
