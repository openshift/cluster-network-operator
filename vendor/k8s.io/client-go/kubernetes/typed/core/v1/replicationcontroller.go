/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Code generated by client-gen. DO NOT EDIT.

package v1

import (
	"context"
	json "encoding/json"
	"fmt"
	"time"

	autoscalingv1 "k8s.io/api/autoscaling/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	corev1 "k8s.io/client-go/applyconfigurations/core/v1"
	scheme "k8s.io/client-go/kubernetes/scheme"
	rest "k8s.io/client-go/rest"
	consistencydetector "k8s.io/client-go/util/consistencydetector"
	watchlist "k8s.io/client-go/util/watchlist"
	"k8s.io/klog/v2"
)

// ReplicationControllersGetter has a method to return a ReplicationControllerInterface.
// A group's client should implement this interface.
type ReplicationControllersGetter interface {
	ReplicationControllers(namespace string) ReplicationControllerInterface
}

// ReplicationControllerInterface has methods to work with ReplicationController resources.
type ReplicationControllerInterface interface {
	Create(ctx context.Context, replicationController *v1.ReplicationController, opts metav1.CreateOptions) (*v1.ReplicationController, error)
	Update(ctx context.Context, replicationController *v1.ReplicationController, opts metav1.UpdateOptions) (*v1.ReplicationController, error)
	UpdateStatus(ctx context.Context, replicationController *v1.ReplicationController, opts metav1.UpdateOptions) (*v1.ReplicationController, error)
	Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error
	DeleteCollection(ctx context.Context, opts metav1.DeleteOptions, listOpts metav1.ListOptions) error
	Get(ctx context.Context, name string, opts metav1.GetOptions) (*v1.ReplicationController, error)
	List(ctx context.Context, opts metav1.ListOptions) (*v1.ReplicationControllerList, error)
	Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error)
	Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (result *v1.ReplicationController, err error)
	Apply(ctx context.Context, replicationController *corev1.ReplicationControllerApplyConfiguration, opts metav1.ApplyOptions) (result *v1.ReplicationController, err error)
	ApplyStatus(ctx context.Context, replicationController *corev1.ReplicationControllerApplyConfiguration, opts metav1.ApplyOptions) (result *v1.ReplicationController, err error)
	GetScale(ctx context.Context, replicationControllerName string, options metav1.GetOptions) (*autoscalingv1.Scale, error)
	UpdateScale(ctx context.Context, replicationControllerName string, scale *autoscalingv1.Scale, opts metav1.UpdateOptions) (*autoscalingv1.Scale, error)

	ReplicationControllerExpansion
}

// replicationControllers implements ReplicationControllerInterface
type replicationControllers struct {
	client rest.Interface
	ns     string
}

// newReplicationControllers returns a ReplicationControllers
func newReplicationControllers(c *CoreV1Client, namespace string) *replicationControllers {
	return &replicationControllers{
		client: c.RESTClient(),
		ns:     namespace,
	}
}

// Get takes name of the replicationController, and returns the corresponding replicationController object, and an error if there is any.
func (c *replicationControllers) Get(ctx context.Context, name string, options metav1.GetOptions) (result *v1.ReplicationController, err error) {
	result = &v1.ReplicationController{}
	err = c.client.Get().
		Namespace(c.ns).
		Resource("replicationcontrollers").
		Name(name).
		VersionedParams(&options, scheme.ParameterCodec).
		Do(ctx).
		Into(result)
	return
}

// List takes label and field selectors, and returns the list of ReplicationControllers that match those selectors.
func (c *replicationControllers) List(ctx context.Context, opts metav1.ListOptions) (*v1.ReplicationControllerList, error) {
	if watchListOptions, hasWatchListOptionsPrepared, watchListOptionsErr := watchlist.PrepareWatchListOptionsFromListOptions(opts); watchListOptionsErr != nil {
		klog.Warningf("Failed preparing watchlist options for replicationcontrollers, falling back to the standard LIST semantics, err = %v", watchListOptionsErr)
	} else if hasWatchListOptionsPrepared {
		result, err := c.watchList(ctx, watchListOptions)
		if err == nil {
			consistencydetector.CheckWatchListFromCacheDataConsistencyIfRequested(ctx, "watchlist request for replicationcontrollers", c.list, opts, result)
			return result, nil
		}
		klog.Warningf("The watchlist request for replicationcontrollers ended with an error, falling back to the standard LIST semantics, err = %v", err)
	}
	result, err := c.list(ctx, opts)
	if err == nil {
		consistencydetector.CheckListFromCacheDataConsistencyIfRequested(ctx, "list request for replicationcontrollers", c.list, opts, result)
	}
	return result, err
}

