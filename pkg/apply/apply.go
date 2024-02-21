package apply

import (
	"context"
	"fmt"
	"log"
	"strings"

	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/names"

	"github.com/pkg/errors"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilpointer "k8s.io/utils/ptr"
)

type Object interface {
	metav1.Object
	runtime.Object
}

// GetClusterName returns the names.ClusterNameAnnotation annotation for the specified object.
// If the annotation does not exist it will return an empty string.
func GetClusterName(obj Object) string {
	return obj.GetAnnotations()[names.ClusterNameAnnotation]
}

// ApplyObject submits a server-side apply patch for the given object.
// This causes fields we own to be updated, and fields we don't own to be preserved.
// For more information, see https://kubernetes.io/docs/reference/using-api/server-side-apply/
// The subcontroller, if set, is used to assign field ownership.
func ApplyObject(ctx context.Context, client cnoclient.Client, obj Object, subcontroller string, subresources ...string) error {
	name := obj.GetName()
	namespace := obj.GetNamespace()
	clusterClient := client.ClientFor(GetClusterName(obj))
	if clusterClient == nil {
		return fmt.Errorf("object %s/%s specifies unknown cluster %s", namespace, name, GetClusterName(obj))
	}

	oks, _, _ := clusterClient.Scheme().ObjectKinds(obj)
	if len(oks) == 0 {
		return errors.Errorf("Object %s/%s has no Kind registered in the Scheme", namespace, name)
	}
	gvk := oks[0]
	if name == "" {
		return errors.Errorf("Object %s has no name", gvk)
	}

	// Dragons: If we're passed a non-Unstructured object (e.g. v1.ConfigMap), it won't have
	// the GVK set necessarily. So, use the retrieved GVK from the schema and add it.
	// This is a no-op for Unstructured objects.
	obj.GetObjectKind().SetGroupVersionKind(gvk)
	// used for logging and errors
	objDesc := fmt.Sprintf("(%s) %s/%s", gvk.String(), namespace, name)
	log.Printf("reconciling %s", objDesc)

	// It isn't allowed to send ManagedFields in a Patch.
	obj.SetManagedFields(nil)

	if _, exists := obj.GetAnnotations()[names.CopyFromAnnotation]; exists {
		var err error
		obj, err = getCopySource(ctx, obj, client)
		if err != nil {
			return fmt.Errorf("failed to retrieve copy-from object: %w", err)
		}
	}

	// determine resource
	rm, err := clusterClient.RESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return fmt.Errorf("failed to retrieve resource from Object %s: %v", objDesc, err)
	}

	// If create-wait is specified, ignore creating the object
	if _, ok := obj.GetAnnotations()[names.CreateWaitAnnotation]; ok {
		log.Printf("Object %s has create-wait annotation, skipping apply.", objDesc)
		return nil
	}

	// If create-only is specified, check to see if exists
	if _, ok := obj.GetAnnotations()[names.CreateOnlyAnnotation]; ok {
		_, err := clusterClient.Dynamic().Resource(rm.Resource).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			log.Printf("Object %s has create-only annotation and already exists, skipping apply.", objDesc)
			return nil
		}
		if !apierrors.IsNotFound(err) {
			return err
		}
	}

	merge := getMergeForUpdate(obj)
	if merge != nil {
		// this object requires some of the existing data merged into the
		// updated object, this is on exceptional cases where the server-side
		// apply is not doing what we want
		obj, err = merge(ctx, clusterClient)
		if err != nil {
			return fmt.Errorf("failed to merge object %s: %w", objDesc, err)
		}
	}

	fieldManager := "cluster-network-operator"
	if subcontroller != "" {
		fieldManager = fmt.Sprintf("%s/%s", fieldManager, subcontroller)
	}

	// Use server-side apply to merge the desired object with the object on disk
	patchOptions := metav1.PatchOptions{
		// It is considered best-practice for controllers to force
		Force:        utilpointer.To(true),
		FieldManager: fieldManager,
	}
	// Send the full object to be applied on the server side.
	data, err := runtime.Encode(unstructured.UnstructuredJSONScheme, obj)
	if err != nil {
		log.Printf("could not encode %s for apply", objDesc)
		return fmt.Errorf("could not encode for patching: %w", err)
	}

	_, err = clusterClient.Dynamic().Resource(rm.Resource).Namespace(namespace).Patch(ctx, name, types.ApplyPatchType, data, patchOptions, subresources...)
	if err != nil {
		return fmt.Errorf("failed to apply / update %s: %w", objDesc, err)
	}

	log.Printf("Apply / Create of %s was successful", objDesc)
	return nil
}

// getCopySource retrieves an object using copy-from annotation from obj.
// Returns an object that has it's readonly fields cleared, the following metadata fields are preserved from obj:
//
//	Name
//	Namespace
//	ClusterName
//	Labels
//	OwnerReferences
//	ManagedFields
//	Finalizers
//
// Annotations are merged, when there is a conflict obj's annotation is used.
func getCopySource(ctx context.Context, obj Object, client cnoclient.Client) (Object, error) {
	anno, exists := obj.GetAnnotations()[names.CopyFromAnnotation]
	if !exists {
		return nil, fmt.Errorf("%s annotation not specified", names.CopyFromAnnotation)
	}

	parts := strings.Split(anno, "/")
	if len(parts) != 3 {
		return nil, fmt.Errorf("'%s' annotation is invalid, expected: ClusterName/Namespace/Name, got: %s", names.CopyFromAnnotation, anno)
	}

	clusterName, namespace, name := parts[0], parts[1], parts[2]

	cli := client.ClientFor(clusterName)
	if cli == nil {
		return nil, fmt.Errorf("cluster %s is unknown", clusterName)
	}

	ret := &unstructured.Unstructured{}
	ret.SetGroupVersionKind(obj.GetObjectKind().GroupVersionKind())

	err := cli.CRClient().Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, ret)
	if err != nil {
		return nil, fmt.Errorf("get failed (%s) %s/%s: %w", obj.GetObjectKind().GroupVersionKind(), namespace, name, err)
	}

	// clear read-only fields
	ret.SetSelfLink("")
	ret.SetUID("")
	ret.SetResourceVersion("")
	ret.SetGeneration(0)
	ret.SetCreationTimestamp(metav1.Time{})

	ret.SetNamespace(obj.GetNamespace())
	ret.SetName(obj.GetName())
	ret.SetLabels(obj.GetLabels())
	ret.SetOwnerReferences(obj.GetOwnerReferences())
	ret.SetManagedFields(obj.GetManagedFields())
	ret.SetFinalizers(obj.GetFinalizers())

	annotations := ret.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	for k, v := range obj.GetAnnotations() {
		annotations[k] = v
	}
	ret.SetAnnotations(annotations)

	return ret, nil
}
