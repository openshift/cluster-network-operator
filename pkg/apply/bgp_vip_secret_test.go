package apply

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/names"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestBGPVIPPasswordCopyOwnership(t *testing.T) {
	g := NewWithT(t)
	scheme := runtime.NewScheme()
	g.Expect(corev1.AddToScheme(scheme)).To(Succeed())
	g.Expect(operv1.Install(scheme)).To(Succeed())
	owner := &operv1.Network{ObjectMeta: metav1.ObjectMeta{Name: "cluster", UID: "network-uid"}}
	key := client.ObjectKey{Namespace: names.FRRK8sNamespace, Name: "peer"}
	desired := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name: key.Name, Namespace: key.Namespace,
			Labels:          map[string]string{"app.kubernetes.io/managed-by": "cluster-network-operator"},
			Annotations:     map[string]string{names.BGPVIPPasswordSource: "openshift-config/peer"},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(owner, operv1.GroupVersion.WithKind("Network"))},
		},
		Type: corev1.SecretTypeBasicAuth,
		Data: map[string][]byte{"password": []byte("new")},
	}
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(desired)
	g.Expect(err).NotTo(HaveOccurred())
	obj := &uns.Unstructured{Object: raw}
	g.Expect(IsBGPVIPPasswordCopy(obj)).To(BeTrue())
	for name, change := range map[string]func(*corev1.Secret){
		"unmanaged":          func(s *corev1.Secret) { s.OwnerReferences = nil; s.Labels = nil; s.Annotations = nil },
		"label only":         func(s *corev1.Secret) { s.OwnerReferences = nil; s.Annotations = nil },
		"another controller": func(s *corev1.Secret) { s.OwnerReferences[0].UID = "another-uid" },
		"other managed copy": func(s *corev1.Secret) { s.Annotations[names.BGPVIPPasswordSource] = "openshift-config/other" },
	} {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			existing := desired.DeepCopy()
			change(existing)
			existing.Data["password"] = []byte("original")
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(owner.DeepCopy(), existing).Build()
			var before, after corev1.Secret
			g.Expect(c.Get(t.Context(), key, &before)).To(Succeed())
			g.Expect(ReconcileBGPVIPPasswordCopy(t.Context(), c, obj, owner.UID)).To(MatchError(ContainSubstring("collision")))
			g.Expect(PruneBGPVIPPasswordCopy(t.Context(), c, key)).To(Succeed())
			g.Expect(c.Get(t.Context(), key, &after)).To(Succeed())
			g.Expect(after).To(Equal(before))
		})
	}
	t.Run("create rotate and prune owned copy", func(t *testing.T) {
		g := NewWithT(t)
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(owner.DeepCopy()).Build()
		g.Expect(ReconcileBGPVIPPasswordCopy(t.Context(), c, obj, owner.UID)).To(Succeed())
		var live corev1.Secret
		g.Expect(c.Get(t.Context(), key, &live)).To(Succeed())
		g.Expect(live.Data).To(Equal(desired.Data))
		live.Data["password"] = []byte("outdated")
		g.Expect(c.Update(t.Context(), &live)).To(Succeed())
		g.Expect(ReconcileBGPVIPPasswordCopy(t.Context(), c, obj, owner.UID)).To(Succeed())
		g.Expect(c.Get(t.Context(), key, &live)).To(Succeed())
		g.Expect(live.Data).To(Equal(desired.Data))
		g.Expect(PruneBGPVIPPasswordCopy(t.Context(), c, key)).To(Succeed())
		g.Expect(apierrors.IsNotFound(c.Get(t.Context(), key, &live))).To(BeTrue())
	})
	t.Run("concurrent creator", func(t *testing.T) {
		g := NewWithT(t)
		c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				foreign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}
				g.Expect(c.Create(ctx, foreign)).To(Succeed())
				return c.Create(ctx, obj, opts...)
			},
		}).Build()
		g.Expect(apierrors.IsAlreadyExists(ReconcileBGPVIPPasswordCopy(t.Context(), c, obj, owner.UID))).To(BeTrue())
		var live corev1.Secret
		g.Expect(c.Get(t.Context(), key, &live)).To(Succeed())
		g.Expect(live.Data).To(BeEmpty())
	})
	t.Run("ownership changes during rotation", func(t *testing.T) {
		g := NewWithT(t)
		existing := desired.DeepCopy()
		existing.Data["password"] = []byte("original")
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).WithInterceptorFuncs(interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				var foreign corev1.Secret
				g.Expect(c.Get(ctx, key, &foreign)).To(Succeed())
				foreign.OwnerReferences = nil
				g.Expect(c.Update(ctx, &foreign)).To(Succeed())
				return c.Update(ctx, obj, opts...)
			},
		}).Build()
		g.Expect(apierrors.IsConflict(ReconcileBGPVIPPasswordCopy(t.Context(), c, obj, owner.UID))).To(BeTrue())
		var live corev1.Secret
		g.Expect(c.Get(t.Context(), key, &live)).To(Succeed())
		g.Expect(live.Data["password"]).To(Equal([]byte("original")))
		g.Expect(live.OwnerReferences).To(BeEmpty())
	})
}
