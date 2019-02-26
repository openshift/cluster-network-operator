package clusteroperator

import (
	"context"
	"testing"

	configv1 "github.com/openshift/api/config/v1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func init() {
	configv1.AddToScheme(scheme.Scheme)
	appsv1.AddToScheme(scheme.Scheme)
}

func getCO(client client.Client, name string) (*configv1.ClusterOperator, error) {
	co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: name}}
	err := client.Get(context.TODO(), types.NamespacedName{Name: name}, co)
	return co, err
}

func TestStatusManagerSet(t *testing.T) {
	client := fake.NewFakeClient()
	status := NewStatusManager(client, "testing", "1.2.3")

	co, err := getCO(client, "testing")
	if !errors.IsNotFound(err) {
		t.Fatalf("unexpected error (expected Not Found): %v", err)
	}

	condFail := configv1.ClusterOperatorStatusCondition{
		Type:    configv1.OperatorFailing,
		Status:  configv1.ConditionTrue,
		Reason:  "Reason",
		Message: "Message",
	}
	err = status.Set(&condFail)
	if err != nil {
		t.Fatalf("error setting status: %v", err)
	}

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !ConditionsEqual(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{condFail}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	condProgress := configv1.ClusterOperatorStatusCondition{
		Type:    configv1.OperatorProgressing,
		Status:  configv1.ConditionTrue,
	}
	err = status.Set(&condProgress)
	if err != nil {
		t.Fatalf("error setting status: %v", err)
	}

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !ConditionsEqual(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{condFail, condProgress}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	condNoFail := configv1.ClusterOperatorStatusCondition{
		Type:    configv1.OperatorFailing,
		Status:  configv1.ConditionFalse,
	}
	err = status.Set(&condNoFail)
	if err != nil {
		t.Fatalf("error setting status: %v", err)
	}

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !ConditionsEqual(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{condNoFail, condProgress}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	condNoProgress := configv1.ClusterOperatorStatusCondition{
		Type:    configv1.OperatorProgressing,
		Status:  configv1.ConditionFalse,
	}
	condAvailable := configv1.ClusterOperatorStatusCondition{
		Type:    configv1.OperatorAvailable,
		Status:  configv1.ConditionTrue,
	}
	err = status.Set(&condNoProgress, &condAvailable)
	if err != nil {
		t.Fatalf("error setting status: %v", err)
	}

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !ConditionsEqual(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{condNoFail, condNoProgress, condAvailable}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}
}

// A weaker version of ConditionsEqual: basically "the parts of newConditions that are set match
// what's in oldConditions, but there might also be other stuff in oldConditions"
func conditionsInclude(oldConditions, newConditions []configv1.ClusterOperatorStatusCondition) bool {
	for _, newCondition := range newConditions {
		foundMatchingCondition := false

		for _, oldCondition := range oldConditions {
			if newCondition.Type != oldCondition.Type || newCondition.Status != oldCondition.Status {
				continue
			}
			if newCondition.Reason != "" && newCondition.Reason != oldCondition.Reason {
				return false
			}
			if newCondition.Message != "" && newCondition.Message != oldCondition.Message {
				return false
			}
			foundMatchingCondition = true
			break
		}

		if !foundMatchingCondition {
			return false
		}
	}

	return true
}

func TestStatusManagerSetFromDaemonSets(t *testing.T) {
	client := fake.NewFakeClient()
	status := NewStatusManager(client, "testing", "1.2.3")

	status.SetDaemonSets([]types.NamespacedName{
		types.NamespacedName{Namespace: "one", Name: "alpha"},
		types.NamespacedName{Namespace: "two", Name: "beta"},
	})

	err := status.SetFromPods()
	if err != nil {
		t.Fatalf("error setting status: %v", err)
	}

	co, err := getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
		configv1.ClusterOperatorStatusCondition{
			Type:    configv1.OperatorFailing,
			Status:  configv1.ConditionTrue,
			Reason:  "NoNamespace",
		},
		configv1.ClusterOperatorStatusCondition{
			Type:    configv1.OperatorProgressing,
			Status:  configv1.ConditionFalse,
			Reason:  "Failing",
		},
		configv1.ClusterOperatorStatusCondition{
			Type:    configv1.OperatorAvailable,
			Status:  configv1.ConditionFalse,
			Reason:  "Failing",
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	// Create namespaces, try again
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "one"}}
	err = client.Create(context.TODO(), ns)
	if err != nil {
		t.Fatalf("error creating Namespace: %v", err)
	}
	ns = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "two"}}
	err = client.Create(context.TODO(), ns)
	if err != nil {
		t.Fatalf("error creating Namespace: %v", err)
	}

	err = status.SetFromPods()
	if err != nil {
		t.Fatalf("error setting status: %v", err)
	}

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
		configv1.ClusterOperatorStatusCondition{
			Type:    configv1.OperatorFailing,
			Status:  configv1.ConditionTrue,
			Reason:  "NoDaemonSet",
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	// Create minimal DaemonSets
	dsA := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: "one", Name: "alpha"}}
	err = client.Create(context.TODO(), dsA)
	if err != nil {
		t.Fatalf("error creating DaemonSet: %v", err)
	}
	dsB := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: "two", Name: "beta"}}
	err = client.Create(context.TODO(), dsB)
	if err != nil {
		t.Fatalf("error creating DaemonSet: %v", err)
	}
	err = status.SetFromPods()
	if err != nil {
		t.Fatalf("error setting status: %v", err)
	}

	// Since the DaemonSet.Status reports no pods Available, the status should be Progressing
	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
		configv1.ClusterOperatorStatusCondition{
			Type:   configv1.OperatorFailing,
			Status: configv1.ConditionFalse,
		},
		configv1.ClusterOperatorStatusCondition{
			Type:   configv1.OperatorProgressing,
			Status: configv1.ConditionTrue,
			Reason: "Deploying",
		},
		configv1.ClusterOperatorStatusCondition{
			Type:   configv1.OperatorAvailable,
			Status: configv1.ConditionFalse,
			Reason: "Deploying",
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	// Update to report expected deployment size
	dsANodes := int32(1)
	dsBNodes := int32(3)
	dsA.Status.NumberUnavailable = dsANodes
	dsB.Status.NumberUnavailable = dsBNodes

	// Now start "deploying"
	for dsA.Status.NumberUnavailable > 0 || dsB.Status.NumberUnavailable > 0 {
		err = client.Update(context.TODO(), dsA)
		if err != nil {
			t.Fatalf("error updating DaemonSet: %v", err)
		}
		err = client.Update(context.TODO(), dsB)
		if err != nil {
			t.Fatalf("error updating DaemonSet: %v", err)
		}
		err = status.SetFromPods()
		if err != nil {
			t.Fatalf("error setting status: %v", err)
		}

		co, err = getCO(client, "testing")
		if err != nil {
			t.Fatalf("error getting ClusterOperator: %v", err)
		}
		if !conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
			configv1.ClusterOperatorStatusCondition{
				Type:   configv1.OperatorProgressing,
				Status: configv1.ConditionTrue,
			},
			configv1.ClusterOperatorStatusCondition{
				Type:   configv1.OperatorAvailable,
				Status: configv1.ConditionFalse,
			},
		}) {
			t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
		}

		if dsA.Status.NumberUnavailable > 0 {
			dsA.Status.NumberUnavailable--
			dsA.Status.NumberAvailable++
		}
		if dsB.Status.NumberUnavailable > 0 {
			dsB.Status.NumberUnavailable--
			dsB.Status.NumberAvailable++
		}
	}

	// Final update, should be fully-available now
	if dsA.Status.NumberAvailable != dsANodes || dsA.Status.NumberUnavailable != 0 || dsB.Status.NumberAvailable != dsBNodes || dsB.Status.NumberUnavailable != 0 {
		t.Fatalf("assertion failed: %#v, %#v", dsA, dsB)
	}

	err = client.Update(context.TODO(), dsA)
	if err != nil {
		t.Fatalf("error updating DaemonSet: %v", err)
	}
	err = client.Update(context.TODO(), dsB)
	if err != nil {
		t.Fatalf("error updating DaemonSet: %v", err)
	}
	err = status.SetFromPods()
	if err != nil {
		t.Fatalf("error setting status: %v", err)
	}

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
		configv1.ClusterOperatorStatusCondition{
			Type:   configv1.OperatorFailing,
			Status: configv1.ConditionFalse,
		},
		configv1.ClusterOperatorStatusCondition{
			Type:   configv1.OperatorProgressing,
			Status: configv1.ConditionFalse,
		},
		configv1.ClusterOperatorStatusCondition{
			Type:   configv1.OperatorAvailable,
			Status: configv1.ConditionTrue,
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}
}

