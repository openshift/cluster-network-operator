package network

import (
	"testing"

	"github.com/openshift/cluster-network-operator/pkg/hypershift"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"

	. "github.com/onsi/gomega"
)

// legacyOperandDeployment builds an operand Deployment shaped like the rendered
// HyperShift-mode manifests: replicas, a zone podAntiAffinity, and a colocation podAffinity.
func legacyOperandDeployment(name string, replicas int32) *uns.Unstructured {
	deploy := &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{Kind: "Deployment", APIVersion: "apps/v1"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ocm-test"},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(replicas),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{
						PodAntiAffinity: &corev1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
								TopologyKey:   corev1.LabelTopologyZone,
								LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
							}},
						},
						PodAffinity: &corev1.PodAffinity{
							PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
								Weight: 100,
								PodAffinityTerm: corev1.PodAffinityTerm{
									TopologyKey:   corev1.LabelHostname,
									LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"hypershift.openshift.io/hosted-control-plane": "ocm-test"}},
								},
							}},
						},
					},
				},
			},
		},
	}
	out, err := runtime.DefaultUnstructuredConverter.ToUnstructured(deploy)
	if err != nil {
		panic(err)
	}
	return &uns.Unstructured{Object: out}
}

func toDeployment(g *WithT, obj *uns.Unstructured) *appsv1.Deployment {
	d := &appsv1.Deployment{}
	g.Expect(runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, d)).To(Succeed())
	return d
}

func findTSC(cs []corev1.TopologySpreadConstraint, key string) *corev1.TopologySpreadConstraint {
	for i := range cs {
		if cs[i].TopologyKey == key {
			return &cs[i]
		}
	}
	return nil
}

func requiredHasRole(na *corev1.NodeAffinity, value string) bool {
	if na == nil || na.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		return false
	}
	for _, t := range na.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
		for _, r := range t.MatchExpressions {
			if r.Key == hypershift.ControlPlaneNodeRoleLabel && len(r.Values) == 1 && r.Values[0] == value {
				return true
			}
		}
	}
	return false
}

func preferredHasRole(na *corev1.NodeAffinity, value string) bool {
	if na == nil {
		return false
	}
	for _, t := range na.PreferredDuringSchedulingIgnoredDuringExecution {
		for _, r := range t.Preference.MatchExpressions {
			if r.Key == hypershift.ControlPlaneNodeRoleLabel && len(r.Values) == 1 && r.Values[0] == value {
				return true
			}
		}
	}
	return false
}

