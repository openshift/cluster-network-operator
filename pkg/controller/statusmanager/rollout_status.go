package statusmanager

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"reflect"
	"strings"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/names"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	client "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// if a rollout has not made any progress by this time,
	// mark ourselves as Degraded
	ProgressTimeout = 10 * time.Minute

	// lastSeenAnnotation - the annotation where we stash our state
	lastSeenAnnotation = "network.operator.openshift.io/last-seen-state"
)

// rolloutState is a snapshot of the last-seen-state and last-changed-times
// for pod-creating entities, as marshalled to json in an annotation
type rolloutState struct {
	// "public" for marshalling to json, since we can't have complex keys
	DaemonsetStates  []daemonsetState
	DeploymentStates []deploymentState
	PodStates        []podState
}

// daemonsetState is the internal state we use to check if a rollout has
// stalled.
type daemonsetState struct {
	types.NamespacedName

	LastSeenStatus appsv1.DaemonSetStatus
	LastChangeTime time.Time
}

// deploymentState is the same as daemonsetState.. but for deployments!
type deploymentState struct {
	types.NamespacedName

	LastSeenStatus appsv1.DeploymentStatus
	LastChangeTime time.Time
}

// podState  is the same as daemonsetState.. but for pods!
type podState struct {
	types.NamespacedName

	LastSeenStatus v1.PodStatus
	LastChangeTime time.Time
}

