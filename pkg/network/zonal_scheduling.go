package network

import (
	"fmt"

	"github.com/openshift/cluster-network-operator/pkg/hypershift"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
)

// networkOperandZoneCritical maps each HyperShift-mode network control-plane operand
// Deployment to whether it must be spread across availability zones ("zone-critical")
// under the Minimal control plane availability-zone scheduling policy. The blocking-webhook
// backends (network-node-identity, multus-admission-controller) stay zone-critical because
// their failurePolicy: Fail webhooks gate the guest apiserver; ovnkube-control-plane is a
// leader-elected controller and floats to overflow capacity.
var networkOperandZoneCritical = map[string]bool{
	"network-node-identity":       true,
	"multus-admission-controller": true,
	"ovnkube-control-plane":       false,
}

// applyMinimalZonalScheduling transforms the network control-plane operand Deployments
// for the Minimal control plane availability-zone scheduling policy: it replaces the zone
// podAntiAffinity with topologySpreadConstraints, steers each operand onto the correct
// management-cluster node pool (zonal for the blocking-webhook backends, overflow for
// ovnkube-control-plane), scopes colocation per tier, and runs network-node-identity as a
// two-replica pair instead of three. It is a no-op unless the hosted control plane has
// opted into the Minimal policy.
func applyMinimalZonalScheduling(objs []*uns.Unstructured, hcp *hypershift.HostedControlPlane) error {
	if hcp == nil || hcp.AvailabilityZoneSchedulingPolicy != hypershift.MinimalAvailabilityZoneSchedulingPolicy {
		return nil
	}
	hard := hcp.NonZonalPlacement == hypershift.NonZonalPlacementRequired

	for _, obj := range objs {
		if obj.GetKind() != "Deployment" || obj.GroupVersionKind().Group != "apps" {
			continue
		}
		zoneCritical, known := networkOperandZoneCritical[obj.GetName()]
		if !known {
			continue
		}

		deploy := &appsv1.Deployment{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, deploy); err != nil {
			return fmt.Errorf("converting %s to Deployment for zonal scheduling: %w", obj.GetName(), err)
		}
		mutateDeploymentForMinimalZonalScheduling(deploy, zoneCritical, hard)
		out, err := runtime.DefaultUnstructuredConverter.ToUnstructured(deploy)
		if err != nil {
			return fmt.Errorf("converting %s back to unstructured for zonal scheduling: %w", obj.GetName(), err)
		}
		obj.Object = out
	}
	return nil
}

