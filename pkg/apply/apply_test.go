package apply

import (
	"fmt"
	"testing"

	"github.com/openshift/cluster-network-operator/pkg/client/fake"
	"github.com/openshift/cluster-network-operator/pkg/names"

	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	fakedynamic "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

const prePatchValue = `{"spec":{"strategy":{"type":"Recreate","rollingUpdate":null}}}`

func newTestDeployment(annotations map[string]string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"})
	obj.SetName("test-deployment")
	obj.SetNamespace("test-ns")
	obj.SetAnnotations(annotations)
	return obj
}

func patchRecorder(patchTypes *[]types.PatchType, prePatchBody *string, prePatchErr error) func(action clienttesting.Action) (bool, runtime.Object, error) {
	return func(action clienttesting.Action) (bool, runtime.Object, error) {
		patchAction := action.(clienttesting.PatchAction)
		*patchTypes = append(*patchTypes, patchAction.GetPatchType())
		if patchAction.GetPatchType() == types.StrategicMergePatchType {
			*prePatchBody = string(patchAction.GetPatch())
			if prePatchErr != nil {
				return true, nil, prePatchErr
			}
		}
		return true, newTestDeployment(nil), nil
	}
}

func TestApplyObjectPrePatchRunsBeforeSSA(t *testing.T) {
	g := NewWithT(t)

	client := fake.NewFakeClient()

	var patchTypes []types.PatchType
	var prePatchBody string
	pr := patchRecorder(&patchTypes, &prePatchBody, nil)
	client.Default().Dynamic().(*fakedynamic.FakeDynamicClient).PrependReactor("patch", "deployments", pr)

	obj := newTestDeployment(map[string]string{
		names.PrePatchAnnotation: prePatchValue,
	})

	err := ApplyObject(t.Context(), client, obj, "test-controller")
	g.Expect(err).To(Succeed())
	g.Expect(patchTypes).To(HaveLen(2))
	g.Expect(patchTypes[0]).To(Equal(types.StrategicMergePatchType), "pre-patch should be strategic-merge-patch")
	g.Expect(patchTypes[1]).To(Equal(types.ApplyPatchType), "second patch should be SSA apply")
	g.Expect(prePatchBody).To(Equal(prePatchValue), "pre-patch body should match annotation value")
}

func TestApplyObjectPrePatchNotFoundAllowsSSA(t *testing.T) {
	g := NewWithT(t)

	client := fake.NewFakeClient()

	var patchTypes []types.PatchType
	var prePatchBody string
	pr := patchRecorder(&patchTypes, &prePatchBody, apierrors.NewNotFound(
		schema.GroupResource{Group: "apps", Resource: "deployments"}, "test-deployment"))
	client.Default().Dynamic().(*fakedynamic.FakeDynamicClient).PrependReactor("patch", "deployments", pr)

	obj := newTestDeployment(map[string]string{
		names.PrePatchAnnotation: prePatchValue,
	})

	err := ApplyObject(t.Context(), client, obj, "test-controller")
	g.Expect(err).To(Succeed())
	g.Expect(patchTypes).To(HaveLen(2))
	g.Expect(patchTypes[0]).To(Equal(types.StrategicMergePatchType), "pre-patch was attempted")
	g.Expect(patchTypes[1]).To(Equal(types.ApplyPatchType), "SSA apply still proceeded")
	g.Expect(prePatchBody).To(Equal(prePatchValue), "pre-patch body should match annotation value")
}

func TestApplyObjectPrePatchErrorStopsReconciliation(t *testing.T) {
	g := NewWithT(t)

	client := fake.NewFakeClient()

	var patchTypes []types.PatchType
	var prePatchBody string
	pr := patchRecorder(&patchTypes, &prePatchBody, fmt.Errorf("API server error"))
	client.Default().Dynamic().(*fakedynamic.FakeDynamicClient).PrependReactor("patch", "deployments", pr)

	obj := newTestDeployment(map[string]string{
		names.PrePatchAnnotation: prePatchValue,
	})

	err := ApplyObject(t.Context(), client, obj, "test-controller")
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("failed to pre-patch"))
	g.Expect(patchTypes).To(HaveLen(1), "SSA apply should not have been reached")
	g.Expect(patchTypes[0]).To(Equal(types.StrategicMergePatchType), "pre-patch should be strategic-merge-patch")
	g.Expect(prePatchBody).To(Equal(prePatchValue), "pre-patch body should match annotation value")
}

func TestApplyObjectNoPrePatchAnnotationSkipsPrePatch(t *testing.T) {
	g := NewWithT(t)

	client := fake.NewFakeClient()

	var patchTypes []types.PatchType
	var prePatchBody string
	pr := patchRecorder(&patchTypes, &prePatchBody, nil)
	client.Default().Dynamic().(*fakedynamic.FakeDynamicClient).PrependReactor("patch", "deployments", pr)

	obj := newTestDeployment(nil)

	err := ApplyObject(t.Context(), client, obj, "test-controller")
	g.Expect(err).To(Succeed())
	g.Expect(patchTypes).To(HaveLen(1))
	g.Expect(patchTypes[0]).To(Equal(types.ApplyPatchType), "only SSA apply should have run")
	g.Expect(prePatchBody).To(BeEmpty(), "no pre-patch should have been sent")
}
