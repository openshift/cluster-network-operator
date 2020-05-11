package apply

import (
	"context"
	"fmt"
	"log"

	"github.com/pkg/errors"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	kscheme "k8s.io/client-go/kubernetes/scheme"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
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
	err := client.Get(ctx, types.NamespacedName{Name: obj.GetName(), Namespace: obj.GetNamespace()}, existing)

	if err != nil && apierrors.IsNotFound(err) {
		log.Printf("does not exist, creating %s", objDesc)
		err := client.Create(ctx, obj)
		if err != nil {
			return errors.Wrapf(err, "could not create %s", objDesc)
		}
		log.Printf("successfully created %s", objDesc)
		return nil
	}
	if err != nil {
		return errors.Wrapf(err, "could not retrieve existing %s", objDesc)
	}

	if obj.GetKind() == "Namespace" {
		ns := &v1.Namespace{}
		if err := kscheme.Scheme.Convert(existing, ns, nil); err != nil {
			return errors.Wrapf(err, "could not convert namespace %s", objDesc)
		}
		if ns.Status.Phase == v1.NamespaceTerminating {
			log.Printf("finalize Namespace %s", objDesc)
			ns.Spec.Finalizers = []v1.FinalizerName{}
			// Get a config to for client-go
			cfg, err := config.GetConfig()
			if err != nil {
				return errors.Wrapf(err, "could not create rest config")
			}
			clientset, err := kubernetes.NewForConfig(cfg)
			if err != nil {
				return errors.Wrapf(err, "could not create clientset")
			}
			if _, err := clientset.CoreV1().Namespaces().Finalize(ns); err != nil {
				return errors.Wrapf(err, "could not finalize namespace %s", objDesc)
			}
			return nil
		}
	}

	// Merge the desired object with what actually exists
	if err := MergeObjectForUpdate(existing, obj); err != nil {
		return errors.Wrapf(err, "could not merge object %s with existing", objDesc)
	}
	if !equality.Semantic.DeepEqual(existing, obj) {
		if err := client.Update(ctx, obj); err != nil {
			return errors.Wrapf(err, "could not update object %s", objDesc)
		} else {
			log.Printf("update was successful")
		}
	}

	return nil
}
