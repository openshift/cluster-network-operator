package statusmanager

import (
	"context"
	"fmt"
	"log"
	"os"
	"reflect"
	"strings"

	"github.com/ghodss/yaml"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/library-go/pkg/config/clusteroperator/v1helpers"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

type StatusLevel int

const (
	ClusterConfig  StatusLevel = iota
	OperatorConfig StatusLevel = iota
	ProxyConfig    StatusLevel = iota
	PodDeployment  StatusLevel = iota
	maxStatusLevel StatusLevel = iota
)

// StatusManager coordinates changes to ClusterOperator.Status
type StatusManager struct {
	client  client.Client
	name    string
	version string

	failing [maxStatusLevel]*configv1.ClusterOperatorStatusCondition

	daemonSets     []types.NamespacedName
	deployments    []types.NamespacedName
	relatedObjects []configv1.ObjectReference
}

func New(client client.Client, name, version string) *StatusManager {
	return &StatusManager{client: client, name: name, version: version}
}

// Set updates the ClusterOperator.Status with the provided conditions
func (status *StatusManager) Set(reachedAvailableLevel bool, conditions ...configv1.ClusterOperatorStatusCondition) {
	co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: status.name}}
	err := status.client.Get(context.TODO(), types.NamespacedName{Name: status.name}, co)
	isNotFound := errors.IsNotFound(err)
	if err != nil && !isNotFound {
		log.Printf("Failed to get ClusterOperator %q: %v", status.name, err)
		return
	}

	oldStatus := co.Status.DeepCopy()
	co.Status.RelatedObjects = status.relatedObjects

	if reachedAvailableLevel {
		if releaseVersion := os.Getenv("RELEASE_VERSION"); len(releaseVersion) > 0 {
			co.Status.Versions = []configv1.OperandVersion{
				{Name: "operator", Version: releaseVersion},
			}
		} else {
			co.Status.Versions = nil
		}
	}
	for _, condition := range conditions {
		v1helpers.SetStatusCondition(&co.Status.Conditions, condition)
	}

	progressingCondition := v1helpers.FindStatusCondition(co.Status.Conditions, configv1.OperatorProgressing)
	availableCondition := v1helpers.FindStatusCondition(co.Status.Conditions, configv1.OperatorAvailable)
	if availableCondition == nil && progressingCondition != nil && progressingCondition.Status == configv1.ConditionTrue {
		v1helpers.SetStatusCondition(&co.Status.Conditions,
			configv1.ClusterOperatorStatusCondition{
				Type:    configv1.OperatorAvailable,
				Status:  configv1.ConditionFalse,
				Reason:  "Startup",
				Message: "The network is starting up",
			},
		)
	}

	v1helpers.SetStatusCondition(&co.Status.Conditions,
		configv1.ClusterOperatorStatusCondition{
			Type:   configv1.OperatorUpgradeable,
			Status: configv1.ConditionTrue,
		},
	)

	if reflect.DeepEqual(*oldStatus, co.Status) {
		return
	}

	buf, err := yaml.Marshal(co.Status.Conditions)
	if err != nil {
		buf = []byte(fmt.Sprintf("(failed to convert to YAML: %s)", err))
	}
	if isNotFound {
		if err := status.client.Create(context.TODO(), co); err != nil {
			log.Printf("Failed to create ClusterOperator %q: %v", co.Name, err)
		} else {
			log.Printf("Created ClusterOperator with conditions:\n%s", string(buf))
		}
	} else {
		err = status.client.Status().Update(context.TODO(), co)
		if err != nil {
			log.Printf("Failed to update ClusterOperator %q: %v", co.Name, err)
		} else {
			log.Printf("Updated ClusterOperator with conditions:\n%s", string(buf))
		}
	}
}

// syncDegraded syncs the current Degraded status
func (status *StatusManager) syncDegraded() {
	for _, c := range status.failing {
		if c != nil {
			status.Set(false, *c)
			return
		}
	}
	status.Set(
		false,
		configv1.ClusterOperatorStatusCondition{
			Type:   configv1.OperatorDegraded,
			Status: configv1.ConditionFalse,
		},
	)
}

// SetDegraded marks the operator as Degraded with the given reason and message. If it
// is not already failing for a lower-level reason, the operator's status will be updated.
func (status *StatusManager) SetDegraded(level StatusLevel, reason, message string) {
	status.failing[level] = &configv1.ClusterOperatorStatusCondition{
		Type:    configv1.OperatorDegraded,
		Status:  configv1.ConditionTrue,
		Reason:  reason,
		Message: message,
	}
	status.syncDegraded()
}

// SetNotDegraded marks the operator as not Degraded at the given level. If the operator
// status previously indicated failure at this level, it will updated to show the next
// higher-level failure, or else to show that the operator is no longer failing.
func (status *StatusManager) SetNotDegraded(level StatusLevel) {
	if status.failing[level] != nil {
		status.failing[level] = nil
	}
	status.syncDegraded()
}

