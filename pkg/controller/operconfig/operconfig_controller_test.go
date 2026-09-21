package operconfig

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openshift/cluster-network-operator/pkg/network"
)

func TestNodeUpdateTriggersReconcile(t *testing.T) {
	base := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Labels:      map[string]string{"kubernetes.io/os": "linux"},
		Annotations: map[string]string{},
	}}

	tests := []struct {
		name   string
		mutate func(*corev1.Node)
		want   bool
	}{
		{name: "condition-only update", mutate: func(node *corev1.Node) {
			node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
		}},
		{name: "uplink capability annotation", mutate: func(node *corev1.Node) {
			node.Annotations[network.OVNUplinkModeCapabilityAnnotation] = "v1"
		}, want: true},
		{name: "schedulability", mutate: func(node *corev1.Node) {
			node.Spec.Unschedulable = true
		}, want: true},
		{name: "labels", mutate: func(node *corev1.Node) {
			node.Labels["k8s.ovn.org/dpu"] = ""
		}, want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			updated := base.DeepCopy()
			test.mutate(updated)
			if got := nodeUpdateTriggersReconcile(base, updated); got != test.want {
				t.Fatalf("nodeUpdateTriggersReconcile() = %t, want %t", got, test.want)
			}
		})
	}
}
