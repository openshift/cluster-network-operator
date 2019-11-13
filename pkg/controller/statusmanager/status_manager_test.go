package statusmanager

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/library-go/pkg/config/clusteroperator/v1helpers"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

// Tests that the parts of newConditions that are set match what's in oldConditions (but
// doesn't look at anything else in oldConditions)
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

func conditionsEqual(oldConditions, newConditions []configv1.ClusterOperatorStatusCondition) bool {
	return conditionsInclude(oldConditions, newConditions) && conditionsInclude(newConditions, oldConditions)
}

type fakeRESTMapper struct {
	kindForInput schema.GroupVersionResource
}

func (f *fakeRESTMapper) KindFor(resource schema.GroupVersionResource) (schema.GroupVersionKind, error) {
	f.kindForInput = resource
	return schema.GroupVersionKind{
		Group:   "test",
		Version: "test",
		Kind:    "test"}, nil
}

func (f *fakeRESTMapper) KindsFor(resource schema.GroupVersionResource) ([]schema.GroupVersionKind, error) {
	return nil, nil
}

func (f *fakeRESTMapper) ResourceFor(input schema.GroupVersionResource) (schema.GroupVersionResource, error) {
	return schema.GroupVersionResource{}, nil
}

func (f *fakeRESTMapper) ResourcesFor(input schema.GroupVersionResource) ([]schema.GroupVersionResource, error) {
	return nil, nil
}

func (f *fakeRESTMapper) RESTMapping(gk schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	return nil, nil
}

func (f *fakeRESTMapper) RESTMappings(gk schema.GroupKind, versions ...string) ([]*meta.RESTMapping, error) {
	return nil, nil
}

func (f *fakeRESTMapper) ResourceSingularizer(resource string) (singular string, err error) {
	return "", nil
}

