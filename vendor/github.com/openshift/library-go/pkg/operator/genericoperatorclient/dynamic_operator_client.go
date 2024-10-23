package genericoperatorclient

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"
	applyoperatorv1 "github.com/openshift/client-go/operator/applyconfigurations/operator/v1"
	"github.com/openshift/library-go/pkg/apiserver/jsonpatch"
	"github.com/openshift/library-go/pkg/operator/v1helpers"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
)

const defaultConfigName = "cluster"

type StaticPodOperatorSpecExtractorFunc func(obj *unstructured.Unstructured, fieldManager string) (*applyoperatorv1.StaticPodOperatorSpecApplyConfiguration, error)
type StaticPodOperatorStatusExtractorFunc func(obj *unstructured.Unstructured, fieldManager string) (*applyoperatorv1.StaticPodOperatorStatusApplyConfiguration, error)
type OperatorSpecExtractorFunc func(obj *unstructured.Unstructured, fieldManager string) (*applyoperatorv1.OperatorSpecApplyConfiguration, error)
type OperatorStatusExtractorFunc func(obj *unstructured.Unstructured, fieldManager string) (*applyoperatorv1.OperatorStatusApplyConfiguration, error)

func newClusterScopedOperatorClient(clock clock.PassiveClock, dynamicClient dynamic.Interface, gvr schema.GroupVersionResource, gvk schema.GroupVersionKind, instanceName string, extractApplySpec StaticPodOperatorSpecExtractorFunc, extractApplyStatus StaticPodOperatorStatusExtractorFunc) (*dynamicOperatorClient, dynamicinformer.DynamicSharedInformerFactory, error) {
	if len(instanceName) < 1 {
		return nil, nil, fmt.Errorf("config name cannot be empty")
	}

	client := dynamicClient.Resource(gvr)

	informers := dynamicinformer.NewDynamicSharedInformerFactory(dynamicClient, 12*time.Hour)
	informer := informers.ForResource(gvr)

	return &dynamicOperatorClient{
		clock:              clock,
		gvk:                gvk,
		informer:           informer,
		client:             client,
		configName:         instanceName,
		extractApplySpec:   extractApplySpec,
		extractApplyStatus: extractApplyStatus,
	}, informers, nil
}

func convertOperatorSpecToStaticPodOperatorSpec(extractApplySpec OperatorSpecExtractorFunc) StaticPodOperatorSpecExtractorFunc {
	return func(obj *unstructured.Unstructured, fieldManager string) (*applyoperatorv1.StaticPodOperatorSpecApplyConfiguration, error) {
		operatorSpec, err := extractApplySpec(obj, fieldManager)
		if err != nil {
			return nil, err
		}
		if operatorSpec == nil {
			return nil, nil
		}
		return &applyoperatorv1.StaticPodOperatorSpecApplyConfiguration{
			OperatorSpecApplyConfiguration: *operatorSpec,
		}, nil
	}
}

func convertOperatorStatusToStaticPodOperatorStatus(extractApplyStatus OperatorStatusExtractorFunc) StaticPodOperatorStatusExtractorFunc {
	return func(obj *unstructured.Unstructured, fieldManager string) (*applyoperatorv1.StaticPodOperatorStatusApplyConfiguration, error) {
		operatorStatus, err := extractApplyStatus(obj, fieldManager)
		if err != nil {
			return nil, err
		}
		if operatorStatus == nil {
			return nil, nil
		}
		return &applyoperatorv1.StaticPodOperatorStatusApplyConfiguration{
			OperatorStatusApplyConfiguration: *operatorStatus,
		}, nil
	}
}

func NewClusterScopedOperatorClient(clock clock.PassiveClock, config *rest.Config, gvr schema.GroupVersionResource, gvk schema.GroupVersionKind, extractApplySpec OperatorSpecExtractorFunc, extractApplyStatus OperatorStatusExtractorFunc) (v1helpers.OperatorClientWithFinalizers, dynamicinformer.DynamicSharedInformerFactory, error) {
	return NewClusterScopedOperatorClientWithConfigName(clock, config, gvr, gvk, defaultConfigName, extractApplySpec, extractApplyStatus)

}