func (status *StatusManager) SetDaemonSets(daemonSets []types.NamespacedName) {
	status.daemonSets = daemonSets
}

func (status *StatusManager) SetDeployments(deployments []types.NamespacedName) {
	status.deployments = deployments
}

func (status *StatusManager) SetRelatedObjects(relatedObjects []configv1.ObjectReference) {
	status.relatedObjects = relatedObjects
}

// SetFromPods sets the operator Degraded/Progressing/Available status, based on
// the current status of the manager's DaemonSets and Deployments.
func (status *StatusManager) SetFromPods() {

	targetLevel := os.Getenv("RELEASE_VERSION")
	reachedAvailableLevel := (len(status.daemonSets) + len(status.deployments)) > 0

	progressing := []string{}

	for _, dsName := range status.daemonSets {
		ds := &appsv1.DaemonSet{}
		if err := status.client.Get(context.TODO(), dsName, ds); err != nil {
			log.Printf("Error getting DaemonSet %q: %v", dsName.String(), err)
			progressing = append(progressing, fmt.Sprintf("Waiting for DaemonSet %q to be created", dsName.String()))
			// Assume the OperConfig Controller is in the process of reconciling
			// things; it will set a Degraded status if it fails.
			continue
		}

		if ds.Status.UpdatedNumberScheduled < ds.Status.DesiredNumberScheduled {
			progressing = append(progressing, fmt.Sprintf("DaemonSet %q update is rolling out (%d out of %d updated)", dsName.String(), ds.Status.UpdatedNumberScheduled, ds.Status.DesiredNumberScheduled))
		} else if ds.Status.NumberUnavailable > 0 {
			progressing = append(progressing, fmt.Sprintf("DaemonSet %q is not available (awaiting %d nodes)", dsName.String(), ds.Status.NumberUnavailable))
		} else if ds.Status.NumberAvailable == 0 { // NOTE: update this if we ever expect empty (unscheduled) daemonsets ~cdc
			progressing = append(progressing, fmt.Sprintf("DaemonSet %q is not yet scheduled on any nodes", dsName.String()))
		} else if ds.Generation > ds.Status.ObservedGeneration {
			progressing = append(progressing, fmt.Sprintf("DaemonSet %q update is being processed (generation %d, observed generation %d)", dsName.String(), ds.Generation, ds.Status.ObservedGeneration))
		}

		if !(ds.Generation <= ds.Status.ObservedGeneration && ds.Status.UpdatedNumberScheduled == ds.Status.DesiredNumberScheduled && ds.Status.NumberUnavailable == 0 && ds.Annotations["release.openshift.io/version"] == targetLevel) {
			reachedAvailableLevel = false
		}
	}

	for _, depName := range status.deployments {
		dep := &appsv1.Deployment{}
		if err := status.client.Get(context.TODO(), depName, dep); err != nil {
			log.Printf("Error getting Deployment %q: %v", depName.String(), err)
			progressing = append(progressing, fmt.Sprintf("Waiting for Deployment %q to be created", depName.String()))
			// Assume the OperConfig Controller is in the process of reconciling
			// things; it will set a Degraded status if it fails.
			continue
		}

		if dep.Status.UnavailableReplicas > 0 {
			progressing = append(progressing, fmt.Sprintf("Deployment %q is not available (awaiting %d nodes)", depName.String(), dep.Status.UnavailableReplicas))
		} else if dep.Status.AvailableReplicas == 0 {
			progressing = append(progressing, fmt.Sprintf("Deployment %q is not yet scheduled on any nodes", depName.String()))
		} else if dep.Status.ObservedGeneration < dep.Generation {
			progressing = append(progressing, fmt.Sprintf("Deployment %q update is being processed (generation %d, observed generation %d)", depName.String(), dep.Generation, dep.Status.ObservedGeneration))
		}

		if !(dep.Generation <= dep.Status.ObservedGeneration && dep.Status.UpdatedReplicas == dep.Status.Replicas && dep.Status.AvailableReplicas > 0 && dep.Annotations["release.openshift.io/version"] == targetLevel) {
			reachedAvailableLevel = false
		}
	}

	status.SetNotDegraded(PodDeployment)

	if len(progressing) > 0 {
		status.Set(
			reachedAvailableLevel,
			configv1.ClusterOperatorStatusCondition{
				Type:    configv1.OperatorProgressing,
				Status:  configv1.ConditionTrue,
				Reason:  "Deploying",
				Message: strings.Join(progressing, "\n"),
			},
		)
	} else {
		status.Set(
			reachedAvailableLevel,
			configv1.ClusterOperatorStatusCondition{
				Type:   configv1.OperatorProgressing,
				Status: configv1.ConditionFalse,
			},
			configv1.ClusterOperatorStatusCondition{
				Type:   configv1.OperatorAvailable,
				Status: configv1.ConditionTrue,
			},
		)
	}
}