func TestStatusManager_set(t *testing.T) {
	client := fake.NewFakeClient()
	mapper := &fakeRESTMapper{}
	status := New(client, mapper, "testing", "1.2.3")

	co, err := getCO(client, "testing")
	if !errors.IsNotFound(err) {
		t.Fatalf("unexpected error (expected Not Found): %v", err)
	}

	condUpdate := configv1.ClusterOperatorStatusCondition{
		Type:   configv1.OperatorUpgradeable,
		Status: configv1.ConditionTrue,
	}

	condFail := configv1.ClusterOperatorStatusCondition{
		Type:    configv1.OperatorDegraded,
		Status:  configv1.ConditionTrue,
		Reason:  "Reason",
		Message: "Message",
	}
	status.set(false, condFail)

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}

	if !conditionsEqual(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{condFail, condUpdate}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	condProgress := configv1.ClusterOperatorStatusCondition{
		Type:   configv1.OperatorProgressing,
		Status: configv1.ConditionUnknown,
	}
	status.set(false, condProgress)

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsEqual(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{condFail, condUpdate, condProgress}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	condNoFail := configv1.ClusterOperatorStatusCondition{
		Type:   configv1.OperatorDegraded,
		Status: configv1.ConditionFalse,
	}
	status.set(false, condNoFail)

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsEqual(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{condNoFail, condUpdate, condProgress}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	condNoProgress := configv1.ClusterOperatorStatusCondition{
		Type:   configv1.OperatorProgressing,
		Status: configv1.ConditionFalse,
	}
	condAvailable := configv1.ClusterOperatorStatusCondition{
		Type:   configv1.OperatorAvailable,
		Status: configv1.ConditionTrue,
	}
	status.set(false, condNoProgress, condAvailable)

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsEqual(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{condNoFail, condUpdate, condNoProgress, condAvailable}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}

	obj := &uns.Unstructured{}
	gvk := schema.GroupVersionKind{
		Group:   "test",
		Version: "test",
		Kind:    "test",
	}
	obj.SetGroupVersionKind(gvk)
	obj.SetName("current")
	err = status.client.Create(context.TODO(), obj)
	if err != nil {
		t.Fatalf("error creating not rendered object: %v", err)
	}

	co.Status.RelatedObjects = []configv1.ObjectReference{
		{
			Group:    "test",
			Resource: "test",
			Name:     "current",
		},
	}
	status.relatedObjects = []configv1.ObjectReference{
		{
			Group:    "test",
			Resource: "test",
			Name:     "related",
		},
	}
	status.deleteRelatedObjectsNotRendered(co)
	err = status.client.Get(context.TODO(), types.NamespacedName{Name: "current"}, obj)
	if err == nil {
		t.Fatalf("unexpected related object in ClusterOperator object was not deleted")
	}
}

func TestStatusManagerSetDegraded(t *testing.T) {
	client := fake.NewFakeClient()
	mapper := &fakeRESTMapper{}
	status := New(client, mapper, "testing", "1.2.3")

	co, err := getCO(client, "testing")
	if !errors.IsNotFound(err) {
		t.Fatalf("unexpected error (expected Not Found): %v", err)
	}

	condUpdate := configv1.ClusterOperatorStatusCondition{
		Type:   configv1.OperatorUpgradeable,
		Status: configv1.ConditionTrue,
	}
	condFailCluster := configv1.ClusterOperatorStatusCondition{
		Type:   configv1.OperatorDegraded,
		Status: configv1.ConditionTrue,
		Reason: "Cluster",
	}
	condFailOperator := configv1.ClusterOperatorStatusCondition{
		Type:   configv1.OperatorDegraded,
		Status: configv1.ConditionTrue,
		Reason: "Operator",
	}
	condFailPods := configv1.ClusterOperatorStatusCondition{
		Type:   configv1.OperatorDegraded,
		Status: configv1.ConditionTrue,
		Reason: "Pods",
	}

	// Initial failure status
	status.SetDegraded(OperatorConfig, "Operator", "")
	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsEqual(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{condFailOperator, condUpdate}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	// Setting a higher-level status should override it
	status.SetDegraded(ClusterConfig, "Cluster", "")
	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsEqual(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{condFailCluster, condUpdate}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	// Setting a lower-level status should be ignored
	status.SetDegraded(PodDeployment, "Pods", "")
	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsEqual(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{condFailCluster, condUpdate}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	// Clearing an unseen status should have no effect
	status.SetNotDegraded(OperatorConfig)
	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsEqual(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{condFailCluster, condUpdate}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	// Clearing the currently-seen status should reveal the higher-level status
	status.SetNotDegraded(ClusterConfig)
	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsEqual(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{condFailPods, condUpdate}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	// Clearing all failing statuses should result in not failing
	status.SetNotDegraded(PodDeployment)
	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !v1helpers.IsStatusConditionFalse(co.Status.Conditions, configv1.OperatorDegraded) && !conditionsEqual(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{condUpdate}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}
}

func TestStatusManagerSetFromDaemonSets(t *testing.T) {
	client := fake.NewFakeClient()
	mapper := &fakeRESTMapper{}
	status := New(client, mapper, "testing", "1.2.3")

	status.SetDaemonSets([]types.NamespacedName{
		{Namespace: "one", Name: "alpha"},
		{Namespace: "two", Name: "beta"},
	})

	status.SetFromPods()
	co, err := getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
		{
			Type:   configv1.OperatorProgressing,
			Status: configv1.ConditionTrue,
			Reason: "Deploying",
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	// Create minimal DaemonSets
	dsA := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: "one", Name: "alpha", Generation: 1}}
	err = client.Create(context.TODO(), dsA)
	if err != nil {
		t.Fatalf("error creating DaemonSet: %v", err)
	}
	dsB := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: "two", Name: "beta", Generation: 1}}
	err = client.Create(context.TODO(), dsB)
	if err != nil {
		t.Fatalf("error creating DaemonSet: %v", err)
	}
	status.SetFromPods()

	// Since the DaemonSet.Status reports no pods Available, the status should be Progressing
	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
		{
			Type:   configv1.OperatorDegraded,
			Status: configv1.ConditionFalse,
		},
		{
			Type:   configv1.OperatorProgressing,
			Status: configv1.ConditionTrue,
			Reason: "Deploying",
		},
		{
			Type:   configv1.OperatorUpgradeable,
			Status: configv1.ConditionTrue,
		},
		{
			Type:   configv1.OperatorAvailable,
			Status: configv1.ConditionFalse,
			Reason: "Startup",
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	progressingTS := metav1.Now()
	if cond := v1helpers.FindStatusCondition(co.Status.Conditions, configv1.OperatorProgressing); cond != nil {
		if cond.LastTransitionTime.IsZero() {
			t.Fatalf("progressing transition time was zero")
		}
		progressingTS = cond.LastTransitionTime
	} else {
		// unreachable
		t.Fatalf("Progressing condition unexpectedly missing")
	}

	// Update to report expected deployment size
	dsANodes := int32(1)
	dsBNodes := int32(3)
	dsA.Status.NumberUnavailable = dsANodes
	dsA.Status.ObservedGeneration = 1
	dsB.Status.NumberUnavailable = dsBNodes
	dsB.Status.ObservedGeneration = 1

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
		status.SetFromPods()

		co, err = getCO(client, "testing")
		if err != nil {
			t.Fatalf("error getting ClusterOperator: %v", err)
		}
		if !conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
			{
				Type:   configv1.OperatorProgressing,
				Status: configv1.ConditionTrue,
			},
			{
				Type:   configv1.OperatorUpgradeable,
				Status: configv1.ConditionTrue,
			},
			{
				Type:   configv1.OperatorAvailable,
				Status: configv1.ConditionFalse,
			},
		}) {
			t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
		}

		// Validate that the transition time was not bumped
		if cond := v1helpers.FindStatusCondition(co.Status.Conditions, configv1.OperatorProgressing); cond != nil {
			if !progressingTS.Equal(&cond.LastTransitionTime) {
				t.Fatalf("Progressing LastTransitionTime changed unnecessarily")
			}
		} else {
			// unreachable
			t.Fatalf("Progressing condition unexpectedly missing")
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
	time.Sleep(1 * time.Second) // minimum transition time fidelity
	status.SetFromPods()

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
		{
			Type:   configv1.OperatorDegraded,
			Status: configv1.ConditionFalse,
		},
		{
			Type:   configv1.OperatorProgressing,
			Status: configv1.ConditionFalse,
		},
		{
			Type:   configv1.OperatorUpgradeable,
			Status: configv1.ConditionTrue,
		},
		{
			Type:   configv1.OperatorAvailable,
			Status: configv1.ConditionTrue,
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	// Validate that the transition time was bumped
	if cond := v1helpers.FindStatusCondition(co.Status.Conditions, configv1.OperatorProgressing); cond != nil {
		if progressingTS.Equal(&cond.LastTransitionTime) {
			t.Fatalf("Progressing LastTransitionTime didn't change when Progressing -> false")
		}
	} else {
		// unreachable
		t.Fatalf("Progressing condition unexpectedly missing")
	}

	// Now, bump the generation of one of the daemonsets, and verify
	// that we enter Progressing state but otherwise stay Available
	dsA.Generation = 2
	err = client.Update(context.TODO(), dsA)
	if err != nil {
		t.Fatalf("error updating DaemonSet: %v", err)
	}
	status.SetFromPods()

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
		{
			Type:   configv1.OperatorDegraded,
			Status: configv1.ConditionFalse,
		},
		{
			Type:   configv1.OperatorProgressing,
			Status: configv1.ConditionTrue,
		},
		{
			Type:   configv1.OperatorUpgradeable,
			Status: configv1.ConditionTrue,
		},
		{
			Type:   configv1.OperatorAvailable,
			Status: configv1.ConditionTrue,
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	// update the daemonset status to mimic a kubernetes rollout
	// Taken from a live v1.16 apiserver
	// Transition: observedGeneration -> 2, UpdatedNumberScheduled -> 0
	dsA.Status = appsv1.DaemonSetStatus{
		CurrentNumberScheduled: 1,
		DesiredNumberScheduled: 1,
		NumberMisscheduled:     0,
		NumberReady:            1,
		ObservedGeneration:     2,
	}
	err = client.Update(context.TODO(), dsA)
	if err != nil {
		t.Fatalf("error updating DaemonSet: %v", err)
	}
	status.SetFromPods()

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
		{
			Type:   configv1.OperatorDegraded,
			Status: configv1.ConditionFalse,
		},
		{
			Type:   configv1.OperatorProgressing,
			Status: configv1.ConditionTrue,
		},
		{
			Type:   configv1.OperatorUpgradeable,
			Status: configv1.ConditionTrue,
		},
		{
			Type:   configv1.OperatorAvailable,
			Status: configv1.ConditionTrue,
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	// Next update: Ready -> 0 Unavailable -> 1
	dsA.Status = appsv1.DaemonSetStatus{
		CurrentNumberScheduled: 1,
		DesiredNumberScheduled: 1,
		NumberMisscheduled:     0,
		NumberReady:            0,
		NumberUnavailable:      1,
		ObservedGeneration:     2,
	}
	err = client.Update(context.TODO(), dsA)
	if err != nil {
		t.Fatalf("error updating DaemonSet: %v", err)
	}
	status.SetFromPods()

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
		{
			Type:   configv1.OperatorDegraded,
			Status: configv1.ConditionFalse,
		},
		{
			Type:   configv1.OperatorProgressing,
			Status: configv1.ConditionTrue,
		},
		{
			Type:   configv1.OperatorUpgradeable,
			Status: configv1.ConditionTrue,
		},
		{
			Type:   configv1.OperatorAvailable,
			Status: configv1.ConditionTrue,
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	// Next update: updatedNumberScheduled -> 1
	dsA.Status = appsv1.DaemonSetStatus{
		CurrentNumberScheduled: 1,
		DesiredNumberScheduled: 1,
		NumberMisscheduled:     0,
		NumberReady:            0,
		NumberUnavailable:      1,
		ObservedGeneration:     2,
		UpdatedNumberScheduled: 1,
	}
	err = client.Update(context.TODO(), dsA)
	if err != nil {
		t.Fatalf("error updating DaemonSet: %v", err)
	}

	t0 := time.Now()
	time.Sleep(time.Second / 10)
	status.SetFromPods()

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
		{
			Type:   configv1.OperatorDegraded,
			Status: configv1.ConditionFalse,
		},
		{
			Type:   configv1.OperatorProgressing,
			Status: configv1.ConditionTrue,
		},
		{
			Type:   configv1.OperatorUpgradeable,
			Status: configv1.ConditionTrue,
		},
		{
			Type:   configv1.OperatorAvailable,
			Status: configv1.ConditionTrue,
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	// See that the last pod state is reasonable
	ps := getLastPodState(t, client, "testing")
	nsn := types.NamespacedName{Namespace: "one", Name: "alpha"}
	found := false
	for _, ds := range ps.DaemonsetStates {
		if ds.NamespacedName == nsn {
			found = true
			if !ds.LastChangeTime.After(t0) {
				t.Fatalf("Expected %s to be after %s", ds.LastChangeTime, t0)
			}
			if !reflect.DeepEqual(dsA.Status, ds.LastSeenStatus) {
				t.Fatal("expected cached status to equal last seen status")
			}

			break
		}
	}
	if !found {
		t.Fatalf("Didn't find %s in pod state", nsn)
	}

	// intermission: set the last-update-time to an hour ago, make sure we
	// set degraded (because the rollout is hung)
	ps = getLastPodState(t, client, "testing")
	for idx, ds := range ps.DaemonsetStates {
		if ds.NamespacedName == nsn {
			ps.DaemonsetStates[idx].LastChangeTime = time.Now().Add(-time.Hour)
			break
		}
	}
	setLastPodState(t, client, "testing", ps)
	status.SetFromPods()

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
		{
			Type:   configv1.OperatorDegraded,
			Status: configv1.ConditionTrue,
		},
		{
			Type:   configv1.OperatorProgressing,
			Status: configv1.ConditionTrue,
		},
		{
			Type:   configv1.OperatorUpgradeable,
			Status: configv1.ConditionTrue,
		},
		{
			Type:   configv1.OperatorAvailable,
			Status: configv1.ConditionTrue,
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	// done: numberReady -> 1, numberUnavailable -> 0
	dsA.Status = appsv1.DaemonSetStatus{
		CurrentNumberScheduled: 1,
		DesiredNumberScheduled: 1,
		NumberAvailable:        1,
		NumberMisscheduled:     0,
		NumberReady:            1,
		ObservedGeneration:     2,
		UpdatedNumberScheduled: 1,
	}
	err = client.Update(context.TODO(), dsA)
	if err != nil {
		t.Fatalf("error updating DaemonSet: %v", err)
	}
	status.SetFromPods()

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
		{
			Type:   configv1.OperatorDegraded,
			Status: configv1.ConditionFalse,
		},
		{
			Type:   configv1.OperatorProgressing,
			Status: configv1.ConditionFalse,
		},
		{
			Type:   configv1.OperatorUpgradeable,
			Status: configv1.ConditionTrue,
		},
		{
			Type:   configv1.OperatorAvailable,
			Status: configv1.ConditionTrue,
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	// see that the pod state is sensible
}

func TestStatusManagerSetFromDeployments(t *testing.T) {
	client := fake.NewFakeClient()
	mapper := &fakeRESTMapper{}
	status := New(client, mapper, "testing", "1.2.3")

	status.SetDeployments([]types.NamespacedName{
		{Namespace: "one", Name: "alpha"},
	})

	status.SetFromPods()

	co, err := getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
		{
			Type:   configv1.OperatorProgressing,
			Status: configv1.ConditionTrue,
			Reason: "Deploying",
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
	status.SetFromPods()

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
		{
			Type:   configv1.OperatorProgressing,
			Status: configv1.ConditionTrue,
			Reason: "Deploying",
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
	status.SetFromPods()

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
		{
			Type:   configv1.OperatorDegraded,
			Status: configv1.ConditionFalse,
		},
		{
			Type:   configv1.OperatorProgressing,
			Status: configv1.ConditionTrue,
			Reason: "Deploying",
		},
		{
			Type:   configv1.OperatorUpgradeable,
			Status: configv1.ConditionTrue,
		},
		{
			Type:   configv1.OperatorAvailable,
			Status: configv1.ConditionFalse,
			Reason: "Startup",
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
	status.SetFromPods()

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
		{
			Type:   configv1.OperatorDegraded,
			Status: configv1.ConditionFalse,
		},
		{
			Type:   configv1.OperatorProgressing,
			Status: configv1.ConditionFalse,
		},
		{
			Type:   configv1.OperatorUpgradeable,
			Status: configv1.ConditionTrue,
		},
		{
			Type:   configv1.OperatorAvailable,
			Status: configv1.ConditionTrue,
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	// Add more expected pods
	status.SetDeployments([]types.NamespacedName{
		{Namespace: "one", Name: "alpha"},
		{Namespace: "one", Name: "beta"},
	})
	status.SetDaemonSets([]types.NamespacedName{
		{Namespace: "one", Name: "gamma"},
	})

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

	t0 := time.Now()
	time.Sleep(time.Second / 10)
	status.SetFromPods()

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	// We should now be progressing because both Deployments and the DaemonSet exist,
	// but depB is still incomplete. We're still Available though because we were
	// Available before
	if !conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
		{
			Type:   configv1.OperatorDegraded,
			Status: configv1.ConditionFalse,
		},
		{
			Type:   configv1.OperatorProgressing,
			Status: configv1.ConditionTrue,
		},
		{
			Type:   configv1.OperatorUpgradeable,
			Status: configv1.ConditionTrue,
		},
		{
			Type:   configv1.OperatorAvailable,
			Status: configv1.ConditionTrue,
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

	ps := getLastPodState(t, client, "testing")
	// see that the pod state is sensible
	nsn := types.NamespacedName{Namespace: "one", Name: "beta"}
	found := false
	for _, ds := range ps.DeploymentStates {
		if ds.NamespacedName == nsn {
			found = true
			if !ds.LastChangeTime.After(t0) {
				t.Fatalf("Expected %s to be after %s", ds.LastChangeTime, t0)
			}
			if !reflect.DeepEqual(depB.Status, ds.LastSeenStatus) {
				t.Fatal("expected cached status to equal last seen status")
			}

			break
		}
	}
	if !found {
		t.Fatalf("Didn't find %s in pod state", nsn)
	}

	// intermission: set back last-seen times by an hour, see that we mark
	// as hung
	ps = getLastPodState(t, client, "testing")
	for idx, ds := range ps.DeploymentStates {
		if ds.NamespacedName == nsn {
			ps.DeploymentStates[idx].LastChangeTime = time.Now().Add(-time.Hour)
			break
		}
	}
	setLastPodState(t, client, "testing", ps)
	status.SetFromPods()

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	// We should still be Progressing, since nothing else has changed, but
	// now we're also Degraded, since rollout has not made any progress
	if !conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
		{
			Type:   configv1.OperatorDegraded,
			Status: configv1.ConditionTrue,
		},
		{
			Type:   configv1.OperatorProgressing,
			Status: configv1.ConditionTrue,
		},
		{
			Type:   configv1.OperatorUpgradeable,
			Status: configv1.ConditionTrue,
		},
		{
			Type:   configv1.OperatorAvailable,
			Status: configv1.ConditionTrue,
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

	status.SetFromPods()

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(co.Status.Conditions, []configv1.ClusterOperatorStatusCondition{
		{
			Type:   configv1.OperatorDegraded,
			Status: configv1.ConditionFalse,
		},
		{
			Type:   configv1.OperatorProgressing,
			Status: configv1.ConditionFalse,
		},
		{
			Type:   configv1.OperatorUpgradeable,
			Status: configv1.ConditionTrue,
		},
		{
			Type:   configv1.OperatorAvailable,
			Status: configv1.ConditionTrue,
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", co.Status.Conditions)
	}

}

func getLastPodState(t *testing.T, client client.Client, name string) podState {
	t.Helper()
	co, err := getCO(client, name)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(co.Annotations)

	ps := podState{}
	if err := json.Unmarshal([]byte(co.Annotations[lastSeenAnnotation]), &ps); err != nil {
		t.Fatal(err)
	}

	return ps
}

// sets *all* last-seen-times back an hour
func setLastPodState(t *testing.T, client client.Client, name string, ps podState) {
	t.Helper()
	co, err := getCO(client, name)
	if err != nil {
		t.Fatal(err)
	}

	lsBytes, err := json.Marshal(ps)
	if err != nil {
		t.Fatal(err)
	}
	co.Annotations[lastSeenAnnotation] = string(lsBytes)
	err = client.Update(context.Background(), co)
	if err != nil {
		t.Fatal(err)
	}
}
