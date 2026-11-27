package configmapcainjector

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	clientfake "github.com/openshift/cluster-network-operator/pkg/client/fake"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/library-go/pkg/crypto"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	v1corelisters "k8s.io/client-go/listers/core/v1"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// TestReconcileConfigMapApplyErrors controls cache contents and API responses to
// exercise deletion races without depending on informer delivery timing.
func TestReconcileConfigMapApplyErrors(t *testing.T) {
	g := NewWithT(t)
	ca, err := crypto.MakeSelfSignedCAConfigForDuration("test-ca", time.Hour)
	g.Expect(err).NotTo(HaveOccurred(), "create test CA")
	certificate := &bytes.Buffer{}
	g.Expect(ca.WriteCertConfig(certificate, &bytes.Buffer{})).To(Succeed(), "encode test CA")

	notFound := apierrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, "test-namespace")
	unavailable := apierrors.NewServiceUnavailable("API server unavailable")
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "target-ca", fmt.Errorf("access denied"))
	terminating := apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "target-ca", fmt.Errorf("namespace is terminating"))
	terminating.ErrStatus.Details.Causes = []metav1.StatusCause{{
		Type:    corev1.NamespaceTerminatingCause,
		Message: "namespace test-namespace is being terminated",
		Field:   "metadata.namespace",
	}}

	for _, tt := range []struct {
		name          string
		patchErrors   []error
		singleTarget  bool
		deletedTarget bool
		wantError     bool
	}{
		{
			name:        "skips stale configmap when namespace is deleted",
			patchErrors: []error{notFound},
		},
		{
			name:         "skips stale configmap on an individual target request",
			patchErrors:  []error{notFound},
			singleTarget: true,
		},
		{
			name:         "preserves a peer failure after an individual target is notfound",
			patchErrors:  []error{notFound, unavailable},
			singleTarget: true,
			wantError:    true,
		},
		{
			name:         "preserves a peer failure after an individual namespace terminates",
			patchErrors:  []error{terminating, forbidden},
			singleTarget: true,
			wantError:    true,
		},
		{
			name:         "updates a healthy peer after an individual target is notfound",
			patchErrors:  []error{notFound, nil},
			singleTarget: true,
		},
		{
			name:         "updates a healthy peer after an individual namespace terminates",
			patchErrors:  []error{terminating, nil},
			singleTarget: true,
		},
		{
			name:         "continues checking peers after another deletion",
			patchErrors:  []error{notFound, terminating, unavailable},
			singleTarget: true,
			wantError:    true,
		},
		{
			name:        "skips creation in a terminating namespace",
			patchErrors: []error{terminating},
		},
		{
			name:        "returns service unavailable and remains degraded",
			patchErrors: []error{unavailable},
			wantError:   true,
		},
		{
			name:        "returns ordinary forbidden and remains degraded",
			patchErrors: []error{forbidden},
			wantError:   true,
		},
		{
			name:        "updates another configmap after notfound",
			patchErrors: []error{notFound, nil},
		},
		{
			name:        "updates another configmap after namespace termination",
			patchErrors: []error{terminating, nil},
		},
		{
			name:        "preserves an API failure after notfound",
			patchErrors: []error{notFound, unavailable},
			wantError:   true,
		},
		{
			name:        "preserves an API failure before notfound",
			patchErrors: []error{unavailable, notFound},
			wantError:   true,
		},
		{
			name:          "clears degradation after the last target leaves the cache",
			deletedTarget: true,
		},
		{
			name:          "checks remaining targets after a target leaves the cache",
			patchErrors:   []error{nil},
			deletedTarget: true,
		},
		{
			name:          "preserves remaining failures after a target leaves the cache",
			patchErrors:   []error{unavailable},
			deletedTarget: true,
			wantError:     true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			client := clientfake.NewFakeClient()
			status := statusmanager.New(client, "network", names.StandAloneClusterName)
			clusterOperator := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: "network"}}
			operatorConfig := &operv1.Network{ObjectMeta: metav1.ObjectMeta{Name: names.OperatorConfig}}
			g.Expect(client.Default().CRClient().Create(t.Context(), clusterOperator)).To(Succeed(), "create ClusterOperator")
			_, createErr := client.Default().OpenshiftOperatorClient().OperatorV1().Networks().Create(t.Context(), operatorConfig, metav1.CreateOptions{})
			g.Expect(createErr).NotTo(HaveOccurred(), "create operator Network")

			expectDegraded := func(want configv1.ConditionStatus) {
				t.Helper()
				g.Expect(client.Default().CRClient().Get(t.Context(), types.NamespacedName{Name: "network"}, clusterOperator)).To(Succeed(), "read ClusterOperator status")
				g.Expect(clusterOperator.Status.Conditions).To(ContainElement(And(
					HaveField("Type", configv1.OperatorDegraded),
					HaveField("Status", want),
				)), "injector Degraded condition")
			}
			status.SetDegraded(t.Context(), statusmanager.InjectorConfig, "ConfigmapUpdateFailure", "test degraded condition")
			expectDegraded(configv1.ConditionTrue)

			trustedConfigMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: names.TrustedCABundleConfigMapNamespace,
					Name:      names.TrustedCABundleConfigMapName,
					Annotations: map[string]string{
						names.OpenShiftComponent: "test-component",
					},
				},
				Data: map[string]string{
					names.TrustedCABundleConfigMapKey: certificate.String(),
				},
			}
			trustedIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{
				cache.NamespaceIndex: cache.MetaNamespaceIndexFunc,
			})
			g.Expect(trustedIndexer.Add(trustedConfigMap)).To(Succeed(), "cache trusted CA bundle")

			labelIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{
				cache.NamespaceIndex: cache.MetaNamespaceIndexFunc,
			})
			target := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "test-namespace",
					Labels: map[string]string{
						names.TrustedCABundleConfigMapLabel: "true",
					},
				},
			}
			var expectedTargets []types.NamespacedName
			for i := range tt.patchErrors {
				cm := target.DeepCopy()
				cm.Name = fmt.Sprintf("target-%d", i)
				if tt.singleTarget && i > 0 {
					// A peer with the same name in another namespace must still be checked.
					cm.Namespace = fmt.Sprintf("peer-namespace-%d", i)
					cm.Name = "target-0"
				}
				g.Expect(labelIndexer.Add(cm)).To(Succeed(), "cache target %s", cm.Name)
				expectedTargets = append(expectedTargets, types.NamespacedName{Namespace: cm.Namespace, Name: cm.Name})
			}

			request := reconcile.Request{NamespacedName: types.NamespacedName{
				Namespace: trustedConfigMap.Namespace,
				Name:      trustedConfigMap.Name,
			}}
			if tt.singleTarget {
				request.NamespacedName = types.NamespacedName{Namespace: target.Namespace, Name: "target-0"}
			}
			if tt.deletedTarget {
				target.Name = "deleted-ca"
				g.Expect(labelIndexer.Add(target)).To(Succeed(), "cache target before deletion")
				g.Expect(labelIndexer.Delete(target)).To(Succeed(), "remove deleted target from cache")
				request.NamespacedName = types.NamespacedName{Namespace: target.Namespace, Name: target.Name}
			}

			// Assign outcomes by call order so the first target fails even when the
			// lister returns targets in a different order. This detects early exits.
			var patchedTargets []types.NamespacedName
			dynamicClient := client.Default().Dynamic().(*dynamicfake.FakeDynamicClient)
			dynamicClient.PrependReactor("patch", "configmaps", func(action ktesting.Action) (bool, runtime.Object, error) {
				patch := action.(ktesting.PatchAction)
				g.Expect(patch.GetPatchType()).To(Equal(types.ApplyPatchType), "use SSA for %s", patch.GetName())
				patchedTarget := types.NamespacedName{Namespace: patch.GetNamespace(), Name: patch.GetName()}
				g.Expect(expectedTargets).To(ContainElement(patchedTarget), "patch a cached target")
				var updated corev1.ConfigMap
				g.Expect(json.Unmarshal(patch.GetPatch(), &updated)).To(Succeed(), "decode patch for %s", patch.GetName())
				g.Expect(updated.Data).To(HaveKeyWithValue(names.TrustedCABundleConfigMapKey, certificate.String()), "inject trusted CA into %s", patch.GetName())
				g.Expect(updated.Annotations).To(HaveKeyWithValue(names.OpenShiftComponent, "test-component"), "copy component ownership to %s", patch.GetName())
				g.Expect(len(patchedTargets)).To(BeNumerically("<", len(tt.patchErrors)), "unexpected extra patch for %s", patch.GetName())
				patchErr := tt.patchErrors[len(patchedTargets)]
				patchedTargets = append(patchedTargets, patchedTarget)
				return true, nil, patchErr
			})

			reconciler := &ReconcileConfigMapInjector{
				client:      client,
				status:      status,
				labelLister: v1corelisters.NewConfigMapLister(labelIndexer),
				nsLister:    v1corelisters.NewConfigMapLister(trustedIndexer),
			}
			_, reconcileErr := reconciler.Reconcile(t.Context(), request)
			g.Expect(patchedTargets).To(ConsistOf(expectedTargets), "process every cached target exactly once")
			if tt.wantError {
				g.Expect(reconcileErr).To(MatchError("some configmaps didn't fully update with CA cert. data"), "report genuine API errors")
				expectDegraded(configv1.ConditionTrue)
			} else {
				g.Expect(reconcileErr).NotTo(HaveOccurred(), "reconcile deleted or healthy targets")
				expectDegraded(configv1.ConditionFalse)
			}
		})
	}
}
