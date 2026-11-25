package operconfig

import (
	"testing"

	. "github.com/onsi/gomega"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/client/fake"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/cluster-network-operator/pkg/names"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func TestBGPCopyCollisionDoesNotBlockIndependentUpdates(t *testing.T) {
	g := NewWithT(t)
	owner := &operv1.Network{ObjectMeta: metav1.ObjectMeta{Name: names.OperatorConfig, UID: "network-uid"}}
	copyFor := func(name string) *corev1.Secret {
		return &corev1.Secret{
			TypeMeta: metav1.TypeMeta{Kind: "Secret", APIVersion: "v1"},
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: names.FRRK8sNamespace,
				Labels:          map[string]string{"app.kubernetes.io/managed-by": "cluster-network-operator"},
				Annotations:     map[string]string{names.BGPVIPPasswordSource: "openshift-config/" + name},
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(owner, operv1.GroupVersion.WithKind("Network"))},
			},
			Type: corev1.SecretTypeBasicAuth, Data: map[string][]byte{"password": []byte("rotated")},
		}
	}
	toObject := func(s *corev1.Secret) *uns.Unstructured {
		raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(s)
		g.Expect(err).NotTo(HaveOccurred())
		return &uns.Unstructured{Object: raw}
	}
	a, b := copyFor("peer-a"), copyFor("peer-b")
	objs := []*uns.Unstructured{toObject(a), toObject(b)}
	a.OwnerReferences = nil
	a.Data["password"] = []byte("foreign")
	b.Data["password"] = []byte("old")
	c := fake.NewFakeClient(owner, a, b)
	_, err := c.Default().OpenshiftOperatorClient().OperatorV1().Networks().Create(t.Context(), owner, metav1.CreateOptions{})
	g.Expect(err).NotTo(HaveOccurred())
	r := ReconcileOperConfig{client: c, status: statusmanager.New(c, "network", names.StandAloneClusterName)}
	for _, gvk := range []schema.GroupVersionKind{
		{Group: "frrk8s.metallb.io", Version: "v1beta1", Kind: "FRRConfiguration"},
		corev1.SchemeGroupVersion.WithKind("ConfigMap"),
	} {
		obj := &uns.Unstructured{}
		obj.SetGroupVersionKind(gvk)
		obj.SetNamespace(names.FRRK8sNamespace)
		obj.SetName("bgp-vip")
		objs = append(objs, obj)
	}
	var patched []string
	c.Default().Dynamic().(*dynamicfake.FakeDynamicClient).PrependReactor("patch", "*", func(action clienttesting.Action) (bool, runtime.Object, error) {
		patched = append(patched, action.GetResource().Resource)
		return true, &uns.Unstructured{}, nil
	})
	err = r.applyRenderedObjects(t.Context(), owner, objs)
	g.Expect(err).To(MatchError("could not reconcile BGP peer password Secret openshift-frr-k8s/peer-a: " +
		"BGP peer password Secret collision at openshift-frr-k8s/peer-a: not the expected CNO-managed copy"))
	for _, tc := range []struct{ name, want string }{{"peer-a", "foreign"}, {"peer-b", "rotated"}} {
		var live corev1.Secret
		g.Expect(c.Default().CRClient().Get(t.Context(), crclient.ObjectKey{Namespace: names.FRRK8sNamespace, Name: tc.name}, &live)).To(Succeed())
		g.Expect(string(live.Data["password"])).To(Equal(tc.want), tc.name)
	}
	g.Expect(patched).To(Equal([]string{"configmaps"}))
}
