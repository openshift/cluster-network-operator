package clusteroperator

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/ghodss/yaml"

	configv1 "github.com/openshift/api/config/v1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// StatusManager coordinates changes to ClusterOperator.Status
type StatusManager struct {
	client  client.Client
	name    string
	version string

	configFailure bool

	daemonSets  []types.NamespacedName
	deployments []types.NamespacedName
}

func NewStatusManager(client client.Client, name, version string) *StatusManager {
	return &StatusManager{client: client, name: name, version: version}
}

// Set updates the ClusterOperator.Status with the provided conditions
func (status *StatusManager) Set(conditions ...*configv1.ClusterOperatorStatusCondition) error {
	co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: status.name}}
	err := status.client.Get(context.TODO(), types.NamespacedName{Name: status.name}, co)
	isNotFound := errors.IsNotFound(err)
	if err != nil && !isNotFound {
		log.Printf("Failed to get ClusterOperator %q: %v", status.name, err)
		return fmt.Errorf("failed to get ClusterOperator %s: %v", status.name, err)
	}

	oldConditions := co.Status.Conditions
	for _, condition := range conditions {
		co.Status.Conditions = SetStatusCondition(co.Status.Conditions, condition)
	}
	if ConditionsEqual(oldConditions, co.Status.Conditions) {
		return nil
	}

	buf, err := yaml.Marshal(co.Status)
	if err != nil {
		buf = []byte(fmt.Sprintf("(failed to convert to YAML: %s)", err))
	}
	if isNotFound {
		if err := status.client.Create(context.TODO(), co); err != nil {
			log.Printf("Failed to create ClusterOperator %q: %v", co.Name, err)
			return fmt.Errorf("failed to create ClusterOperator %q: %v", co.Name, err)
		}
		log.Printf("Created ClusterOperator with status:\n%s", string(buf))
	} else {
		err = status.client.Status().Update(context.TODO(), co)
		if err != nil {
			log.Printf("Failed to update ClusterOperator %q: %v", co.Name, err)
			return fmt.Errorf("failed to update ClusterOperator %s: %v", co.Name, err)
		}
		log.Printf("Updated ClusterOperator with status:\n%s", string(buf))
	}
	return nil
}

// SetConfigFailure marks the operator as Failing due to a configuration problem. Attempts
// to mark the operator as Progressing or Available will be ignored until SetConfigSuccess
// is called to clear the config error.
func (status *StatusManager) SetConfigFailing(reason string, err error) error {
	status.configFailure = true
	return status.SetFailing(reason, err)
}

// SetConfigSuccess marks the operator as having a valid configuration, allowing it
// to be set Progressing or Available.
func (status *StatusManager) SetConfigSuccess() error {
	status.configFailure = false
	return status.Set(
		&configv1.ClusterOperatorStatusCondition{
			Type:   configv1.OperatorFailing,
			Status: configv1.ConditionFalse,
		},
	)
}

// SetFailing marks the operator as Failing, with the given reason and error message.
// Unlike with SetConfigFailing, this failure is not persistent.
func (status *StatusManager) SetFailing(reason string, err error) error {
	return status.Set(
		&configv1.ClusterOperatorStatusCondition{
			Type:    configv1.OperatorFailing,
			Status:  configv1.ConditionTrue,
			Reason:  reason,
			Message: err.Error(),
		},
		&configv1.ClusterOperatorStatusCondition{
			Type:   configv1.OperatorProgressing,
			Status: configv1.ConditionFalse,
			Reason: "Failing",
		},
		&configv1.ClusterOperatorStatusCondition{
			Type:   configv1.OperatorAvailable,
			Status: configv1.ConditionFalse,
			Reason: "Failing",
		},
	)
}

func (status *StatusManager) SetDaemonSets(daemonSets []types.NamespacedName) {
	status.daemonSets = daemonSets
}

func (status *StatusManager) SetDeployments(deployments []types.NamespacedName) {
	status.deployments = deployments
}