// SetFromRollout sets the operator Degraded/Progressing/Available status, based on
// the current status of the manager's DaemonSets, Deployments and Pods.
func (status *StatusManager) SetFromRollout() {
	status.Lock()
	defer status.Unlock()

	targetLevel := os.Getenv("RELEASE_VERSION")
	reachedAvailableLevel := (len(status.daemonSets) + len(status.deployments) + len(status.pods)) > 0

	progressing := []string{}
	hung := []string{}

	daemonsetStates, deploymentStates, podStates := status.getLastRolloutState()

	for _, dsName := range status.daemonSets {
		ds := &appsv1.DaemonSet{}
		if err := status.client.Get(context.TODO(), dsName, ds); err != nil {
			log.Printf("Error getting DaemonSet %q: %v", dsName.String(), err)
			progressing = append(progressing, fmt.Sprintf("Waiting for DaemonSet %q to be created", dsName.String()))
			reachedAvailableLevel = false
			// Assume the OperConfig Controller is in the process of reconciling
			// things; it will set a Degraded status if it fails.
			continue
		}

		dsProgressing := false

		if isNonCritical(ds) && ds.Status.NumberReady == 0 && !status.installComplete {
			progressing = append(progressing, fmt.Sprintf("DaemonSet %q is waiting for other operators to become ready", dsName.String()))
			dsProgressing = true
		} else if ds.Status.UpdatedNumberScheduled < ds.Status.DesiredNumberScheduled {
			progressing = append(progressing, fmt.Sprintf("DaemonSet %q update is rolling out (%d out of %d updated)", dsName.String(), ds.Status.UpdatedNumberScheduled, ds.Status.DesiredNumberScheduled))
			dsProgressing = true
		} else if ds.Status.NumberUnavailable > 0 {
			progressing = append(progressing, fmt.Sprintf("DaemonSet %q is not available (awaiting %d nodes)", dsName.String(), ds.Status.NumberUnavailable))
			dsProgressing = true
			// Check for any pods in CrashLoopBackOff state and mark the operator as degraded if so.
			if !isNonCritical(ds) {
				hung = append(hung, status.CheckCrashLoopBackOffPods(dsName, ds.Spec.Selector.MatchLabels, "DaemonSet")...)
			}
		} else if ds.Status.NumberAvailable == 0 { // NOTE: update this if we ever expect empty (unscheduled) daemonsets ~cdc
			progressing = append(progressing, fmt.Sprintf("DaemonSet %q is not yet scheduled on any nodes", dsName.String()))
			dsProgressing = true
		} else if ds.Generation > ds.Status.ObservedGeneration {
			progressing = append(progressing, fmt.Sprintf("DaemonSet %q update is being processed (generation %d, observed generation %d)", dsName.String(), ds.Generation, ds.Status.ObservedGeneration))
			dsProgressing = true
		}

		if ds.Annotations["release.openshift.io/version"] != targetLevel {
			reachedAvailableLevel = false
		}

		var dsHung *string

		if dsProgressing && !isNonCritical(ds) {
			reachedAvailableLevel = false

			dsState, exists := daemonsetStates[dsName]
			if !exists || !reflect.DeepEqual(dsState.LastSeenStatus, ds.Status) {
				dsState.LastChangeTime = time.Now()
				ds.Status.DeepCopyInto(&dsState.LastSeenStatus)
				daemonsetStates[dsName] = dsState
			}

			// Catch hung rollouts
			if exists && (time.Since(dsState.LastChangeTime)) > ProgressTimeout {
				hung = append(hung, fmt.Sprintf("DaemonSet %q rollout is not making progress - last change %s", dsName.String(), dsState.LastChangeTime.Format(time.RFC3339)))
				empty := ""
				dsHung = &empty
			}
		} else {
			delete(daemonsetStates, dsName)
		}
		if err := status.setDSAnnotation(ds, names.RolloutHungAnnotation, dsHung); err != nil {
			log.Printf("Error setting DaemonSet %q annotation: %v", dsName, err)
		}
	}

	for _, depName := range status.deployments {
		dep := &appsv1.Deployment{}
		if err := status.client.Get(context.TODO(), depName, dep); err != nil {
			log.Printf("Error getting Deployment %q: %v", depName.String(), err)
			progressing = append(progressing, fmt.Sprintf("Waiting for Deployment %q to be created", depName.String()))
			reachedAvailableLevel = false
			// Assume the OperConfig Controller is in the process of reconciling
			// things; it will set a Degraded status if it fails.
			continue
		}

		depProgressing := false

		if isNonCritical(dep) && dep.Status.UnavailableReplicas > 0 && !status.installComplete {
			progressing = append(progressing, fmt.Sprintf("Deployment %q is waiting for other operators to become ready", depName.String()))
			depProgressing = true
		} else if dep.Status.UnavailableReplicas > 0 {
			progressing = append(progressing, fmt.Sprintf("Deployment %q is not available (awaiting %d nodes)", depName.String(), dep.Status.UnavailableReplicas))
			depProgressing = true
			// Check for any pods in CrashLoopBackOff state and mark the operator as degraded if so.
			if !isNonCritical(dep) {
				hung = append(hung, status.CheckCrashLoopBackOffPods(depName, dep.Spec.Selector.MatchLabels, "Deployment")...)
			}
		} else if dep.Status.AvailableReplicas == 0 {
			progressing = append(progressing, fmt.Sprintf("Deployment %q is not yet scheduled on any nodes", depName.String()))
			depProgressing = true
		} else if dep.Status.ObservedGeneration < dep.Generation {
			progressing = append(progressing, fmt.Sprintf("Deployment %q update is being processed (generation %d, observed generation %d)", depName.String(), dep.Generation, dep.Status.ObservedGeneration))
			depProgressing = true
		}

		if dep.Annotations["release.openshift.io/version"] != targetLevel {
			reachedAvailableLevel = false
		}

		var depHung *string

		if depProgressing && !isNonCritical(dep) {
			reachedAvailableLevel = false

			depState, exists := deploymentStates[depName]
			if !exists || !reflect.DeepEqual(depState.LastSeenStatus, dep.Status) {
				depState.LastChangeTime = time.Now()
				dep.Status.DeepCopyInto(&depState.LastSeenStatus)
				deploymentStates[depName] = depState
			}

			// Catch hung rollouts
			if exists && (time.Since(depState.LastChangeTime)) > ProgressTimeout {
				hung = append(hung, fmt.Sprintf("Deployment %q rollout is not making progress - last change %s", depName.String(), depState.LastChangeTime.Format(time.RFC3339)))
				empty := ""
				depHung = &empty
			}
		} else {
			delete(deploymentStates, depName)
		}
		if err := status.setDepAnnotation(dep, names.RolloutHungAnnotation, depHung); err != nil {
			log.Printf("Error setting Deployment %q annotation: %v", depName, err)
		}
	}

	// Bare pods are assumed to be jobs that are run to completion
	for _, podName := range status.pods {
		pod := &v1.Pod{}
		if err := status.client.Get(context.TODO(), podName, pod); err != nil {
			log.Printf("Error getting Pod %q: %v", podName.String(), err)
			progressing = append(progressing, fmt.Sprintf("Waiting for Pod %q to be created", podName.String()))
			reachedAvailableLevel = false
			// Assume the OperConfig Controller is in the process of reconciling
			// things; it will set a Degraded status if it fails.
			continue
		}

		podProgressing := false

		switch pod.Status.Phase {
		case v1.PodSucceeded:
		case v1.PodFailed:
			hung = append(progressing, fmt.Sprintf("Pod %q has failed", podName.String()))
		case v1.PodPending:
			progressing = append(progressing, fmt.Sprintf("Pod %q is pending", podName.String()))
			podProgressing = true
		case v1.PodRunning:
			progressing = append(progressing, fmt.Sprintf("Pod %q is progressing", podName.String()))
			podProgressing = true
		default:
			progressing = append(progressing, fmt.Sprintf("Pod %q state is unknown", podName.String()))
			podProgressing = true
		}

		// Check for any pods in CrashLoopBackOff state and mark the operator as degraded if so.
		if !isNonCritical(pod) {
			hung = append(hung, status.CheckCrashLoopBackOffPod(pod)...)
		}

		var depHung *string
		if podProgressing && !isNonCritical(pod) {
			reachedAvailableLevel = false

			podState, exists := podStates[podName]
			if !exists || !reflect.DeepEqual(podState.LastSeenStatus, pod.Status) {
				podState.LastChangeTime = time.Now()
				pod.Status.DeepCopyInto(&podState.LastSeenStatus)
				podStates[podName] = podState
			}

			// Catch hung rollouts
			if exists && (time.Since(podState.LastChangeTime)) > ProgressTimeout {
				hung = append(hung, fmt.Sprintf("Pod %q rollout is not making progress - last change %s", podName.String(), podState.LastChangeTime.Format(time.RFC3339)))
				empty := ""
				depHung = &empty
			}
		} else {
			delete(podStates, podName)
		}
		if err := status.setPodAnnotation(pod, names.RolloutHungAnnotation, depHung); err != nil {
			log.Printf("Error setting Pod %q annotation: %v", podName, err)
		}
	}

	status.setNotDegraded(PodDeployment)
	if err := status.setLastRolloutState(daemonsetStates, deploymentStates, podStates); err != nil {
		log.Printf("Failed to set pod state (continuing): %+v\n", err)
	}

	conditions := make([]operv1.OperatorCondition, 0, 2)
	if len(progressing) > 0 {
		conditions = append(conditions,
			operv1.OperatorCondition{
				Type:    operv1.OperatorStatusTypeProgressing,
				Status:  operv1.ConditionTrue,
				Reason:  "Deploying",
				Message: strings.Join(progressing, "\n"),
			},
		)
	} else {
		conditions = append(conditions,
			operv1.OperatorCondition{
				Type:   operv1.OperatorStatusTypeProgressing,
				Status: operv1.ConditionFalse,
			},
		)
	}
	if reachedAvailableLevel {
		conditions = append(conditions,
			operv1.OperatorCondition{
				Type:   operv1.OperatorStatusTypeAvailable,
				Status: operv1.ConditionTrue,
			},
		)
		status.installComplete = true
	}

	status.set(reachedAvailableLevel, conditions...)
	if len(hung) > 0 {
		status.setDegraded(RolloutHung, "RolloutHung", strings.Join(hung, "\n"))
	} else {
		status.setNotDegraded(RolloutHung)
	}
}

