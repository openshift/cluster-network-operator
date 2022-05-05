package statusmanager

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/openshift/cluster-network-operator/pkg/apply"
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
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// if a rollout has not made any progress by this time,
	// mark ourselves as Degraded
	ProgressTimeout = 10 * time.Minute

	// lastSeenAnnotation - the annotation where we stash our state
	lastSeenAnnotation = "network.operator.openshift.io/last-seen-state"
)

// podState is a snapshot of the last-seen-state and last-changed-times
// for pod-creating entities, as marshalled to json in an annotation
type podState struct {
	// "public" for marshalling to json, since we can't have complex keys
	DaemonsetStates   []daemonsetState
	DeploymentStates  []deploymentState
	StatefulsetStates []statefulsetState
}

// daemonsetState is the internal state we use to check if a rollout has
// stalled.
type daemonsetState struct {
	ClusteredName

	LastSeenStatus appsv1.DaemonSetStatus
	LastChangeTime time.Time
}

// deploymentState is the same as daemonsetState.. but for deployments!
type deploymentState struct {
	ClusteredName

	LastSeenStatus appsv1.DeploymentStatus
	LastChangeTime time.Time
}

// statefulsetState is the same as daemonsetState.. but for statefulsets!
type statefulsetState struct {
	ClusteredName

	LastSeenStatus appsv1.StatefulSetStatus
	LastChangeTime time.Time
}