func mutateDeploymentForMinimalZonalScheduling(deploy *appsv1.Deployment, zoneCritical, hard bool) {
	tier := hypershift.ControlPlaneNodeRoleOverflow
	if zoneCritical {
		tier = hypershift.ControlPlaneNodeRoleZonal
	}

	// The blocking-webhook backends run as two-replica pairs under the Minimal policy
	// (network-node-identity is otherwise three replicas in HA).
	if zoneCritical && deploy.Spec.Replicas != nil && *deploy.Spec.Replicas > 2 {
		deploy.Spec.Replicas = ptr.To[int32](2)
	}
	multiReplica := deploy.Spec.Replicas == nil || *deploy.Spec.Replicas > 1

	pt := &deploy.Spec.Template
	if pt.Labels == nil {
		pt.Labels = map[string]string{}
	}
	pt.Labels[hypershift.ControlPlaneSchedulingTierLabel] = tier

	if pt.Spec.Affinity == nil {
		pt.Spec.Affinity = &corev1.Affinity{}
	}

	// Replace the zone podAntiAffinity with topologySpreadConstraints below.
	if pt.Spec.Affinity.PodAntiAffinity != nil {
		removeZonePodAntiAffinity(pt.Spec.Affinity.PodAntiAffinity)
		if len(pt.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution) == 0 &&
			len(pt.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution) == 0 {
			pt.Spec.Affinity.PodAntiAffinity = nil
		}
	}

	// Steer onto the correct node pool. Zone-critical components are required onto the
	// zonal pools; float components are required (hard) or preferred (soft) onto overflow.
	if pt.Spec.Affinity.NodeAffinity == nil {
		pt.Spec.Affinity.NodeAffinity = &corev1.NodeAffinity{}
	}
	roleRequirement := corev1.NodeSelectorRequirement{
		Key:      hypershift.ControlPlaneNodeRoleLabel,
		Operator: corev1.NodeSelectorOpIn,
		Values:   []string{tier},
	}
	if zoneCritical || hard {
		pt.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = requireNodeSelector(
			pt.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution, roleRequirement)
	} else {
		pt.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution = append(
			pt.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution,
			corev1.PreferredSchedulingTerm{
				Weight:     100,
				Preference: corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{roleRequirement}},
			})
	}

	// Zone-critical components tolerate the zonal NoSchedule taint (present in hard mode).
	if zoneCritical {
		pt.Spec.Tolerations = append(pt.Spec.Tolerations, corev1.Toleration{
			Key:      hypershift.ControlPlaneNodeRoleLabel,
			Operator: corev1.TolerationOpEqual,
			Value:    hypershift.ControlPlaneNodeRoleZonal,
			Effect:   corev1.TaintEffectNoSchedule,
		})
	}

	// Scope colocation per tier so float pods are not pulled onto the zonal pools.
	if pt.Spec.Affinity.PodAffinity != nil {
		for i := range pt.Spec.Affinity.PodAffinity.PreferredDuringSchedulingIgnoredDuringExecution {
			term := &pt.Spec.Affinity.PodAffinity.PreferredDuringSchedulingIgnoredDuringExecution[i]
			if term.PodAffinityTerm.LabelSelector == nil {
				continue
			}
			if term.PodAffinityTerm.LabelSelector.MatchLabels == nil {
				term.PodAffinityTerm.LabelSelector.MatchLabels = map[string]string{}
			}
			term.PodAffinityTerm.LabelSelector.MatchLabels[hypershift.ControlPlaneSchedulingTierLabel] = tier
		}
	}

	// Spreading via topologySpreadConstraints (only meaningful for multi-replica
	// workloads): hard per-host spread for all, hard zone spread for zone-critical, and
	// best-effort zone spread for float.
	if multiReplica {
		selector := &metav1.LabelSelector{MatchLabels: deploy.Spec.Selector.MatchLabels}
		matchLabelKeys := []string{"pod-template-hash"}
		zoneWhenUnsatisfiable := corev1.ScheduleAnyway
		if zoneCritical {
			zoneWhenUnsatisfiable = corev1.DoNotSchedule
		}
		pt.Spec.TopologySpreadConstraints = append(pt.Spec.TopologySpreadConstraints,
			corev1.TopologySpreadConstraint{
				MaxSkew:           1,
				TopologyKey:       corev1.LabelHostname,
				WhenUnsatisfiable: corev1.DoNotSchedule,
				LabelSelector:     selector,
				MatchLabelKeys:    matchLabelKeys,
			},
			corev1.TopologySpreadConstraint{
				MaxSkew:           1,
				TopologyKey:       corev1.LabelTopologyZone,
				WhenUnsatisfiable: zoneWhenUnsatisfiable,
				LabelSelector:     selector,
				MatchLabelKeys:    matchLabelKeys,
			})
	}
}

// removeZonePodAntiAffinity drops any pod anti-affinity term keyed on
// topology.kubernetes.io/zone (replaced by topologySpreadConstraints).
func removeZonePodAntiAffinity(paa *corev1.PodAntiAffinity) {
	required := paa.RequiredDuringSchedulingIgnoredDuringExecution[:0]
	for _, term := range paa.RequiredDuringSchedulingIgnoredDuringExecution {
		if term.TopologyKey != corev1.LabelTopologyZone {
			required = append(required, term)
		}
	}
	paa.RequiredDuringSchedulingIgnoredDuringExecution = required

	preferred := paa.PreferredDuringSchedulingIgnoredDuringExecution[:0]
	for _, term := range paa.PreferredDuringSchedulingIgnoredDuringExecution {
		if term.PodAffinityTerm.TopologyKey != corev1.LabelTopologyZone {
			preferred = append(preferred, term)
		}
	}
	paa.PreferredDuringSchedulingIgnoredDuringExecution = preferred
}

// requireNodeSelector ANDs requirement into every existing required NodeSelectorTerm (or
// creates one if none exist). Required NodeSelectorTerms are ORed, so appending the
// requirement to each term ANDs it with all pre-existing constraints.
func requireNodeSelector(existing *corev1.NodeSelector, requirement corev1.NodeSelectorRequirement) *corev1.NodeSelector {
	if existing == nil || len(existing.NodeSelectorTerms) == 0 {
		return &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{MatchExpressions: []corev1.NodeSelectorRequirement{requirement}},
			},
		}
	}
	for i := range existing.NodeSelectorTerms {
		existing.NodeSelectorTerms[i].MatchExpressions = append(existing.NodeSelectorTerms[i].MatchExpressions, requirement)
	}
	return existing
}
