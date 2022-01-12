package apply

import (
	"context"
	"fmt"
	"log"

	"github.com/openshift/cluster-network-operator/pkg/names"

	"github.com/pkg/errors"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// ApplyObject applies the desired object against the apiserver,
// merging it with any existing objects if already present.
func ApplyObject(ctx context.Context, client k8sclient.Client, obj *uns.Unstructured) error {
	name := obj.GetName()
	namespace := obj.GetNamespace()
	if name == "" {
		return errors.Errorf("Object %s has no name", obj.GroupVersionKind().String())
	}
	gvk := obj.GroupVersionKind()
	// used for logging and errors
	objDesc := fmt.Sprintf("(%s) %s/%s", gvk.String(), namespace, name)
	log.Printf("reconciling %s", objDesc)

	if err := IsObjectSupported(obj); err != nil {
		return errors.Wrapf(err, "object %s unsupported", objDesc)
	}

	// Get existing
	existing := &uns.Unstructured{}
	existing.SetGroupVersionKind(gvk)
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		err := client.Get(ctx, types.NamespacedName{Name: obj.GetName(), Namespace: obj.GetNamespace()}, existing)

		if err != nil && apierrors.IsNotFound(err) {
			log.Printf("does not exist, creating %s", objDesc)
			err := client.Create(ctx, obj)
			if err != nil {
				log.Printf("create of %s was unsucessful", objDesc)
				return err
			}
			log.Printf("successfully created %s", objDesc)
			return nil
		}
		if err != nil {
			log.Printf("could not retrieve %s", objDesc)
			return err
		}

		// object exists - for create-only objects, stop here
		if anno := existing.GetAnnotations()[names.CreateOnlyAnnotation]; anno == "true" {
			return nil
		}

		// Merge the desired object with what actually exists
		if err := MergeObjectForUpdate(existing, obj); err != nil {
			log.Printf("could not merge %s with existing", objDesc)
			return err
		}
		if !equality.Semantic.DeepEqual(existing, obj) {
			if err := client.Update(ctx, obj); err != nil {
				log.Printf("update of %s was unsuccessful", objDesc)
				return err
			} else {
				log.Printf("update was successful")
			}
		}
		return nil
	})

	if err != nil {
		return errors.Wrapf(err, "ApplyObject of %s was unsuccessful", objDesc)
	}
	return nil
}