func NewClusterScopedOperatorClientWithConfigName(clock clock.PassiveClock, config *rest.Config, gvr schema.GroupVersionResource, gvk schema.GroupVersionKind, configName string, extractApplySpec OperatorSpecExtractorFunc, extractApplyStatus OperatorStatusExtractorFunc) (v1helpers.OperatorClientWithFinalizers, dynamicinformer.DynamicSharedInformerFactory, error) {
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, nil, err
	}

	return newClusterScopedOperatorClient(clock, dynamicClient, gvr, gvk, configName,
		convertOperatorSpecToStaticPodOperatorSpec(extractApplySpec), convertOperatorStatusToStaticPodOperatorStatus(extractApplyStatus))
}

type dynamicOperatorClient struct {
	// clock is used to allow apply-configuration to choose a fixed, "execute as though time/X", which is needed for stable
	// testing output.
	clock clock.PassiveClock

	gvk        schema.GroupVersionKind
	configName string
	informer   informers.GenericInformer
	client     dynamic.ResourceInterface

	extractApplySpec   StaticPodOperatorSpecExtractorFunc
	extractApplyStatus StaticPodOperatorStatusExtractorFunc
}

func (c dynamicOperatorClient) Informer() cache.SharedIndexInformer {
	return c.informer.Informer()
}

func (c dynamicOperatorClient) GetObjectMeta() (*metav1.ObjectMeta, error) {
	uncastInstance, err := c.informer.Lister().Get(c.configName)
	if err != nil {
		return nil, err
	}
	instance := uncastInstance.(*unstructured.Unstructured)
	return getObjectMetaFromUnstructured(instance.UnstructuredContent())
}

func (c dynamicOperatorClient) GetOperatorState() (*operatorv1.OperatorSpec, *operatorv1.OperatorStatus, string, error) {
	uncastInstance, err := c.informer.Lister().Get(c.configName)
	if err != nil {
		return nil, nil, "", err
	}
	instance := uncastInstance.(*unstructured.Unstructured)

	return getOperatorStateFromInstance(instance)
}

func (c dynamicOperatorClient) GetOperatorStateWithQuorum(ctx context.Context) (*operatorv1.OperatorSpec, *operatorv1.OperatorStatus, string, error) {
	instance, err := c.client.Get(ctx, c.configName, metav1.GetOptions{})
	if err != nil {
		return nil, nil, "", err
	}

	return getOperatorStateFromInstance(instance)
}

func getOperatorStateFromInstance(instance *unstructured.Unstructured) (*operatorv1.OperatorSpec, *operatorv1.OperatorStatus, string, error) {
	spec, err := getOperatorSpecFromUnstructured(instance.UnstructuredContent())
	if err != nil {
		return nil, nil, "", err
	}
	status, err := getOperatorStatusFromUnstructured(instance.UnstructuredContent())
	if err != nil {
		return nil, nil, "", err
	}

	return spec, status, instance.GetResourceVersion(), nil
}

// UpdateOperatorSpec overwrites the operator object spec with the values given
// in operatorv1.OperatorSpec while preserving pre-existing spec fields that have
// no correspondence in operatorv1.OperatorSpec.
func (c dynamicOperatorClient) UpdateOperatorSpec(ctx context.Context, resourceVersion string, spec *operatorv1.OperatorSpec) (*operatorv1.OperatorSpec, string, error) {
	uncastOriginal, err := c.informer.Lister().Get(c.configName)
	if err != nil {
		return nil, "", err
	}
	original := uncastOriginal.(*unstructured.Unstructured)

	copy := original.DeepCopy()
	copy.SetResourceVersion(resourceVersion)
	if err := setOperatorSpecFromUnstructured(copy.UnstructuredContent(), spec); err != nil {
		return nil, "", err
	}

	ret, err := c.client.Update(ctx, copy, metav1.UpdateOptions{})
	if err != nil {
		return nil, "", err
	}
	retSpec, err := getOperatorSpecFromUnstructured(ret.UnstructuredContent())
	if err != nil {
		return nil, "", err
	}

	return retSpec, ret.GetResourceVersion(), nil
}