// list takes label and field selectors, and returns the list of ReplicationControllers that match those selectors.
func (c *replicationControllers) list(ctx context.Context, opts metav1.ListOptions) (result *v1.ReplicationControllerList, err error) {
	var timeout time.Duration
	if opts.TimeoutSeconds != nil {
		timeout = time.Duration(*opts.TimeoutSeconds) * time.Second
	}
	result = &v1.ReplicationControllerList{}
	err = c.client.Get().
		Namespace(c.ns).
		Resource("replicationcontrollers").
		VersionedParams(&opts, scheme.ParameterCodec).
		Timeout(timeout).
		Do(ctx).
		Into(result)
	return
}

// watchList establishes a watch stream with the server and returns the list of ReplicationControllers
func (c *replicationControllers) watchList(ctx context.Context, opts metav1.ListOptions) (result *v1.ReplicationControllerList, err error) {
	var timeout time.Duration
	if opts.TimeoutSeconds != nil {
		timeout = time.Duration(*opts.TimeoutSeconds) * time.Second
	}
	result = &v1.ReplicationControllerList{}
	err = c.client.Get().
		Namespace(c.ns).
		Resource("replicationcontrollers").
		VersionedParams(&opts, scheme.ParameterCodec).
		Timeout(timeout).
		WatchList(ctx).
		Into(result)
	return
}

// Watch returns a watch.Interface that watches the requested replicationControllers.
func (c *replicationControllers) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	var timeout time.Duration
	if opts.TimeoutSeconds != nil {
		timeout = time.Duration(*opts.TimeoutSeconds) * time.Second
	}
	opts.Watch = true
	return c.client.Get().
		Namespace(c.ns).
		Resource("replicationcontrollers").
		VersionedParams(&opts, scheme.ParameterCodec).
		Timeout(timeout).
		Watch(ctx)
}

// Create takes the representation of a replicationController and creates it.  Returns the server's representation of the replicationController, and an error, if there is any.
func (c *replicationControllers) Create(ctx context.Context, replicationController *v1.ReplicationController, opts metav1.CreateOptions) (result *v1.ReplicationController, err error) {
	result = &v1.ReplicationController{}
	err = c.client.Post().
		Namespace(c.ns).
		Resource("replicationcontrollers").
		VersionedParams(&opts, scheme.ParameterCodec).
		Body(replicationController).
		Do(ctx).
		Into(result)
	return
}

// Update takes the representation of a replicationController and updates it. Returns the server's representation of the replicationController, and an error, if there is any.
func (c *replicationControllers) Update(ctx context.Context, replicationController *v1.ReplicationController, opts metav1.UpdateOptions) (result *v1.ReplicationController, err error) {
	result = &v1.ReplicationController{}
	err = c.client.Put().
		Namespace(c.ns).
		Resource("replicationcontrollers").
		Name(replicationController.Name).
		VersionedParams(&opts, scheme.ParameterCodec).
		Body(replicationController).
		Do(ctx).
		Into(result)
	return
}

// UpdateStatus was generated because the type contains a Status member.
// Add a +genclient:noStatus comment above the type to avoid generating UpdateStatus().
func (c *replicationControllers) UpdateStatus(ctx context.Context, replicationController *v1.ReplicationController, opts metav1.UpdateOptions) (result *v1.ReplicationController, err error) {
	result = &v1.ReplicationController{}
	err = c.client.Put().
		Namespace(c.ns).
		Resource("replicationcontrollers").
		Name(replicationController.Name).
		SubResource("status").
		VersionedParams(&opts, scheme.ParameterCodec).
		Body(replicationController).
		Do(ctx).
		Into(result)
	return
}

// Delete takes name of the replicationController and deletes it. Returns an error if one occurs.
func (c *replicationControllers) Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error {
	return c.client.Delete().
		Namespace(c.ns).
		Resource("replicationcontrollers").
		Name(name).
		Body(&opts).
		Do(ctx).
		Error()
}

