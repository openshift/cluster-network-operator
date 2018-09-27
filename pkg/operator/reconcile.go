package operator

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"

	"github.com/operator-framework/operator-sdk/pkg/k8sclient"
	"github.com/operator-framework/operator-sdk/pkg/util/k8sutil"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/kubernetes/staging/src/k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ReconcileObject takes a rendered desired object and ensures it exists on the apiserver
func (h *Handler) ReconcileObject(ctx context.Context, object *uns.Unstructured) error {
	name, namespace, err := k8sutil.GetNameAndNamespace(object)
	if err != nil {
		return err
	}
	gvk := object.GetObjectKind().GroupVersionKind()
	// used for logging
	objDesc := fmt.Sprintf("(%s) %s/%s", gvk.String(), namespace, name)

	logrus.Info("reconciling", objDesc)

	apiVersion, kind := gvk.ToAPIVersionAndKind()
	resourceClient, _, err := k8sclient.GetResourceClient(apiVersion, kind, namespace)
	if err != nil {
		return errors.Wrapf(err, "failed to get resource client for %s", gvk.String())
	}

	// Try and retrieve the existing oject from the apiserver
	current, err := resourceClient.Get(name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		logrus.Info("object does not exist, creating", objDesc)
		_, err := resourceClient.Create(object)
		if err != nil {
			return errors.Wrapf(err, "failed to create %s", objDesc)
		}
		return nil
	}
	if err != nil {
		return errors.Wrapf(err, "failed to get %s", objDesc)
	}

	// Update the desired object so we can actually commit it to the apiserver
	err = MergeObjectForUpdate(current, object)
	if err != nil {
		return errors.Wrapf(err, "failed to merge %s", objDesc)
	}

	logrus.Info("updating", objDesc)
	updated, err := resourceClient.Update(object)
	if err != nil {
		return errors.Wrapf(err, "failed to update %s", objDesc)
	}
	if updated.GetResourceVersion() != current.GetResourceVersion() {
		logrus.Info("update successful")
	} else {
		logrus.Infof("update was noop")
	}

	return nil
}

// MergeObjectForUpdate prepares a "desired" object to be updated.
// Some objects, such as Deployments, Services, and Daemonsets require
// some semantic-aware updates
func MergeObjectForUpdate(current, updated *uns.Unstructured) error {
	updated.SetResourceVersion(current.GetResourceVersion())

	// todo: add service, daemonset
	if err := MergeDeploymentForUpdate(current, updated); err != nil {
		return err
	}

	if err := MergeServiceForUpdate(current, updated); err != nil {
		return err
	}
	return nil
}

const (
	deploymentRevisionAnnotation = "deployment.kubernetes.io/revision"
)

// MergeDeploymentForUpdate updates Deployment objects.
// The existing revision annotation needs to take precedence
func MergeDeploymentForUpdate(current, updated *uns.Unstructured) error {
	if (updated.GetAPIVersion() == "apps/v1" || updated.GetAPIVersion() == "v1beta2") && updated.GetKind() == "Deployment" {
		updatedAnnotations := updated.GetAnnotations()
		if updatedAnnotations == nil {
			updatedAnnotations = map[string]string{}
		}
		curAnnotations := current.GetAnnotations()

		if curAnnotations != nil {
			updatedAnnotations[deploymentRevisionAnnotation] = curAnnotations[deploymentRevisionAnnotation]
			updated.SetAnnotations(updatedAnnotations)
		}
	}

	return nil
}

// MergeServiceForUpdate ensures the clusterip is never written to
func MergeServiceForUpdate(current, updated *uns.Unstructured) error {
	if updated.GetAPIVersion() == "v1" && updated.GetKind() == "Service" {
		clusterIP, found, err := unstructured.NestedString(current.Object, "spec", "clusterIP")
		if err != nil {
			return err
		}

		if found {
			return unstructured.SetNestedField(updated.Object, clusterIP, "spec", "clusterIP")
		}
	}

	return nil
}