// UpdateOperatorStatus overwrites the operator object status with the values given
// in operatorv1.OperatorStatus while preserving pre-existing status fields that have
// no correspondence in operatorv1.OperatorStatus.
func (c dynamicOperatorClient) UpdateOperatorStatus(ctx context.Context, resourceVersion string, status *operatorv1.OperatorStatus) (*operatorv1.OperatorStatus, error) {
	uncastOriginal, err := c.informer.Lister().Get(c.configName)
	if err != nil {
		return nil, err
	}
	original := uncastOriginal.(*unstructured.Unstructured)

	copy := original.DeepCopy()
	copy.SetResourceVersion(resourceVersion)
	if err := setOperatorStatusFromUnstructured(copy.UnstructuredContent(), status); err != nil {
		return nil, err
	}

	ret, err := c.client.UpdateStatus(ctx, copy, metav1.UpdateOptions{})
	if err != nil {
		return nil, err
	}
	retStatus, err := getOperatorStatusFromUnstructured(ret.UnstructuredContent())
	if err != nil {
		return nil, err
	}

	return retStatus, nil
}

func (c dynamicOperatorClient) ApplyOperatorSpec(ctx context.Context, fieldManager string, desiredConfiguration *applyoperatorv1.OperatorSpecApplyConfiguration) (err error) {
	if desiredConfiguration == nil {
		return fmt.Errorf("desiredConfiguration must have value")
	}
	desiredConfigurationAsStaticPod := applyoperatorv1.StaticPodOperatorSpec()
	desiredConfigurationAsStaticPod.OperatorSpecApplyConfiguration = *desiredConfiguration
	return c.applyOperatorSpec(ctx, fieldManager, desiredConfigurationAsStaticPod)
}

func (c dynamicOperatorClient) applyOperatorSpec(ctx context.Context, fieldManager string, desiredConfiguration *applyoperatorv1.StaticPodOperatorSpecApplyConfiguration) (err error) {
	uncastOriginal, err := c.informer.Lister().Get(c.configName)
	switch {
	case apierrors.IsNotFound(err):
		// do nothing and proceed with the apply
	case err != nil:
		return fmt.Errorf("unable to read existing %q: %w", c.configName, err)
	default:
		original := uncastOriginal.(*unstructured.Unstructured)
		if c.extractApplySpec == nil {
			return fmt.Errorf("extractApplySpec is nil")
		}
		previouslyDesiredConfiguration, err := c.extractApplySpec(original, fieldManager)
		if err != nil {
			return fmt.Errorf("unable to extract status for %q: %w", fieldManager, err)
		}
		if equality.Semantic.DeepEqual(previouslyDesiredConfiguration, desiredConfiguration) {
			// nothing to apply, so return early
			return nil
		}
	}

	desiredSpec, err := runtime.DefaultUnstructuredConverter.ToUnstructured(desiredConfiguration)
	if err != nil {
		return fmt.Errorf("failed to convert to unstructured: %w", err)
	}
	desiredConfigurationAsUnstructured := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"spec": desiredSpec,
		},
	}
	desiredConfigurationAsUnstructured.SetGroupVersionKind(c.gvk)
	desiredConfigurationAsUnstructured.SetName(c.configName)
	_, err = c.client.Apply(ctx, c.configName, desiredConfigurationAsUnstructured, metav1.ApplyOptions{
		Force:        true,
		FieldManager: fieldManager,
	})
	if err != nil {
		return fmt.Errorf("unable to Apply for operator using fieldManager %q: %w", fieldManager, err)
	}

	return nil
}