func TestApplyMinimalZonalScheduling(t *testing.T) {
	minimalHCP := func(placement string) *hypershift.HostedControlPlane {
		return &hypershift.HostedControlPlane{
			Namespace:                        "ocm-test",
			ControllerAvailabilityPolicy:     hypershift.HighlyAvailable,
			AvailabilityZoneSchedulingPolicy: hypershift.MinimalAvailabilityZoneSchedulingPolicy,
			NonZonalPlacement:                placement,
		}
	}

	t.Run("is a no-op when the policy is not set", func(t *testing.T) {
		g := NewWithT(t)
		objs := []*uns.Unstructured{legacyOperandDeployment("ovnkube-control-plane", 2)}
		g.Expect(applyMinimalZonalScheduling(objs, &hypershift.HostedControlPlane{Namespace: "ocm-test"})).To(Succeed())
		d := toDeployment(g, objs[0])
		g.Expect(d.Spec.Template.Spec.TopologySpreadConstraints).To(BeEmpty())
		g.Expect(d.Spec.Template.Spec.Affinity.PodAntiAffinity).ToNot(BeNil(), "legacy zone podAntiAffinity should be preserved")
	})

	t.Run("network-node-identity becomes a strict zonal two-replica pair", func(t *testing.T) {
		g := NewWithT(t)
		objs := []*uns.Unstructured{legacyOperandDeployment("network-node-identity", 3)}
		g.Expect(applyMinimalZonalScheduling(objs, minimalHCP("Preferred"))).To(Succeed())
		d := toDeployment(g, objs[0])

		g.Expect(d.Spec.Replicas).ToNot(BeNil())
		g.Expect(*d.Spec.Replicas).To(Equal(int32(2)), "should drop from three to two replicas")
		g.Expect(d.Spec.Template.Labels).To(HaveKeyWithValue(hypershift.ControlPlaneSchedulingTierLabel, hypershift.ControlPlaneNodeRoleZonal))
		g.Expect(d.Spec.Template.Spec.Affinity.PodAntiAffinity).To(BeNil(), "zone podAntiAffinity should be removed")
		g.Expect(requiredHasRole(d.Spec.Template.Spec.Affinity.NodeAffinity, hypershift.ControlPlaneNodeRoleZonal)).To(BeTrue())

		zone := findTSC(d.Spec.Template.Spec.TopologySpreadConstraints, corev1.LabelTopologyZone)
		g.Expect(zone).ToNot(BeNil())
		g.Expect(zone.WhenUnsatisfiable).To(Equal(corev1.DoNotSchedule))
		g.Expect(zone.MatchLabelKeys).To(ContainElement("pod-template-hash"))
		g.Expect(findTSC(d.Spec.Template.Spec.TopologySpreadConstraints, corev1.LabelHostname)).ToNot(BeNil())

		// tolerates the zonal taint
		hasTol := false
		for _, tol := range d.Spec.Template.Spec.Tolerations {
			if tol.Key == hypershift.ControlPlaneNodeRoleLabel && tol.Value == hypershift.ControlPlaneNodeRoleZonal {
				hasTol = true
			}
		}
		g.Expect(hasTol).To(BeTrue())

		// colocation scoped to the zonal tier
		term := d.Spec.Template.Spec.Affinity.PodAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0]
		g.Expect(term.PodAffinityTerm.LabelSelector.MatchLabels).To(HaveKeyWithValue(hypershift.ControlPlaneSchedulingTierLabel, hypershift.ControlPlaneNodeRoleZonal))
	})

	t.Run("ovnkube-control-plane floats to overflow with best-effort zone spread", func(t *testing.T) {
		g := NewWithT(t)
		objs := []*uns.Unstructured{legacyOperandDeployment("ovnkube-control-plane", 2)}
		g.Expect(applyMinimalZonalScheduling(objs, minimalHCP("Preferred"))).To(Succeed())
		d := toDeployment(g, objs[0])

		g.Expect(*d.Spec.Replicas).To(Equal(int32(2)), "float replicas are unchanged")
		g.Expect(d.Spec.Template.Labels).To(HaveKeyWithValue(hypershift.ControlPlaneSchedulingTierLabel, hypershift.ControlPlaneNodeRoleOverflow))
		g.Expect(preferredHasRole(d.Spec.Template.Spec.Affinity.NodeAffinity, hypershift.ControlPlaneNodeRoleOverflow)).To(BeTrue())
		g.Expect(d.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution).To(BeNil())

		zone := findTSC(d.Spec.Template.Spec.TopologySpreadConstraints, corev1.LabelTopologyZone)
		g.Expect(zone).ToNot(BeNil())
		g.Expect(zone.WhenUnsatisfiable).To(Equal(corev1.ScheduleAnyway))
	})

	t.Run("hard placement makes float overflow affinity required", func(t *testing.T) {
		g := NewWithT(t)
		objs := []*uns.Unstructured{legacyOperandDeployment("ovnkube-control-plane", 2)}
		g.Expect(applyMinimalZonalScheduling(objs, minimalHCP(hypershift.NonZonalPlacementRequired))).To(Succeed())
		d := toDeployment(g, objs[0])
		g.Expect(requiredHasRole(d.Spec.Template.Spec.Affinity.NodeAffinity, hypershift.ControlPlaneNodeRoleOverflow)).To(BeTrue())
	})
}