// DeleteCollection deletes a collection of objects.
func (c *replicationControllers) DeleteCollection(ctx context.Context, opts metav1.DeleteOptions, listOpts metav1.ListOptions) error {
	var timeout time.Duration
	if listOpts.TimeoutSeconds != nil {
		timeout = time.Duration(*listOpts.TimeoutSeconds) * time.Second
	}
	return c.client.Delete().
		Namespace(c.ns).
		Resource("replicationcontrollers").
		VersionedParams(&listOpts, scheme.ParameterCodec).
		Timeout(timeout).
		Body(&opts).
		Do(ctx).
		Error()
}

// Patch applies the patch and returns the patched replicationController.
func (c *replicationControllers) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (result *v1.ReplicationController, err error) {
	result = &v1.ReplicationController{}
	err = c.client.Patch(pt).
		Namespace(c.ns).
		Resource("replicationcontrollers").
		Name(name).
		SubResource(subresources...).
		VersionedParams(&opts, scheme.ParameterCodec).
		Body(data).
		Do(ctx).
		Into(result)
	return
}

// Apply takes the given apply declarative configuration, applies it and returns the applied replicationController.
func (c *replicationControllers) Apply(ctx context.Context, replicationController *corev1.ReplicationControllerApplyConfiguration, opts metav1.ApplyOptions) (result *v1.ReplicationController, err error) {
	if replicationController == nil {
		return nil, fmt.Errorf("replicationController provided to Apply must not be nil")
	}
	patchOpts := opts.ToPatchOptions()
	data, err := json.Marshal(replicationController)
	if err != nil {
		return nil, err
	}
	name := replicationController.Name
	if name == nil {
		return nil, fmt.Errorf("replicationController.Name must be provided to Apply")
	}
	result = &v1.ReplicationController{}
	err = c.client.Patch(types.ApplyPatchType).
		Namespace(c.ns).
		Resource("replicationcontrollers").
		Name(*name).
		VersionedParams(&patchOpts, scheme.ParameterCodec).
		Body(data).
		Do(ctx).
		Into(result)
	return
}

// ApplyStatus was generated because the type contains a Status member.
// Add a +genclient:noStatus comment above the type to avoid generating ApplyStatus().
func (c *replicationControllers) ApplyStatus(ctx context.Context, replicationController *corev1.ReplicationControllerApplyConfiguration, opts metav1.ApplyOptions) (result *v1.ReplicationController, err error) {
	if replicationController == nil {
		return nil, fmt.Errorf("replicationController provided to Apply must not be nil")
	}
	patchOpts := opts.ToPatchOptions()
	data, err := json.Marshal(replicationController)
	if err != nil {
		return nil, err
	}

	name := replicationController.Name
	if name == nil {
		return nil, fmt.Errorf("replicationController.Name must be provided to Apply")
	}

	result = &v1.ReplicationController{}
	err = c.client.Patch(types.ApplyPatchType).
		Namespace(c.ns).
		Resource("replicationcontrollers").
		Name(*name).
		SubResource("status").
		VersionedParams(&patchOpts, scheme.ParameterCodec).
		Body(data).
		Do(ctx).
		Into(result)
	return
}

// GetScale takes name of the replicationController, and returns the corresponding autoscalingv1.Scale object, and an error if there is any.
func (c *replicationControllers) GetScale(ctx context.Context, replicationControllerName string, options metav1.GetOptions) (result *autoscalingv1.Scale, err error) {
	result = &autoscalingv1.Scale{}
	err = c.client.Get().
		Namespace(c.ns).
		Resource("replicationcontrollers").
		Name(replicationControllerName).
		SubResource("scale").
		VersionedParams(&options, scheme.ParameterCodec).
		Do(ctx).
		Into(result)
	return
}

// UpdateScale takes the top resource name and the representation of a scale and updates it. Returns the server's representation of the scale, and an error, if there is any.
func (c *replicationControllers) UpdateScale(ctx context.Context, replicationControllerName string, scale *autoscalingv1.Scale, opts metav1.UpdateOptions) (result *autoscalingv1.Scale, err error) {
	result = &autoscalingv1.Scale{}
	err = c.client.Put().
		Namespace(c.ns).
		Resource("replicationcontrollers").
		Name(replicationControllerName).
		SubResource("scale").
		VersionedParams(&opts, scheme.ParameterCodec).
		Body(scale).
		Do(ctx).
		Into(result)
	return
}
