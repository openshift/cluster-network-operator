package v1alpha1helpers

import (
	"context"
	"sort"
	"time"

	operatorcontrolplanev1alpha1 "github.com/openshift/api/operatorcontrolplane/v1alpha1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
)

func SetPodNetworkConnectivityCheckCondition(conditions *[]operatorcontrolplanev1alpha1.PodNetworkConnectivityCheckCondition, newCondition operatorcontrolplanev1alpha1.PodNetworkConnectivityCheckCondition) {
	if conditions == nil {
		conditions = &[]operatorcontrolplanev1alpha1.PodNetworkConnectivityCheckCondition{}
	}
	var existingCondition *operatorcontrolplanev1alpha1.PodNetworkConnectivityCheckCondition
	for i := range *conditions {
		if (*conditions)[i].Type == newCondition.Type {
			existingCondition = &(*conditions)[i]
		}
	}
	if existingCondition == nil {
		newCondition.LastTransitionTime = metav1.NewTime(time.Now())
		*conditions = append(*conditions, newCondition)
		return
	}
	if existingCondition.Status != newCondition.Status {
		existingCondition.Status = newCondition.Status
		existingCondition.LastTransitionTime = metav1.NewTime(time.Now())
	}
	existingCondition.Reason = newCondition.Reason
	existingCondition.Message = newCondition.Message
}

type UpdateStatusFunc func(status *operatorcontrolplanev1alpha1.PodNetworkConnectivityCheckStatus)

type PodNetworkConnectivityCheckClient interface {
	Get(name string) (*operatorcontrolplanev1alpha1.PodNetworkConnectivityCheck, error)
	UpdateStatus(ctx context.Context, podNetworkConnectivityCheck *operatorcontrolplanev1alpha1.PodNetworkConnectivityCheck, opts metav1.UpdateOptions) (*operatorcontrolplanev1alpha1.PodNetworkConnectivityCheck, error)
}

func UpdateStatus(ctx context.Context, client PodNetworkConnectivityCheckClient, name string, updateFuncs ...UpdateStatusFunc) (*operatorcontrolplanev1alpha1.PodNetworkConnectivityCheckStatus, bool, error) {
	updated := false
	var updatedStatus *operatorcontrolplanev1alpha1.PodNetworkConnectivityCheckStatus
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		check, err := client.Get(name)
		if err != nil {
			return err
		}
		oldStatus := check.Status
		newStatus := oldStatus.DeepCopy()
		for _, update := range updateFuncs {
			update(newStatus)
		}
		if equality.Semantic.DeepEqual(oldStatus, newStatus) {
			updatedStatus = newStatus
			return nil
		}
		check, err = client.Get(name)
		if err != nil {
			return err
		}
		check.Status = *newStatus
		updatedCheck, err := client.UpdateStatus(ctx, check, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
		updatedStatus = &updatedCheck.Status
		updated = true
		return err
	})
	return updatedStatus, updated, err
}

func AddSuccessLogEntry(newLogEntry operatorcontrolplanev1alpha1.LogEntry) UpdateStatusFunc {
	return func(status *operatorcontrolplanev1alpha1.PodNetworkConnectivityCheckStatus) {
		status.Successes = appendLogEntry(status.Successes, newLogEntry)
	}
}

func AddFailureLogEntry(newLogEntry operatorcontrolplanev1alpha1.LogEntry) UpdateStatusFunc {
	return func(status *operatorcontrolplanev1alpha1.PodNetworkConnectivityCheckStatus) {
		status.Failures = appendLogEntry(status.Failures, newLogEntry)
	}
}

// appendLogEntry adds log entry in descending time order and limited the total number of log entries
func appendLogEntry(log []operatorcontrolplanev1alpha1.LogEntry, entries ...operatorcontrolplanev1alpha1.LogEntry) []operatorcontrolplanev1alpha1.LogEntry {
	log = append(entries, log...)
	sort.SliceStable(log, func(i, j int) bool {
		return log[i].Start.After(log[j].Start.Time)
	})
	if len(log) > 10 {
		return log[:10]
	}
	return log
}
