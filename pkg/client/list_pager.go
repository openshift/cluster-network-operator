package client

import (
	"context"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/pager"
)

// for use with dynamic client
func ListAllOfSpecifiedType(resourceType schema.GroupVersionResource, ctx context.Context, client Client) ([]*uns.Unstructured, error) {
	list := []*uns.Unstructured{}
	err := pager.New(func(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error) {
		return client.Default().Dynamic().Resource(resourceType).List(ctx, opts)
	}).EachListItem(ctx, metav1.ListOptions{}, func(obj runtime.Object) error {
		list = append(list, obj.(*uns.Unstructured))
		return nil
	})
	return list, err
}

func ListAllNamespaces(ctx context.Context, client Client) ([]*v1.Namespace, error) {
	list := []*v1.Namespace{}
	err := pager.New(func(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error) {
		return client.Default().Kubernetes().CoreV1().Namespaces().List(ctx, opts)
	}).EachListItem(ctx, metav1.ListOptions{}, func(obj runtime.Object) error {
		list = append(list, obj.(*v1.Namespace))
		return nil
	})
	return list, err
}

func ListAllPodsWithAnnotationKey(ctx context.Context, client Client, annotationKey string) ([]*v1.Pod, error) {
	list := []*v1.Pod{}
	err := pager.New(func(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error) {
		return client.Default().Kubernetes().CoreV1().Pods("").List(ctx, opts)
	}).EachListItem(ctx, metav1.ListOptions{}, func(obj runtime.Object) error {
		pod := obj.(*v1.Pod)
		if _, ok := pod.Annotations[annotationKey]; ok {
			list = append(list, pod)
		}
		return nil
	})
	return list, err
}
