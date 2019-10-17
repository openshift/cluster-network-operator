package operconfig

import (
	"testing"

	. "github.com/onsi/gomega"

	"context"

	operv1 "github.com/openshift/api/operator/v1"

	netattachv1 "github.com/K8sNetworkPlumbingWG/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	cli "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestClearNetAttachDef(t *testing.T) {
	g := NewGomegaWithT(t)
	ownerRefs := []metav1.OwnerReference{
		{
			Kind: "Network",
			Name: "cluster",
		},
	}

	netattach1 := &netattachv1.NetworkAttachmentDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "net-attach-1",
			Namespace: "default",
		},
		Spec: netattachv1.NetworkAttachmentDefinitionSpec{},
	}
	netattach1.ObjectMeta.SetOwnerReferences(ownerRefs)

	netattach2 := &netattachv1.NetworkAttachmentDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "net-attach-2",
			Namespace: "default",
		},
		Spec: netattachv1.NetworkAttachmentDefinitionSpec{},
	}
	netattach2.ObjectMeta.SetOwnerReferences(ownerRefs)

	scheme := runtime.NewScheme()
	netattachv1.AddToScheme(scheme)
	client := fake.NewFakeClientWithScheme(scheme, netattach1, netattach2)

	prevSpec := &operv1.NetworkSpec{
		AdditionalNetworks: []operv1.AdditionalNetworkDefinition{
			{Type: operv1.NetworkTypeRaw, Name: "net-attach-1", RawCNIConfig: "{}"},
			{Type: operv1.NetworkTypeRaw, Name: "net-attach-2", RawCNIConfig: "{}"},
		},
	}

	currentSpec := &operv1.NetworkSpec{
		AdditionalNetworks: []operv1.AdditionalNetworkDefinition{
			{Type: operv1.NetworkTypeRaw, Name: "net-attach-2", RawCNIConfig: "{}"},
		},
	}

	err := clearNetAttachDef(client, prevSpec, currentSpec)
	g.Expect(err).NotTo(HaveOccurred())

	obj := &netattachv1.NetworkAttachmentDefinition{}
	err = client.Get(context.TODO(), cli.ObjectKey{
		Name:      "net-attach-2",
		Namespace: "default",
	}, obj)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(obj.GetName()).To(Equal("net-attach-2"))

	// verify remvoved object
	obj = &netattachv1.NetworkAttachmentDefinition{}
	err = client.Get(context.TODO(), cli.ObjectKey{
		Name:      "net-attach-1",
		Namespace: "default",
	}, obj)
	g.Expect(err).To(HaveOccurred())

}
