package clusteroperator

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/sirupsen/logrus"

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
		log.Printf("failed to get clusteroperator %s: %v", status.name, err)
		return fmt.Errorf("failed to get clusteroperator %s: %v", status.name, err)
	}

	oldConditions := co.Status.Conditions
	for _, condition := range conditions {
		co.Status.Conditions = SetStatusCondition(co.Status.Conditions, condition)
	}

	if isNotFound {
		if err := status.client.Create(context.TODO(), co); err != nil {
			log.Printf("failed to create clusteroperator %s: %v", co.Name, err)
			return fmt.Errorf("failed to create clusteroperator %s: %v", co.Name, err)
		}
		logrus.Infof("created clusteroperator: %#v", co)
	} else {
		if !ConditionsEqual(oldConditions, co.Status.Conditions) {
			err = status.client.Status().Update(context.TODO(), co)
			if err != nil {
				log.Printf("failed to update clusteroperator %s: %v", co.Name, err)
				return fmt.Errorf("failed to update clusteroperator %s: %v", co.Name, err)
			}
			logrus.Infof("updated clusteroperator: %#v", co)
 		}
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
			Type:    configv1.OperatorFailing,
			Status:  configv1.ConditionFalse,
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
			Type:    configv1.OperatorProgressing,
			Status:  configv1.ConditionFalse,
			Reason:  "Failing",
		},
		&configv1.ClusterOperatorStatusCondition{
			Type:    configv1.OperatorAvailable,
			Status:  configv1.ConditionFalse,
			Reason:  "Failing",
		},
	)
}

// SetFromDaemonSets sets the operator status to Failing, Progressing, or Available, based
// on the current status of the indicated DaemonSets. However, this is a no-op if the
// StatusManager is currently marked as failing due to a configuration error.
func (status *StatusManager) SetFromDaemonSets(daemonSets []types.NamespacedName) error {
	if status.configFailure {
		return nil
	}

	progressing := []string{}

	for _, dsName := range daemonSets {
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
