package configmapcainjector

import (
	"bytes"
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

func TestReconcileConfigMapApplyErrors(t *testing.T) {
	for _, tt := range []struct {
		name         string
		patchError   error
		wantError    string
		wantDegraded configv1.ConditionStatus
	}{
		{
			name:         "skips stale configmap when namespace is deleted",
			patchError:   apierrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, "test-namespace"),
			wantDegraded: configv1.ConditionFalse,
		},
		{
			name:         "returns non-notfound error and remains degraded",
			patchError:   apierrors.NewServiceUnavailable("API server unavailable"),
			wantError:    "some configmaps didn't fully update with CA cert. data",
			wantDegraded: configv1.ConditionTrue,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			// Start degraded to verify that only the skipped NotFound error clears it.
			client := clientfake.NewFakeClient()
			status := statusmanager.New(client, "network", names.StandAloneClusterName)
			clusterOperator := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: "network"}}
			operatorConfig := &operv1.Network{ObjectMeta: metav1.ObjectMeta{Name: names.OperatorConfig}}
			g.Expect(client.Default().CRClient().Create(t.Context(), clusterOperator)).To(Succeed())
			_, err := client.Default().OpenshiftOperatorClient().OperatorV1().Networks().Create(t.Context(), operatorConfig, metav1.CreateOptions{})
			g.Expect(err).To(Succeed())
			status.SetDegraded(t.Context(), statusmanager.InjectorConfig, "ConfigmapUpdateFailure", "test degraded condition")
			g.Expect(client.Default().CRClient().Get(t.Context(), types.NamespacedName{Name: "network"}, clusterOperator)).To(Succeed())
			g.Expect(clusterOperator.Status.Conditions).To(ContainElement(And(
				HaveField("Type", configv1.OperatorDegraded),
				HaveField("Status", configv1.ConditionTrue),
			)))

			// Populate the cache directly: in the NotFound case, the ConfigMap and
			// namespace have already been deleted from the live API, but the informer
			// has not yet processed their deletion.
			staleConfigMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "test-namespace",
					Name:      "stale-ca",
					Labels: map[string]string{
						names.TrustedCABundleConfigMapLabel: "true",
					},
				},
			}
			ca, err := crypto.MakeSelfSignedCAConfigForDuration("test-ca", time.Hour)
			g.Expect(err).NotTo(HaveOccurred())
			certificate := &bytes.Buffer{}
			g.Expect(ca.WriteCertConfig(certificate, &bytes.Buffer{})).NotTo(HaveOccurred())

			trustedConfigMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: names.TrustedCABundleConfigMapNamespace,
					Name:      names.TrustedCABundleConfigMapName,
				},
				Data: map[string]string{
					names.TrustedCABundleConfigMapKey: certificate.String(),
				},
			}

			labelIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{
				cache.NamespaceIndex: cache.MetaNamespaceIndexFunc,
			})
			g.Expect(labelIndexer.Add(staleConfigMap)).To(Succeed())

			// The trusted bundle comes from the namespace-scoped informer cache.
			trustedIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{
				cache.NamespaceIndex: cache.MetaNamespaceIndexFunc,
			})
			g.Expect(trustedIndexer.Add(trustedConfigMap)).To(Succeed())

			// Inject the live API error at the SSA patch boundary.
			dynamicClient := client.Default().Dynamic().(*dynamicfake.FakeDynamicClient)
			patchAttempted := false
			dynamicClient.PrependReactor("patch", "configmaps", func(_ ktesting.Action) (bool, runtime.Object, error) {
				patchAttempted = true
				return true, nil, tt.patchError
			})

			reconciler := &ReconcileConfigMapInjector{
				client:      client,
				status:      status,
				labelLister: v1corelisters.NewConfigMapLister(labelIndexer),
				nsLister:    v1corelisters.NewConfigMapLister(trustedIndexer),
			}

			// A trusted-bundle change fans out to every ConfigMap in the label cache,
			// including the stale object created above.
			request := reconcile.Request{NamespacedName: types.NamespacedName{
				Namespace: names.TrustedCABundleConfigMapNamespace,
				Name:      names.TrustedCABundleConfigMapName,
			}}

			_, err = reconciler.Reconcile(t.Context(), request)
			if tt.wantError == "" {
				g.Expect(err).NotTo(HaveOccurred())
			} else {
				g.Expect(err).To(MatchError(tt.wantError))
			}
			g.Expect(patchAttempted).To(BeTrue())

			err = client.Default().CRClient().Get(t.Context(), types.NamespacedName{Name: "network"}, clusterOperator)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(clusterOperator.Status.Conditions).To(ContainElement(And(
				HaveField("Type", configv1.OperatorDegraded),
				HaveField("Status", tt.wantDegraded),
			)))
		})
	}
}