func TestStatusManagerSetFromPods(t *testing.T) {
	client := fake.NewFakeClient()
	status := NewStatusManager(client, "testing", "1.2.3")

	status.SetDeployments([]types.NamespacedName{
		types.NamespacedName{Namespace: "one", Name: "alpha"},
	})

	err := status.SetFromPods()
	if err != nil {
		t.Fatalf("error setting status: %v", err)
	}

	co, err := getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
		configv1.ClusterOperatorStatusCondition{
			Type:    configv1.OperatorFailing,
			Status:  configv1.ConditionTrue,
			Reason:  "NoNamespace",
		},
		configv1.ClusterOperatorStatusCondition{
			Type:    configv1.OperatorProgressing,
			Status:  configv1.ConditionFalse,
			Reason:  "Failing",
		},
		configv1.ClusterOperatorStatusCondition{
			Type:    configv1.OperatorAvailable,
			Status:  configv1.ConditionFalse,
			Reason:  "Failing",
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	// Create namespace, try again
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "one"}}
	err = client.Create(context.TODO(), ns)
	if err != nil {
		t.Fatalf("error creating Namespace: %v", err)
	}

	err = status.SetFromPods()
	if err != nil {
		t.Fatalf("error setting status: %v", err)
	}

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
		configv1.ClusterOperatorStatusCondition{
			Type:    configv1.OperatorFailing,
			Status:  configv1.ConditionTrue,
			Reason:  "NoDeployment",
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	// Create a Deployment that isn't the one we're looking for
	depB := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "one", Name: "beta"},
		Status: appsv1.DeploymentStatus{
			UnavailableReplicas: 1,
		},
	}
	err = client.Create(context.TODO(), depB)
	if err != nil {
		t.Fatalf("error creating Deployment: %v", err)
	}
	err = status.SetFromPods()
	if err != nil {
		t.Fatalf("error setting status: %v", err)
	}
	if !conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
		configv1.ClusterOperatorStatusCondition{
			Type:    configv1.OperatorFailing,
			Status:  configv1.ConditionTrue,
			Reason:  "NoDeployment",
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	// Create minimal Deployment
	depA := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "one", Name: "alpha"}}
	err = client.Create(context.TODO(), depA)
	if err != nil {
		t.Fatalf("error creating Deployment: %v", err)
	}
	err = status.SetFromPods()
	if err != nil {
		t.Fatalf("error setting status: %v", err)
	}

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
		configv1.ClusterOperatorStatusCondition{
			Type:   configv1.OperatorFailing,
			Status: configv1.ConditionFalse,
		},
		configv1.ClusterOperatorStatusCondition{
			Type:   configv1.OperatorProgressing,
			Status: configv1.ConditionTrue,
			Reason: "Deploying",
		},
		configv1.ClusterOperatorStatusCondition{
			Type:   configv1.OperatorAvailable,
			Status: configv1.ConditionFalse,
			Reason: "Deploying",
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	// Update to report expected deployment size
	depA.Status.UnavailableReplicas = 0
	depA.Status.AvailableReplicas = 1
	err = client.Update(context.TODO(), depA)
	if err != nil {
		t.Fatalf("error updating Deployment: %v", err)
	}
	err = status.SetFromPods()
	if err != nil {
		t.Fatalf("error setting status: %v", err)
	}

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
		configv1.ClusterOperatorStatusCondition{
			Type:   configv1.OperatorFailing,
			Status: configv1.ConditionFalse,
		},
		configv1.ClusterOperatorStatusCondition{
			Type:   configv1.OperatorProgressing,
			Status: configv1.ConditionFalse,
		},
		configv1.ClusterOperatorStatusCondition{
			Type:   configv1.OperatorAvailable,
			Status: configv1.ConditionTrue,
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	// Add more expected pods
	status.SetDeployments([]types.NamespacedName{
		types.NamespacedName{Namespace: "one", Name: "alpha"},
		types.NamespacedName{Namespace: "one", Name: "beta"},
	})
	status.SetDaemonSets([]types.NamespacedName{
		types.NamespacedName{Namespace: "one", Name: "gamma"},
	})

	err = status.SetFromPods()
	if err != nil {
		t.Fatalf("error setting status: %v", err)
	}

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
		configv1.ClusterOperatorStatusCondition{
			Type:    configv1.OperatorFailing,
			Status:  configv1.ConditionTrue,
			Reason:  "NoDaemonSet",
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "one", Name: "gamma"},
		Status: appsv1.DaemonSetStatus{
			NumberUnavailable: 0,
			NumberAvailable:   1,
		},
	}
	err = client.Create(context.TODO(), ds)
	if err != nil {
		t.Fatalf("error creating DaemonSet: %v", err)
	}
	err = status.SetFromPods()
	if err != nil {
		t.Fatalf("error setting status: %v", err)
	}

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	// We should now be progressing because both Deployments and the DaemonSet exist,
	// but depB is still incomplete.
	if !conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
		configv1.ClusterOperatorStatusCondition{
			Type:   configv1.OperatorFailing,
			Status: configv1.ConditionFalse,
		},
		configv1.ClusterOperatorStatusCondition{
			Type:   configv1.OperatorProgressing,
			Status: configv1.ConditionTrue,
		},
		configv1.ClusterOperatorStatusCondition{
			Type:   configv1.OperatorAvailable,
			Status: configv1.ConditionFalse,
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	depB.Status.UnavailableReplicas = 0
	depB.Status.AvailableReplicas = 1
	err = client.Update(context.TODO(), depB)
	if err != nil {
		t.Fatalf("error updating Deployment: %v", err)
	}
	err = status.SetFromPods()
	if err != nil {
		t.Fatalf("error setting status: %v", err)
	}

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
		configv1.ClusterOperatorStatusCondition{
			Type:   configv1.OperatorFailing,
			Status: configv1.ConditionFalse,
		},
		configv1.ClusterOperatorStatusCondition{
			Type:   configv1.OperatorProgressing,
			Status: configv1.ConditionFalse,
		},
		configv1.ClusterOperatorStatusCondition{
			Type:   configv1.OperatorAvailable,
			Status: configv1.ConditionTrue,
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}
}
