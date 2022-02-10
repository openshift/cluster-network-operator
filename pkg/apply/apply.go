package apply

import (
	"context"
	"fmt"
	"log"

	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/names"

	"github.com/pkg/errors"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilpointer "k8s.io/utils/pointer"
)

type Object interface {
	metav1.Object
	runtime.Object
}

// ApplyObject submits a server-side apply patch for the given object.
// This causes fields we own to be updated, and fields we don't own to be preserved.
// For more information, see https://kubernetes.io/docs/reference/using-api/server-side-apply/
// The subcontroller, if set, is used to assign field ownership.
func ApplyObject(ctx context.Context, client *cnoclient.ClusterClient, obj Object, subcontroller string) error {
	name := obj.GetName()
	namespace := obj.GetNamespace()

	oks, _, _ := client.Scheme().ObjectKinds(obj)
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

	// determine resource
	rm, err := client.RESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return fmt.Errorf("failed to retrieve resource from Object %s: %v", objDesc, err)
	}

	// If create-only is specified, check to see if exists
	if _, ok := obj.GetAnnotations()[names.CreateOnlyAnnotation]; ok {
		_, err := client.Dynamic().Resource(rm.Resource).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			log.Printf("Object %s has create-only annotation and already exists, skipping apply.", objDesc)
			return nil
		}
		if !apierrors.IsNotFound(err) {
			return err
		}
	}

	fieldManager := "cluster-network-operator"
	if subcontroller != "" {
		fieldManager = fmt.Sprintf("%s/%s", fieldManager, subcontroller)
	}

	// Use server-side apply to merge the desired object with the object on disk
	patchOptions := metav1.PatchOptions{
		// It is considered best-practice for controllers to force
		Force:        utilpointer.Bool(true),
		FieldManager: fieldManager,
	}
	// Send the full object to be applied on the server side.
	data, err := runtime.Encode(unstructured.UnstructuredJSONScheme, obj)
	if err != nil {
		log.Printf("could not encode %s for apply", objDesc)
		return fmt.Errorf("could not encode for patching: %w", err)
	}
	if _, err := client.Dynamic().Resource(rm.Resource).Namespace(namespace).Patch(ctx, name, types.ApplyPatchType, data, patchOptions); err != nil {
		return fmt.Errorf("failed to apply / update %s: %w", objDesc, err)
	}
	log.Printf("Apply / Create of %s was successful", objDesc)
	return nil
}