// getLastRolloutState reads the last-seen daemonset + deployment state
// from the clusteroperator annotation and parses it. On error, it returns
// an empty state, since this should not block updating operator status.
func (status *StatusManager) getLastRolloutState() (map[types.NamespacedName]daemonsetState, map[types.NamespacedName]deploymentState, map[types.NamespacedName]podState) {
	// with maps allocated
	daemonsetStates := map[types.NamespacedName]daemonsetState{}
	deploymentStates := map[types.NamespacedName]deploymentState{}
	podStates := map[types.NamespacedName]podState{}

	// Load the last-seen snapshot from our annotation
	co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: status.name}}
	err := status.client.Get(context.TODO(), types.NamespacedName{Name: status.name}, co)
	if err != nil {
		log.Printf("Failed to get ClusterOperator: %v", err)
		return daemonsetStates, deploymentStates, podStates
	}

	lsbytes := co.Annotations[lastSeenAnnotation]
	if lsbytes == "" {
		return daemonsetStates, deploymentStates, podStates
	}

	out := rolloutState{}
	err = json.Unmarshal([]byte(lsbytes), &out)
	if err != nil {
		// No need to return error; just move on
		log.Printf("failed to unmashal last-seen-status: %v", err)
		return daemonsetStates, deploymentStates, podStates
	}

	for _, ds := range out.DaemonsetStates {
		daemonsetStates[ds.NamespacedName] = ds
	}

	for _, ds := range out.DeploymentStates {
		deploymentStates[ds.NamespacedName] = ds
	}

	for _, ps := range out.PodStates {
		podStates[ps.NamespacedName] = ps
	}

	return daemonsetStates, deploymentStates, podStates
}

