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
// Some objects, such as Deployments and Services require
// some semantic-aware updates
func MergeObjectForUpdate(current, updated *uns.Unstructured) error {
	updated.SetResourceVersion(current.GetResourceVersion())

	if err := MergeDeploymentForUpdate(current, updated); err != nil {
		return err
	}

	if err := MergeServiceForUpdate(current, updated); err != nil {
		return err
	}

	// For all object types, merge annotations.
	// Run this last, in case any of the more specific merge logic has
	// changed "updated"
	mergeAnnotations(current, updated)
	mergeLabels(current, updated)

	return nil
}

const (
	deploymentRevisionAnnotation = "deployment.kubernetes.io/revision"
)

// MergeDeploymentForUpdate updates Deployment objects.
// We merge annotations, keeping ours except the Deployment Revision annotation.
func MergeDeploymentForUpdate(current, updated *uns.Unstructured) error {
	gvk := updated.GroupVersionKind()
	if gvk.Group == "apps" && gvk.Kind == "Deployment" {

		// Copy over the revision annotation from current up to updated
		// otherwise, updated would win, and this annotation is "special" and
		// needs to be preserved
		curAnnotations := current.GetAnnotations()
		updatedAnnotations := updated.GetAnnotations()
		if updatedAnnotations == nil {
			updatedAnnotations = map[string]string{}
		}

		anno, ok := curAnnotations[deploymentRevisionAnnotation]
		if ok {
			updatedAnnotations[deploymentRevisionAnnotation] = anno
		}

		updated.SetAnnotations(updatedAnnotations)
	}

	return nil
}

// MergeServiceForUpdate ensures the clusterip is never written to
func MergeServiceForUpdate(current, updated *uns.Unstructured) error {
	gvk := updated.GroupVersionKind()
	if gvk.Group == "" && gvk.Kind == "Service" {
		clusterIP, found, err := uns.NestedString(current.Object, "spec", "clusterIP")
		if err != nil {
			return err
		}

		if found {
			return uns.SetNestedField(updated.Object, clusterIP, "spec", "clusterIP")
		}
	}

	return nil
}

// mergeAnnotations copies over any annotations from current to updated,
// with updated winning if there's a conflict
func mergeAnnotations(current, updated *uns.Unstructured) {
	updatedAnnotations := updated.GetAnnotations()
	curAnnotations := current.GetAnnotations()

	if curAnnotations == nil {
		curAnnotations = map[string]string{}
	}

	for k, v := range updatedAnnotations {
		curAnnotations[k] = v
	}

	updated.SetAnnotations(curAnnotations)
}

// mergeLabels copies over any labels from current to updated,
// with updated winning if there's a conflict
func mergeLabels(current, updated *uns.Unstructured) {
	updatedLabels := updated.GetLabels()
	curLabels := current.GetLabels()

	if curLabels == nil {
		curLabels = map[string]string{}
	}

	for k, v := range updatedLabels {
		curLabels[k] = v
	}

	updated.SetLabels(curLabels)
}