func (c dynamicOperatorClient) ApplyOperatorStatus(ctx context.Context, fieldManager string, desiredConfiguration *applyoperatorv1.OperatorStatusApplyConfiguration) (err error) {
	if desiredConfiguration == nil {
		return fmt.Errorf("desiredConfiguration must have value")
	}
	desiredConfigurationAsStaticPod := applyoperatorv1.StaticPodOperatorStatus()
	desiredConfigurationAsStaticPod.OperatorStatusApplyConfiguration = *desiredConfiguration
	return c.applyOperatorStatus(ctx, fieldManager, desiredConfigurationAsStaticPod)
}

func (c dynamicOperatorClient) applyOperatorStatus(ctx context.Context, fieldManager string, desiredConfiguration *applyoperatorv1.StaticPodOperatorStatusApplyConfiguration) (err error) {
	if desiredConfiguration != nil {
		for i, curr := range desiredConfiguration.Conditions {
			// panicking so we can quickly find it and fix the source
			if len(ptr.Deref(curr.Type, "")) == 0 {
				panic(fmt.Sprintf(".status.conditions[%d].type is missing", i))
			}
			if len(ptr.Deref(curr.Status, "")) == 0 {
				panic(fmt.Sprintf(".status.conditions[%q].status is missing", *curr.Type))
			}
		}
	}

	uncastOriginal, err := c.informer.Lister().Get(c.configName)
	switch {
	case apierrors.IsNotFound(err):
		// set last transitionTimes and then apply
		// If our cache improperly 404's (the lister wasn't synchronized), then we will improperly reset all the last transition times.
		// This isn't ideal, but we shouldn't hit this case unless a loop isn't waiting for HasSynced.
		v1helpers.SetApplyConditionsLastTransitionTime(c.clock, &desiredConfiguration.Conditions, nil)

	case err != nil:
		return fmt.Errorf("unable to read existing %q: %w", c.configName, err)
	default:
		original := uncastOriginal.(*unstructured.Unstructured)
		if c.extractApplyStatus == nil {
			return fmt.Errorf("extractApplyStatus is nil")
		}
		previouslyDesiredConfiguration, err := c.extractApplyStatus(original, fieldManager)
		if err != nil {
			return fmt.Errorf("unable to extract status for %q: %w", fieldManager, err)
		}

		// set last transitionTimes to properly calculate a difference
		// It is possible for last transition time to shift a couple times until the cache updates to have the condition[*].status match,
		// but it will eventually settle.  The failing sequence looks like
		/*
			1. type=foo, status=false, time=t0.Now
			2. type=foo, status=true, time=t1.Now
			3. rapid update happens and the cache still indicates #1
			4. type=foo, status=true, time=t2.Now (this *should* be t1.Now)
		*/
		// Eventually the cache updates to see at #2 and we stop applying new times.
		// This only becomes pathological if the condition is also flapping, but if that happens the time should also update.
		switch {
		case desiredConfiguration != nil && desiredConfiguration.Conditions != nil && previouslyDesiredConfiguration != nil:
			v1helpers.SetApplyConditionsLastTransitionTime(c.clock, &desiredConfiguration.Conditions, previouslyDesiredConfiguration.Conditions)
		case desiredConfiguration != nil && desiredConfiguration.Conditions != nil && previouslyDesiredConfiguration == nil:
			v1helpers.SetApplyConditionsLastTransitionTime(c.clock, &desiredConfiguration.Conditions, nil)
		}

		// canonicalize so the DeepEqual works consistently
		v1helpers.CanonicalizeStaticPodOperatorStatus(previouslyDesiredConfiguration)
		v1helpers.CanonicalizeStaticPodOperatorStatus(desiredConfiguration)
		previouslyDesiredObj, err := v1helpers.ToStaticPodOperator(previouslyDesiredConfiguration)
		if err != nil {
			return err
		}
		desiredObj, err := v1helpers.ToStaticPodOperator(desiredConfiguration)
		if err != nil {
			return err
		}
		if equality.Semantic.DeepEqual(previouslyDesiredObj, desiredObj) {
			// nothing to apply, so return early
			return nil
		}
	}

	for _, curr := range desiredConfiguration.Conditions {
		if len(ptr.Deref(curr.Reason, "")) == 0 {
			klog.Warningf(".status.conditions[%q].reason is missing; this will eventually be fatal", *curr.Type)
		}
		if len(ptr.Deref(curr.Message, "")) == 0 {
			klog.Warningf(".status.conditions[%q].message is missing; this will eventually be fatal", *curr.Type)
		}
	}

	desiredStatus, err := runtime.DefaultUnstructuredConverter.ToUnstructured(desiredConfiguration)
	if err != nil {
		return fmt.Errorf("failed to convert to unstructured: %w", err)
	}
	desiredConfigurationAsUnstructured := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"status": desiredStatus,
		},
	}
	desiredConfigurationAsUnstructured.SetGroupVersionKind(c.gvk)
	desiredConfigurationAsUnstructured.SetName(c.configName)

	_, err = c.client.ApplyStatus(ctx, c.configName, desiredConfigurationAsUnstructured, metav1.ApplyOptions{
		Force:        true,
		FieldManager: fieldManager,
	})
	if err != nil {
		return fmt.Errorf("unable to ApplyStatus for operator using fieldManager %q: %w", fieldManager, err)
	}

	return nil
}