func (status *StatusManager) setLastRolloutState(
	dss map[types.NamespacedName]daemonsetState,
	deps map[types.NamespacedName]deploymentState,
	pods map[types.NamespacedName]podState) error {

	rs := rolloutState{
		DaemonsetStates:  make([]daemonsetState, 0, len(dss)),
		DeploymentStates: make([]deploymentState, 0, len(deps)),
		PodStates:        make([]podState, 0, len(pods)),
	}

	for nsn, ds := range dss {
		ds.NamespacedName = nsn
		rs.DaemonsetStates = append(rs.DaemonsetStates, ds)
	}

	for nsn, ds := range deps {
		ds.NamespacedName = nsn
		rs.DeploymentStates = append(rs.DeploymentStates, ds)
	}

	for nsn, ps := range pods {
		ps.NamespacedName = nsn
		rs.PodStates = append(rs.PodStates, ps)
	}

	lsbytes, err := json.Marshal(rs)
	if err != nil {
		return err
	}

	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		oldStatus := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: status.name}}
		err := status.client.Get(context.TODO(), types.NamespacedName{Name: status.name}, oldStatus)
		isNotFound := errors.IsNotFound(err)
		if err != nil && !isNotFound {
			return err
		}

		newStatus := oldStatus.DeepCopy()
		if newStatus.Annotations == nil {
			newStatus.Annotations = map[string]string{}
		}
		newStatus.Annotations[lastSeenAnnotation] = string(lsbytes)
		return status.client.Patch(context.TODO(), newStatus, client.MergeFrom(oldStatus))
	})
}

// CheckCrashLoopBackOffPods checks for pods (matching the label selector) with
// any containers in the CrashLoopBackoff state. It returns a human-readable string
// for any pod in such a state.
// dName should be the name of a DaemonSet or Deployment.
func (status *StatusManager) CheckCrashLoopBackOffPods(dName types.NamespacedName, selector map[string]string, kind string) []string {
	hung := []string{}
	pods := &v1.PodList{}
	prefix := fmt.Sprintf("%s %q rollout is not making progress", kind, dName.String())
	err := status.client.List(context.TODO(), pods, client.InNamespace(dName.Namespace), client.MatchingLabels(selector))
	if err != nil {
		log.Printf("Error getting pods from %s %q: %v", kind, dName.String(), err)
	}
	for _, pod := range pods.Items {

		hung = append(hung, status.checkCrashLoopBackOffPodWithPrefix(&pod, prefix)...)
	}
	return hung
}

// CheckCrashLoopBackOffPod checks a pod with any containers in the CrashLoopBackoff
// state. It returns a human-readable string for any pod in such a state.
func (status *StatusManager) CheckCrashLoopBackOffPod(pod *v1.Pod) []string {
	return status.checkCrashLoopBackOffPodWithPrefix(pod, "")
}

// CheckCrashLoopBackOffPod checks a pod with any containers in the CrashLoopBackoff
// state. It returns a human-readable string for any pod in such a state with the given
// prefix if any.
func (status *StatusManager) checkCrashLoopBackOffPodWithPrefix(pod *v1.Pod, prefix string) []string {
	hung := []string{}

	for _, container := range pod.Status.ContainerStatuses {
		if container.State.Waiting != nil {
			if container.State.Waiting.Reason == "CrashLoopBackOff" {
				if prefix != "" {
					hung = append(hung, fmt.Sprintf("%s - pod %s is in CrashLoopBackOff State", prefix, pod.Name))
				} else {
					hung = append(hung, fmt.Sprintf("Pod %s is in CrashLoopBackOff State", pod.Name))
				}
				// we can break once we find at least one container crashing in this pod
				break
			}
		}
	}
	return hung
}

func isNonCritical(obj metav1.Object) bool {
	_, exists := obj.GetAnnotations()[names.NonCriticalAnnotation]
	return exists
}

// setDSAnnotation sets an annotation on a daemonset; or unsets it if value is nil
func (status *StatusManager) setDSAnnotation(obj *appsv1.DaemonSet, key string, value *string) error {
	new := obj.DeepCopy()
	return status.patchAnnotation(obj, new, key, value)
}

// setDepAnnotation sets an annotation on a Deployment. If value is nil,
// it unsets the annotation
func (status *StatusManager) setDepAnnotation(obj *appsv1.Deployment, key string, value *string) error {
	new := obj.DeepCopy()
	return status.patchAnnotation(obj, new, key, value)
}

func (status *StatusManager) setPodAnnotation(obj *v1.Pod, key string, value *string) error {
	new := obj.DeepCopy()
	return status.patchAnnotation(obj, new, key, value)
}

func (status *StatusManager) patchAnnotation(old client.Object, new client.Object, key string, value *string) error {
	anno := new.GetAnnotations()

	existing, set := anno[key]
	if value != nil && set && existing == *value {
		return nil
	}
	if !set && value == nil {
		return nil
	}

	if value != nil {
		if anno == nil {
			anno = map[string]string{}
		}
		anno[key] = *value
	} else {
		delete(anno, key)
	}
	new.SetAnnotations(anno)
	return status.client.Patch(context.TODO(), new, client.MergeFrom(old))
}