// SetFromPods sets the operator Degraded/Progressing/Available status, based on
// the current status of the manager's DaemonSets, Deployments and StatefulSets.
func (status *StatusManager) SetFromPods() {
	status.Lock()
	defer status.Unlock()

	targetLevel := os.Getenv("RELEASE_VERSION")
	reachedAvailableLevel := (len(status.daemonSets) + len(status.deployments) + len(status.statefulSets)) > 0

	progressing := []string{}
	hung := []string{}

	daemonsetStates, deploymentStates, statefulsetStates := status.getLastPodState()

	for _, dsName := range status.daemonSets {
		ds := &appsv1.DaemonSet{}
		if err := status.client.ClientFor(dsName.ClusterName).CRClient().Get(context.TODO(), types.NamespacedName{Namespace: dsName.Namespace, Name: dsName.Name}, ds); err != nil {
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

	for _, ssName := range status.statefulSets {
		ss := &appsv1.StatefulSet{}
		if err := status.client.ClientFor(ssName.ClusterName).CRClient().Get(context.TODO(), types.NamespacedName{Namespace: ssName.Namespace, Name: ssName.Name}, ss); err != nil {
			log.Printf("Error getting StatefulSet %q: %v", ssName.String(), err)
			progressing = append(progressing, fmt.Sprintf("Waiting for StatefulSet %q to be created", ssName.String()))
			reachedAvailableLevel = false
			// Assume the OperConfig Controller is in the process of reconciling
			// things; it will set a Degraded status if it fails.
			continue
		}

		ssProgressing := false

		if isNonCritical(ss) && ss.Status.ReadyReplicas == 0 && !status.installComplete {
			progressing = append(progressing, fmt.Sprintf("StatefulSet %q is waiting for other operators to become ready", ssName.String()))
			ssProgressing = true
		} else if ss.Status.ReadyReplicas > 0 && ss.Status.ReadyReplicas < ss.Status.Replicas {
			progressing = append(progressing, fmt.Sprintf("StatefulSet %q is not available (awaiting %d nodes)", ssName.String(), (ss.Status.Replicas-ss.Status.ReadyReplicas)))
			ssProgressing = true
			// Check for any pods in CrashLoopBackOff state and mark the operator as degraded if so.
			if !isNonCritical(ss) {
				hung = append(hung, status.CheckCrashLoopBackOffPods(ssName, ss.Spec.Selector.MatchLabels, "StatefulSet")...)
			}
		} else if ss.Status.AvailableReplicas == 0 {
			progressing = append(progressing, fmt.Sprintf("StatefulSet %q is not yet scheduled on any nodes", ssName.String()))
			ssProgressing = true
		} else if ss.Status.ObservedGeneration < ss.Generation {
			progressing = append(progressing, fmt.Sprintf("StatefulSet %q update is being processed (generation %d, observed generation %d)", ssName.String(), ss.Generation, ss.Status.ObservedGeneration))
			ssProgressing = true
		}

		if ss.Annotations["release.openshift.io/version"] != targetLevel {
			reachedAvailableLevel = false
		}

		var ssHung *string

		if ssProgressing && !isNonCritical(ss) {
			reachedAvailableLevel = false

			ssState, exists := statefulsetStates[ssName]
			if !exists || !reflect.DeepEqual(ssState.LastSeenStatus, ss.Status) {
				ssState.LastChangeTime = time.Now()
				ss.Status.DeepCopyInto(&ssState.LastSeenStatus)
				statefulsetStates[ssName] = ssState
			}

			// Catch hung rollouts
			if exists && (time.Since(ssState.LastChangeTime)) > ProgressTimeout {
				hung = append(hung, fmt.Sprintf("StatefulSet %q rollout is not making progress - last change %s", ssName.String(), ssState.LastChangeTime.Format(time.RFC3339)))
				empty := ""
				ssHung = &empty
			}
		} else {
			delete(statefulsetStates, ssName)
		}
		if err := status.setSSAnnotation(ss, names.RolloutHungAnnotation, ssHung); err != nil {
			log.Printf("Error setting StatefulSet %q annotation: %v", ssName, err)
		}
	}

	for _, depName := range status.deployments {
		dep := &appsv1.Deployment{}
		if err := status.client.ClientFor(depName.ClusterName).CRClient().Get(context.TODO(), types.NamespacedName{Namespace: depName.Namespace, Name: depName.Name}, dep); err != nil {
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

	status.setNotDegraded(PodDeployment)
	if err := status.setLastPodState(daemonsetStates, deploymentStates, statefulsetStates); err != nil {
		log.Printf("Failed to set pod state (continuing): %+v\n", err)
	}

	if len(progressing) > 0 {
		status.setProgressing(PodDeployment, "Deploying", strings.Join(progressing, "\n"))
	} else {
		status.unsetProgressing(PodDeployment)
	}

	if reachedAvailableLevel {
		status.set(reachedAvailableLevel, operv1.OperatorCondition{
			Type:   operv1.OperatorStatusTypeAvailable,
			Status: operv1.ConditionTrue})
	}

	if reachedAvailableLevel && len(progressing) == 0 {
		status.installComplete = true
	}

	if len(hung) > 0 {
		status.setDegraded(RolloutHung, "RolloutHung", strings.Join(hung, "\n"))
	} else {
		status.setNotDegraded(RolloutHung)
	}
}

// getLastPodState reads the last-seen daemonset + deployment + statefulset
// states from the clusteroperator annotation and parses it. On error, it
// returns an empty state, since this should not block updating operator status.
func (status *StatusManager) getLastPodState() (map[ClusteredName]daemonsetState, map[ClusteredName]deploymentState, map[ClusteredName]statefulsetState) {
	// with maps allocated
	daemonsetStates := map[ClusteredName]daemonsetState{}
	deploymentStates := map[ClusteredName]deploymentState{}
	statefulsetStates := map[ClusteredName]statefulsetState{}

	// Load the last-seen snapshot from our annotation
	co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: status.name}}
	err := status.client.ClientFor("").CRClient().Get(context.TODO(), types.NamespacedName{Name: status.name}, co)
	if err != nil {
		log.Printf("Failed to get ClusterOperator: %v", err)
		return daemonsetStates, deploymentStates, statefulsetStates
	}

	lsbytes := co.Annotations[lastSeenAnnotation]
	if lsbytes == "" {
		return daemonsetStates, deploymentStates, statefulsetStates
	}

	out := podState{}
	err = json.Unmarshal([]byte(lsbytes), &out)
	if err != nil {
		// No need to return error; just move on
		log.Printf("failed to unmashal last-seen-status: %v", err)
		return daemonsetStates, deploymentStates, statefulsetStates
	}

	for _, ds := range out.DaemonsetStates {
		daemonsetStates[ds.ClusteredName] = ds
	}

	for _, ds := range out.DeploymentStates {
		deploymentStates[ds.ClusteredName] = ds
	}

	for _, ss := range out.StatefulsetStates {
		statefulsetStates[ss.ClusteredName] = ss
	}

	return daemonsetStates, deploymentStates, statefulsetStates
}

func (status *StatusManager) setLastPodState(
	dss map[ClusteredName]daemonsetState,
	deps map[ClusteredName]deploymentState,
	sss map[ClusteredName]statefulsetState) error {

	ps := podState{
		DaemonsetStates:   make([]daemonsetState, 0, len(dss)),
		DeploymentStates:  make([]deploymentState, 0, len(deps)),
		StatefulsetStates: make([]statefulsetState, 0, len(sss)),
	}

	for nsn, ds := range dss {
		ds.ClusteredName = nsn
		ps.DaemonsetStates = append(ps.DaemonsetStates, ds)
	}

	for nsn, ds := range deps {
		ds.ClusteredName = nsn
		ps.DeploymentStates = append(ps.DeploymentStates, ds)
	}

	for nsn, ss := range sss {
		ss.ClusteredName = nsn
		ps.StatefulsetStates = append(ps.StatefulsetStates, ss)
	}

	lsbytes, err := json.Marshal(ps)
	if err != nil {
		return err
	}

	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		oldStatus := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: status.name}}
		err := status.client.ClientFor("").CRClient().Get(context.TODO(), types.NamespacedName{Name: status.name}, oldStatus)
		isNotFound := errors.IsNotFound(err)
		if err != nil && !isNotFound {
			return err
		}

		newStatus := oldStatus.DeepCopy()
		if newStatus.Annotations == nil {
			newStatus.Annotations = map[string]string{}
		}
		newStatus.Annotations[lastSeenAnnotation] = string(lsbytes)
		return status.client.ClientFor("").CRClient().Patch(context.TODO(), newStatus, crclient.MergeFrom(oldStatus))
	})
}