// SetFromPods sets the operator status to Failing, Progressing, or Available, based on
// the current status of the manager's DaemonSets and Deployments. However, this is a
// no-op if the StatusManager is currently marked as failing due to a configuration error.
func (status *StatusManager) SetFromPods() error {
	if status.configFailure {
		return nil
	}

	progressing := []string{}

	for _, dsName := range status.daemonSets {
		ns := &corev1.Namespace{}
		if err := status.client.Get(context.TODO(), types.NamespacedName{Name: dsName.Namespace}, ns); err != nil {
			if errors.IsNotFound(err) {
				return status.SetFailing("NoNamespace", fmt.Errorf("Namespace %q does not exist", dsName.Namespace))
			} else {
				return status.SetFailing("InternalError", err)
			}
		}

		ds := &appsv1.DaemonSet{}
		if err := status.client.Get(context.TODO(), dsName, ds); err != nil {
			if errors.IsNotFound(err) {
				return status.SetFailing("NoDaemonSet", fmt.Errorf("DaemonSet %q does not exist", dsName.String()))
			} else {
				return status.SetFailing("InternalError", err)
			}
		}

		if ds.Status.NumberUnavailable > 0 {
			progressing = append(progressing, fmt.Sprintf("DaemonSet %q is not available (awaiting %d nodes)", dsName.String(), ds.Status.NumberUnavailable))
		} else if ds.Status.NumberAvailable == 0 {
			progressing = append(progressing, fmt.Sprintf("DaemonSet %q is not yet scheduled on any nodes", dsName.String()))
		}
	}

	for _, depName := range status.deployments {
		ns := &corev1.Namespace{}
		if err := status.client.Get(context.TODO(), types.NamespacedName{Name: depName.Namespace}, ns); err != nil {
			if errors.IsNotFound(err) {
				return status.SetFailing("NoNamespace", fmt.Errorf("Namespace %q does not exist", depName.Namespace))
			} else {
				return status.SetFailing("InternalError", err)
			}
		}

		dep := &appsv1.Deployment{}
		if err := status.client.Get(context.TODO(), depName, dep); err != nil {
			if errors.IsNotFound(err) {
				return status.SetFailing("NoDeployment", fmt.Errorf("Deployment %q does not exist", depName.String()))
			} else {
				return status.SetFailing("InternalError", err)
			}
		}

		if dep.Status.UnavailableReplicas > 0 {
			progressing = append(progressing, fmt.Sprintf("Deployment %q is not available (awaiting %d nodes)", depName.String(), dep.Status.UnavailableReplicas))
		} else if dep.Status.AvailableReplicas == 0 {
			progressing = append(progressing, fmt.Sprintf("Deployment %q is not yet scheduled on any nodes", depName.String()))
		}
	}

	if len(progressing) > 0 {
		return status.Set(
			&configv1.ClusterOperatorStatusCondition{
				Type:   configv1.OperatorFailing,
				Status: configv1.ConditionFalse,
			},
			&configv1.ClusterOperatorStatusCondition{
				Type:    configv1.OperatorProgressing,
				Status:  configv1.ConditionTrue,
				Reason:  "Deploying",
				Message: strings.Join(progressing, "\n"),
			},
			&configv1.ClusterOperatorStatusCondition{
				Type:    configv1.OperatorAvailable,
				Status:  configv1.ConditionFalse,
				Reason:  "Deploying",
				Message: strings.Join(progressing, "\n"),
			},
		)
	} else {
		return status.Set(
			&configv1.ClusterOperatorStatusCondition{
				Type:   configv1.OperatorFailing,
				Status: configv1.ConditionFalse,
			},
			&configv1.ClusterOperatorStatusCondition{
				Type:   configv1.OperatorProgressing,
				Status: configv1.ConditionFalse,
			},
			&configv1.ClusterOperatorStatusCondition{
				Type:   configv1.OperatorAvailable,
				Status: configv1.ConditionTrue,
			},
		)
	}
}
