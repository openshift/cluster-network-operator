package v1alpha1helpers

import (
	"fmt"
	"testing"

	"github.com/openshift/api/operatorcontrolplane/v1alpha1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSetPodNetworkConnectivityCheckCondition(t *testing.T) {

	testCases := []struct {
		conditions         []v1alpha1.PodNetworkConnectivityCheckCondition
		condition          v1alpha1.PodNetworkConnectivityCheckCondition
		expectedConditions []v1alpha1.PodNetworkConnectivityCheckCondition
	}{
		{
			condition: v1alpha1.PodNetworkConnectivityCheckCondition{
				Type:    v1alpha1.Reachable,
				Status:  metav1.ConditionTrue,
				Reason:  "A",
				Message: "Msg",
			},
			expectedConditions: []v1alpha1.PodNetworkConnectivityCheckCondition{
				{
					Type:    v1alpha1.Reachable,
					Status:  metav1.ConditionTrue,
					Reason:  "A",
					Message: "Msg",
				},
			},
		},
		{
			conditions: []v1alpha1.PodNetworkConnectivityCheckCondition{
				{
					Type:    v1alpha1.Reachable,
					Status:  metav1.ConditionTrue,
					Reason:  "A",
					Message: "Msg",
				},
			},
			condition: v1alpha1.PodNetworkConnectivityCheckCondition{
				Type:    v1alpha1.Reachable,
				Status:  metav1.ConditionFalse,
				Reason:  "B",
				Message: "MsgB",
			},
			expectedConditions: []v1alpha1.PodNetworkConnectivityCheckCondition{
				{
					Type:    v1alpha1.Reachable,
					Status:  metav1.ConditionFalse,
					Reason:  "B",
					Message: "MsgB",
				},
			},
		},
	}

	for i, tc := range testCases {
		t.Run(fmt.Sprintf("%05d", i), func(t *testing.T) {
			check := v1alpha1.PodNetworkConnectivityCheck{
				Status: v1alpha1.PodNetworkConnectivityCheckStatus{
					Conditions: tc.conditions,
				},
			}
			SetPodNetworkConnectivityCheckCondition(&check.Status.Conditions, tc.condition)
			for i := range check.Status.Conditions {
				check.Status.Conditions[i].LastTransitionTime = metav1.Time{}
			}
			assert.Equal(t, tc.expectedConditions, check.Status.Conditions)
		})
	}
}

func assertEqualCondition(t *testing.T, expected, actual v1alpha1.PodNetworkConnectivityCheckCondition) {
	assert.Equal(t, expected.Type, actual.Type)
	assert.Equal(t, expected.Status, actual.Status)
	assert.Equal(t, expected.Reason, actual.Reason)
	assert.Equal(t, expected.Message, actual.Message)
}