// CheckCrashLoopBackOffPods checks for pods (matching the label selector) with
// any containers in the CrashLoopBackoff state. It returns a human-readable string
// for any pod in such a state.
// name should be the name of a DaemonSet or Deployment or StatefulSet.
func (status *StatusManager) CheckCrashLoopBackOffPods(name ClusteredName, selector map[string]string, kind string) []string {
	hung := []string{}
	pods := &v1.PodList{}
	err := status.client.ClientFor(name.ClusterName).CRClient().List(context.TODO(), pods, crclient.InNamespace(name.Namespace), crclient.MatchingLabels(selector))
	if err != nil {
		log.Printf("Error getting pods from %s %q: %v", kind, name.String(), err)
	}
	for _, pod := range pods.Items {
		for _, container := range pod.Status.ContainerStatuses {
			if container.State.Waiting != nil {
				if container.State.Waiting.Reason == "CrashLoopBackOff" {
					hung = append(hung, fmt.Sprintf("%s %q rollout is not making progress - pod %s is in CrashLoopBackOff State", kind, name.String(), pod.Name))
					// we can break once we find at least one container crashing in this pod
					break
				}
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
	return status.client.ClientFor(apply.GetClusterName(obj)).CRClient().Patch(context.TODO(), new, crclient.MergeFrom(obj))
}

// setSSAnnotation sets an annotation on a statefulset; or unsets it if value is nil
func (status *StatusManager) setSSAnnotation(obj *appsv1.StatefulSet, key string, value *string) error {
	new := obj.DeepCopy()
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
	return status.client.ClientFor(apply.GetClusterName(obj)).CRClient().Patch(context.TODO(), new, crclient.MergeFrom(obj))
}

// setDepAnnotation sets an annotation on a Deployment. If value is nil,
// it unsets the annotation
func (status *StatusManager) setDepAnnotation(obj *appsv1.Deployment, key string, value *string) error {
	new := obj.DeepCopy()
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
	return status.client.ClientFor(apply.GetClusterName(obj)).CRClient().Patch(context.TODO(), new, crclient.MergeFrom(obj))
}
