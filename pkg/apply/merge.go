package apply

import (
	"context"
	"fmt"
	"log"

	operv1 "github.com/openshift/api/operator/v1"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

// getCurrentFromUnstructured retrieves the current unstructured object in the
// database from the provided one
func getCurrentFromUnstructured(ctx context.Context, client cnoclient.ClusterClient, updated *uns.Unstructured) (*uns.Unstructured, error) {
	name := updated.GetName()
	namespace := updated.GetNamespace()
	gkv := updated.GroupVersionKind()
	objDesc := fmt.Sprintf("(%s) %s/%s", gkv.String(), namespace, name)

	current := &uns.Unstructured{}
	current.SetGroupVersionKind(gkv)
	err := client.CRClient().Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, current)
	if apierrors.IsNotFound(err) {
		log.Printf("Object %s does not exist, no merge needed", objDesc)
		return nil, nil
	}
	if err != nil {
		err = fmt.Errorf("Object %s could not be retrieved, %w", objDesc, err)
		return nil, err
	}

	return current, nil
}

// mergeOperConfigForUpdate handle server-side apply exceptions for the operator
// config object
func mergeOperConfigForUpdate(current, updated *uns.Unstructured) error {
	if current == nil {
		// if there is no existing object, merge is not needed
		return nil
	}

	// unfortunately disableNetworkDiagnostics it's not a pointer so we can't
	// make it a noop in the server side apply
	// since it's supposed to be changed by the user and not programmatically
	// lets make sure it stays at its current value here
	disableNetworkDiagnostics, found, err := uns.NestedBool(current.Object, "spec", "disableNetworkDiagnostics")
	if err != nil {
		return err
	}
	if found {
		if err := uns.SetNestedField(updated.Object, disableNetworkDiagnostics, "spec", "disableNetworkDiagnostics"); err != nil {
			return err
		}
	}

	return nil
}

// mergerFunction provided by getMergeForUpdate merges the existing object with
// the updated object. Returns the merged updated object as unstructured. Note
// that this merger function is not supposed to make any changes in the database.
type mergerFunction func(ctx context.Context, client cnoclient.ClusterClient) (*uns.Unstructured, error)

// getMergeForUpdate returns a function for the provided object that merges some
// of the existing data into the object as an exception to situations that are
// not handled correctly in the server-side apply
func getMergeForUpdate(obj Object) mergerFunction {
	var doMerge func(current, updated *uns.Unstructured) error

	gvk := obj.GetObjectKind().GroupVersionKind()
	if gvk.Group == operv1.GroupName && gvk.Kind == "Network" {
		doMerge = mergeOperConfigForUpdate
	}

	if doMerge != nil {
		return func(ctx context.Context, client cnoclient.ClusterClient) (*uns.Unstructured, error) {
			updated, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
			if err != nil {
				return nil, err
			}
			updatedUns := &uns.Unstructured{Object: updated}

			currentUns, err := getCurrentFromUnstructured(ctx, client, updatedUns)
			if err != nil {
				return nil, err
			}

			err = doMerge(currentUns, updatedUns)
			if err != nil {
				return nil, err
			}
			return updatedUns, nil
		}
	}

	return nil
}