func (c dynamicOperatorClient) PatchOperatorStatus(ctx context.Context, jsonPatch *jsonpatch.PatchSet) (err error) {
	return c.patchOperatorStatus(ctx, jsonPatch)
}

func (c dynamicOperatorClient) patchOperatorStatus(ctx context.Context, jsonPatch *jsonpatch.PatchSet) (err error) {
	jsonPatchBytes, err := jsonPatch.Marshal()
	if err != nil {
		return err
	}
	_, err = c.client.Patch(ctx, c.configName, types.JSONPatchType, jsonPatchBytes, metav1.PatchOptions{}, "/status")
	return err
}

func (c dynamicOperatorClient) EnsureFinalizer(ctx context.Context, finalizer string) error {
	uncastInstance, err := c.informer.Lister().Get(c.configName)
	if err != nil {
		return err
	}

	instance := uncastInstance.(*unstructured.Unstructured)
	finalizers := instance.GetFinalizers()
	for _, f := range finalizers {
		if f == finalizer {
			return nil
		}
	}

	// Change is needed
	klog.V(4).Infof("Adding finalizer %q", finalizer)
	newFinalizers := append(finalizers, finalizer)
	err = c.saveFinalizers(ctx, instance, newFinalizers)
	if err != nil {
		return err
	}
	klog.V(2).Infof("Added finalizer %q", finalizer)
	return err
}

func (c dynamicOperatorClient) RemoveFinalizer(ctx context.Context, finalizer string) error {
	uncastInstance, err := c.informer.Lister().Get(c.configName)
	if err != nil {
		return err
	}

	instance := uncastInstance.(*unstructured.Unstructured)
	finalizers := instance.GetFinalizers()
	found := false
	newFinalizers := make([]string, 0, len(finalizers))
	for _, f := range finalizers {
		if f == finalizer {
			found = true
			continue
		}
		newFinalizers = append(newFinalizers, f)
	}
	if !found {
		return nil
	}

	klog.V(4).Infof("Removing finalizer %q: %v", finalizer, newFinalizers)
	err = c.saveFinalizers(ctx, instance, newFinalizers)
	if err != nil {
		return err
	}
	klog.V(2).Infof("Removed finalizer %q", finalizer)
	return nil
}

func (c dynamicOperatorClient) saveFinalizers(ctx context.Context, instance *unstructured.Unstructured, finalizers []string) error {
	clone := instance.DeepCopy()
	clone.SetFinalizers(finalizers)
	_, err := c.client.Update(ctx, clone, metav1.UpdateOptions{})
	return err
}

func getObjectMetaFromUnstructured(obj map[string]interface{}) (*metav1.ObjectMeta, error) {
	uncastMeta, exists, err := unstructured.NestedMap(obj, "metadata")
	if !exists {
		return &metav1.ObjectMeta{}, nil
	}
	if err != nil {
		return nil, err
	}

	ret := &metav1.ObjectMeta{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(uncastMeta, ret); err != nil {
		return nil, err
	}
	return ret, nil
}

func getOperatorSpecFromUnstructured(obj map[string]interface{}) (*operatorv1.OperatorSpec, error) {
	uncastSpec, exists, err := unstructured.NestedMap(obj, "spec")
	if !exists {
		return &operatorv1.OperatorSpec{}, nil
	}
	if err != nil {
		return nil, err
	}

	ret := &operatorv1.OperatorSpec{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(uncastSpec, ret); err != nil {
		return nil, err
	}
	return ret, nil
}

func setOperatorSpecFromUnstructured(obj map[string]interface{}, spec *operatorv1.OperatorSpec) error {
	// we cannot simply set the entire map because doing so would stomp unknown fields,
	// like say a static pod operator spec when cast as an operator spec
	newSpec, err := runtime.DefaultUnstructuredConverter.ToUnstructured(spec)
	if err != nil {
		return err
	}

	origSpec, preExistingSpec, err := unstructured.NestedMap(obj, "spec")
	if err != nil {
		return err
	}
	if preExistingSpec {
		flds := topLevelFields(*spec)
		for k, v := range origSpec {
			if !flds[k] {
				if err := unstructured.SetNestedField(newSpec, v, k); err != nil {
					return err
				}
			}
		}
	}
	return unstructured.SetNestedMap(obj, newSpec, "spec")
}

func getOperatorStatusFromUnstructured(obj map[string]interface{}) (*operatorv1.OperatorStatus, error) {
	uncastStatus, exists, err := unstructured.NestedMap(obj, "status")
	if !exists {
		return &operatorv1.OperatorStatus{}, nil
	}
	if err != nil {
		return nil, err
	}

	ret := &operatorv1.OperatorStatus{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(uncastStatus, ret); err != nil {
		return nil, err
	}
	return ret, nil
}

func setOperatorStatusFromUnstructured(obj map[string]interface{}, status *operatorv1.OperatorStatus) error {
	// we cannot simply set the entire map because doing so would stomp unknown fields,
	// like say a static pod operator status when cast as an operator status
	newStatus, err := runtime.DefaultUnstructuredConverter.ToUnstructured(status)
	if err != nil {
		return err
	}

	origStatus, preExistingStatus, err := unstructured.NestedMap(obj, "status")
	if err != nil {
		return err
	}
	if preExistingStatus {
		flds := topLevelFields(*status)
		for k, v := range origStatus {
			if !flds[k] {
				if err := unstructured.SetNestedField(newStatus, v, k); err != nil {
					return err
				}
			}
		}
	}
	return unstructured.SetNestedMap(obj, newStatus, "status")
}

func topLevelFields(obj interface{}) map[string]bool {
	ret := map[string]bool{}
	t := reflect.TypeOf(obj)
	for i := 0; i < t.NumField(); i++ {
		fld := t.Field(i)
		fieldName := fld.Name
		if jsonTag := fld.Tag.Get("json"); jsonTag == "-" {
			continue
		} else if jsonTag != "" {
			// check for possible comma as in "...,omitempty"
			var commaIdx int
			if commaIdx = strings.Index(jsonTag, ","); commaIdx < 0 {
				commaIdx = len(jsonTag)
			}
			fieldName = jsonTag[:commaIdx]
		}
		ret[fieldName] = true
	}
	return ret
}
