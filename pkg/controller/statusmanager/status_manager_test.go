package statusmanager

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/client/fake"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/library-go/pkg/operator/v1helpers"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"

	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

//nolint:errcheck
func init() {
	configv1.AddToScheme(scheme.Scheme)
	operv1.AddToScheme(scheme.Scheme)
	appsv1.AddToScheme(scheme.Scheme)
}

func getCO(client cnoclient.Client, name string) (*configv1.ClusterOperator, error) {
	co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: name}}
	err := client.ClientFor("").CRClient().Get(context.TODO(), types.NamespacedName{Name: name}, co)
	return co, err
}

func getOC(client cnoclient.Client) (*operv1.Network, error) {
	oc := &operv1.Network{ObjectMeta: metav1.ObjectMeta{Name: names.OPERATOR_CONFIG}}
	err := client.ClientFor("").CRClient().Get(context.TODO(), types.NamespacedName{Name: names.OPERATOR_CONFIG}, oc)
	return oc, err
}

func getStatuses(client cnoclient.Client, name string) (*configv1.ClusterOperator, *operv1.Network, error) {
	co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: name}}
	err := client.ClientFor("").CRClient().Get(context.TODO(), types.NamespacedName{Name: name}, co)
	if err != nil {
		return nil, nil, err
	}
	oc := &operv1.Network{ObjectMeta: metav1.ObjectMeta{Name: names.OPERATOR_CONFIG}}
	err = client.ClientFor("").CRClient().Get(context.TODO(), types.NamespacedName{Name: names.OPERATOR_CONFIG}, oc)
	return co, oc, err
}

func set(t *testing.T, client cnoclient.Client, obj crclient.Object) {
	t.Helper()
	err := client.ClientFor("").CRClient().Update(context.TODO(), obj)
	if apierrors.IsNotFound(err) {
		err = client.ClientFor("").CRClient().Create(context.TODO(), obj)

	}
	if err != nil {
		t.Fatalf("Failed to set: %v", err)
	}
}

// sl: labels that all status-havers have
var sl map[string]string = map[string]string{names.GenerateStatusLabel: ""}

// Tests that the parts of newConditions that are set match what's in oldConditions (but
// doesn't look at anything else in oldConditions)
func conditionsInclude(oldConditions, newConditions []operv1.OperatorCondition) bool {
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

func conditionsEqual(oldConditions, newConditions []operv1.OperatorCondition) bool {
	return conditionsInclude(oldConditions, newConditions) && conditionsInclude(newConditions, oldConditions)
}

func TestStatusManager_set(t *testing.T) {
	client := fake.NewFakeClient()
	status := New(client, "testing")

	// No operator config yet; should reflect this in the cluster operator
	status.set(false)

	co, err := getCO(client, "testing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(co.Status.Conditions) != 1 || co.Status.Conditions[0].Reason != "NoOperConfig" {
		t.Fatalf("Expected a single Condition with reason NoOperConfig, got %v", co.Status.Conditions)
	}

	// make the network.operator object
	no := &operv1.Network{ObjectMeta: metav1.ObjectMeta{Name: names.OPERATOR_CONFIG}}
	set(t, client, no)

	condUpdate := operv1.OperatorCondition{
		Type:   operv1.OperatorStatusTypeUpgradeable,
		Status: operv1.ConditionTrue,
	}

	condFail := operv1.OperatorCondition{
		Type:    operv1.OperatorStatusTypeDegraded,
		Status:  operv1.ConditionTrue,
		Reason:  "Reason",
		Message: "Message",
	}
	status.set(false, condFail)

	co, oc, err := getStatuses(client, "testing")
	if err != nil {
		t.Fatalf("error getting network.operator: %v", err)
	}

	if !conditionsEqual(oc.Status.Conditions, []operv1.OperatorCondition{condFail, condUpdate}) {
		t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
	}
	if len(co.Status.Versions) > 0 {
		t.Fatalf("Status.Versions unexpectedly already set: %#v", co.Status.Versions)
	}

	condProgress := operv1.OperatorCondition{
		Type:   operv1.OperatorStatusTypeProgressing,
		Status: operv1.ConditionUnknown,
	}
	status.set(false, condProgress)

	oc, err = getOC(client)
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsEqual(oc.Status.Conditions, []operv1.OperatorCondition{condFail, condUpdate, condProgress}) {
		t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
	}

	condNoFail := operv1.OperatorCondition{
		Type:   operv1.OperatorStatusTypeDegraded,
		Status: operv1.ConditionFalse,
	}
	status.set(false, condNoFail)

	oc, err = getOC(client)
	if err != nil {
		t.Fatalf("error getting network.operator: %v", err)
	}
	if !conditionsEqual(oc.Status.Conditions, []operv1.OperatorCondition{condNoFail, condUpdate, condProgress}) {
		t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
	}

	condNoProgress := operv1.OperatorCondition{
		Type:   operv1.OperatorStatusTypeProgressing,
		Status: operv1.ConditionFalse,
	}
	condAvailable := operv1.OperatorCondition{
		Type:   operv1.OperatorStatusTypeAvailable,
		Status: operv1.ConditionTrue,
	}
	status.set(true, condNoProgress, condAvailable)

	oc, err = getOC(client)
	if err != nil {
		t.Fatalf("error getting network.operator: %v", err)
	}
	if !conditionsEqual(oc.Status.Conditions, []operv1.OperatorCondition{condNoFail, condUpdate, condNoProgress, condAvailable}) {
		t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
	}

	co, err = getCO(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}

	// Check that conditions are correctly mirrored to the ClusterOperator object
	if len(co.Status.Conditions) != 4 {
		t.Fatal("Expected status to be mirrored to the ClusterOperator object")
	}
	// And that Versions is now set
	if len(co.Status.Versions) != 1 {
		t.Fatalf("unexpected Status.Versions: %#v", co.Status.Versions)
	}

	obj := &uns.Unstructured{}
	gvk := schema.GroupVersionKind{
		Group:   "test",
		Version: "test",
		Kind:    "test",
	}
	obj.SetGroupVersionKind(gvk)
	obj.SetName("current")
	set(t, client, obj)

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
	err = status.client.ClientFor("").CRClient().Get(context.TODO(), types.NamespacedName{Name: "current"}, obj)
	if err == nil {
		t.Fatalf("unexpected related object in ClusterOperator object was not deleted")
	}
}

func TestStatusManagerSetDegraded(t *testing.T) {
	client := fake.NewFakeClient()
	status := New(client, "testing")

	_, err := getOC(client)
	if !errors.IsNotFound(err) {
		t.Fatalf("unexpected error (expected Not Found): %v", err)
	}
	no := &operv1.Network{ObjectMeta: metav1.ObjectMeta{Name: names.OPERATOR_CONFIG}}
	set(t, client, no)

	condUpdate := operv1.OperatorCondition{
		Type:   operv1.OperatorStatusTypeUpgradeable,
		Status: operv1.ConditionTrue,
	}
	condFailCluster := operv1.OperatorCondition{
		Type:   operv1.OperatorStatusTypeDegraded,
		Status: operv1.ConditionTrue,
		Reason: "Cluster",
	}
	condFailOperator := operv1.OperatorCondition{
		Type:   operv1.OperatorStatusTypeDegraded,
		Status: operv1.ConditionTrue,
		Reason: "Operator",
	}
	condFailPods := operv1.OperatorCondition{
		Type:   operv1.OperatorStatusTypeDegraded,
		Status: operv1.ConditionTrue,
		Reason: "Pods",
	}

	// Initial failure status
	status.SetDegraded(OperatorConfig, "Operator", "")
	oc, err := getOC(client)
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsEqual(oc.Status.Conditions, []operv1.OperatorCondition{condFailOperator, condUpdate}) {
		t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
	}

	// Setting a higher-level status should override it
	status.SetDegraded(ClusterConfig, "Cluster", "")
	oc, err = getOC(client)
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsEqual(oc.Status.Conditions, []operv1.OperatorCondition{condFailCluster, condUpdate}) {
		t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
	}

	// Setting a lower-level status should be ignored
	status.SetDegraded(PodDeployment, "Pods", "")
	oc, err = getOC(client)
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsEqual(oc.Status.Conditions, []operv1.OperatorCondition{condFailCluster, condUpdate}) {
		t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
	}

	// Clearing an unseen status should have no effect
	status.SetNotDegraded(OperatorConfig)
	oc, err = getOC(client)
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsEqual(oc.Status.Conditions, []operv1.OperatorCondition{condFailCluster, condUpdate}) {
		t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
	}

	// Clearing the currently-seen status should reveal the higher-level status
	status.SetNotDegraded(ClusterConfig)
	oc, err = getOC(client)
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsEqual(oc.Status.Conditions, []operv1.OperatorCondition{condFailPods, condUpdate}) {
		t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
	}

	// Clearing all failing statuses should result in not failing
	status.SetNotDegraded(PodDeployment)
	oc, err = getOC(client)
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !v1helpers.IsOperatorConditionFalse(oc.Status.Conditions, operv1.OperatorStatusTypeDegraded) && !conditionsEqual(oc.Status.Conditions, []operv1.OperatorCondition{condUpdate}) {
		t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
	}
}

func TestStatusManagerSetFromDaemonSets(t *testing.T) {
	client := fake.NewFakeClient()
	status := New(client, "testing")
	setFakeListers(status)
	no := &operv1.Network{ObjectMeta: metav1.ObjectMeta{Name: names.OPERATOR_CONFIG}}
	set(t, client, no)

	status.SetFromPods()
	co, oc, err := getStatuses(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(oc.Status.Conditions, []operv1.OperatorCondition{
		{
			Type:   operv1.OperatorStatusTypeProgressing,
			Status: operv1.ConditionTrue,
			Reason: "Deploying",
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
	}
	if len(co.Status.Versions) > 0 {
		t.Fatalf("Status.Versions unexpectedly already set: %#v", co.Status.Versions)
	}

	// Create minimal DaemonSets
	dsA := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "one", Name: "alpha", Generation: 1, Labels: sl},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "alpha"},
			},
		},
	}
	set(t, client, dsA)
	dsB := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "two", Name: "beta", Generation: 1, Labels: sl},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "beta"},
			},
		},
	}
	set(t, client, dsB)
	status.SetFromPods()

	// Since the DaemonSet.Status reports no pods Available, the status should be Progressing
	co, oc, err = getStatuses(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(oc.Status.Conditions, []operv1.OperatorCondition{
		{
			Type:   operv1.OperatorStatusTypeDegraded,
			Status: operv1.ConditionFalse,
		},
		{
			Type:   operv1.OperatorStatusTypeProgressing,
			Status: operv1.ConditionTrue,
			Reason: "Deploying",
		},
		{
			Type:   operv1.OperatorStatusTypeUpgradeable,
			Status: operv1.ConditionTrue,
		},
		{
			Type:   operv1.OperatorStatusTypeAvailable,
			Status: operv1.ConditionFalse,
			Reason: "Startup",
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
	}
	if len(co.Status.Versions) > 0 {
		t.Fatalf("Status.Versions unexpectedly already set: %#v", co.Status.Versions)
	}

	progressingTS := metav1.Now()
	if cond := v1helpers.FindOperatorCondition(oc.Status.Conditions, operv1.OperatorStatusTypeProgressing); cond != nil {
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
		set(t, client, dsA)
		set(t, client, dsB)
		status.SetFromPods()

		co, oc, err = getStatuses(client, "testing")
		if err != nil {
			t.Fatalf("error getting ClusterOperator: %v", err)
		}
		if !conditionsInclude(oc.Status.Conditions, []operv1.OperatorCondition{
			{
				Type:   operv1.OperatorStatusTypeProgressing,
				Status: operv1.ConditionTrue,
			},
			{
				Type:   operv1.OperatorStatusTypeUpgradeable,
				Status: operv1.ConditionTrue,
			},
			{
				Type:   operv1.OperatorStatusTypeAvailable,
				Status: operv1.ConditionFalse,
			},
		}) {
			t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
		}
		if len(co.Status.Versions) > 0 {
			t.Fatalf("Status.Versions unexpectedly already set: %#v", co.Status.Versions)
		}

		// Validate that the transition time was not bumped
		if cond := v1helpers.FindOperatorCondition(oc.Status.Conditions, operv1.OperatorStatusTypeProgressing); cond != nil {
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

	set(t, client, dsA)
	set(t, client, dsB)
	time.Sleep(1 * time.Second) // minimum transition time fidelity
	status.SetFromPods()

	co, oc, err = getStatuses(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(oc.Status.Conditions, []operv1.OperatorCondition{
		{
			Type:   operv1.OperatorStatusTypeDegraded,
			Status: operv1.ConditionFalse,
		},
		{
			Type:   operv1.OperatorStatusTypeProgressing,
			Status: operv1.ConditionFalse,
		},
		{
			Type:   operv1.OperatorStatusTypeUpgradeable,
			Status: operv1.ConditionTrue,
		},
		{
			Type:   operv1.OperatorStatusTypeAvailable,
			Status: operv1.ConditionTrue,
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
	}
	if len(co.Status.Versions) != 1 {
		t.Fatalf("unexpected Status.Versions: %#v", co.Status.Versions)
	}

	// Validate that the transition time was bumped
	if cond := v1helpers.FindOperatorCondition(oc.Status.Conditions, operv1.OperatorStatusTypeProgressing); cond != nil {
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
	set(t, client, dsA)
	status.SetFromPods()

	co, oc, err = getStatuses(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(oc.Status.Conditions, []operv1.OperatorCondition{
		{
			Type:   operv1.OperatorStatusTypeDegraded,
			Status: operv1.ConditionFalse,
		},
		{
			Type:   operv1.OperatorStatusTypeProgressing,
			Status: operv1.ConditionTrue,
		},
		{
			Type:   operv1.OperatorStatusTypeUpgradeable,
			Status: operv1.ConditionTrue,
		},
		{
			Type:   operv1.OperatorStatusTypeAvailable,
			Status: operv1.ConditionTrue,
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
	}
	if len(co.Status.Versions) != 1 {
		t.Fatalf("unexpected Status.Versions: %#v", co.Status.Versions)
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
	set(t, client, dsA)
	status.SetFromPods()

	co, oc, err = getStatuses(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(oc.Status.Conditions, []operv1.OperatorCondition{
		{
			Type:   operv1.OperatorStatusTypeDegraded,
			Status: operv1.ConditionFalse,
		},
		{
			Type:   operv1.OperatorStatusTypeProgressing,
			Status: operv1.ConditionTrue,
		},
		{
			Type:   operv1.OperatorStatusTypeUpgradeable,
			Status: operv1.ConditionTrue,
		},
		{
			Type:   operv1.OperatorStatusTypeAvailable,
			Status: operv1.ConditionTrue,
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
	}
	if len(co.Status.Versions) != 1 {
		t.Fatalf("unexpected Status.Versions: %#v", co.Status.Versions)
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
	set(t, client, dsA)
	status.SetFromPods()

	co, oc, err = getStatuses(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(oc.Status.Conditions, []operv1.OperatorCondition{
		{
			Type:   operv1.OperatorStatusTypeDegraded,
			Status: operv1.ConditionFalse,
		},
		{
			Type:   operv1.OperatorStatusTypeProgressing,
			Status: operv1.ConditionTrue,
		},
		{
			Type:   operv1.OperatorStatusTypeUpgradeable,
			Status: operv1.ConditionTrue,
		},
		{
			Type:   operv1.OperatorStatusTypeAvailable,
			Status: operv1.ConditionTrue,
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
	}
	if len(co.Status.Versions) != 1 {
		t.Fatalf("unexpected Status.Versions: %#v", co.Status.Versions)
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
	set(t, client, dsA)

	t0 := time.Now()
	time.Sleep(time.Second / 10)
	status.SetFromPods()

	co, oc, err = getStatuses(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(oc.Status.Conditions, []operv1.OperatorCondition{
		{
			Type:   operv1.OperatorStatusTypeDegraded,
			Status: operv1.ConditionFalse,
		},
		{
			Type:   operv1.OperatorStatusTypeProgressing,
			Status: operv1.ConditionTrue,
		},
		{
			Type:   operv1.OperatorStatusTypeUpgradeable,
			Status: operv1.ConditionTrue,
		},
		{
			Type:   operv1.OperatorStatusTypeAvailable,
			Status: operv1.ConditionTrue,
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
	}
	if len(co.Status.Versions) != 1 {
		t.Fatalf("unexpected Status.Versions: %#v", co.Status.Versions)
	}

	// See that the last pod state is reasonable
	ps := getLastPodState(t, client, "testing")
	nsn := ClusteredName{Namespace: "one", Name: "alpha"}
	found := false
	for _, ds := range ps.DaemonsetStates {
		if ds.ClusteredName == nsn {
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
		if ds.ClusteredName == nsn {
			ps.DaemonsetStates[idx].LastChangeTime = time.Now().Add(-time.Hour)
			break
		}
	}
	setLastPodState(t, client, "testing", ps)
	status.SetFromPods()

	co, oc, err = getStatuses(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(oc.Status.Conditions, []operv1.OperatorCondition{
		{
			Type:   operv1.OperatorStatusTypeDegraded,
			Status: operv1.ConditionTrue,
		},
		{
			Type:   operv1.OperatorStatusTypeProgressing,
			Status: operv1.ConditionTrue,
		},
		{
			Type:   operv1.OperatorStatusTypeUpgradeable,
			Status: operv1.ConditionTrue,
		},
		{
			Type:   operv1.OperatorStatusTypeAvailable,
			Status: operv1.ConditionTrue,
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
	}
	if len(co.Status.Versions) != 1 {
		t.Fatalf("unexpected Status.Versions: %#v", co.Status.Versions)
	}

	// check hung annotation is set (also, need to refresh objects since they were updated)
	err = client.ClientFor("").CRClient().Get(context.TODO(), types.NamespacedName{Namespace: "one", Name: "alpha"}, dsA)
	if err != nil {
		t.Fatalf("error getting DaemonSet: %v", err)
	}

	if _, set := dsA.Annotations[names.RolloutHungAnnotation]; !set {
		t.Fatalf("Expected rollout-hung annotation, but was missing")
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
	set(t, client, dsA)
	status.SetFromPods()

	// see that the pod state is sensible
	co, oc, err = getStatuses(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(oc.Status.Conditions, []operv1.OperatorCondition{
		{
			Type:   operv1.OperatorStatusTypeDegraded,
			Status: operv1.ConditionFalse,
		},
		{
			Type:   operv1.OperatorStatusTypeProgressing,
			Status: operv1.ConditionFalse,
		},
		{
			Type:   operv1.OperatorStatusTypeUpgradeable,
			Status: operv1.ConditionTrue,
		},
		{
			Type:   operv1.OperatorStatusTypeAvailable,
			Status: operv1.ConditionTrue,
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
	}
	if len(co.Status.Versions) != 1 {
		t.Fatalf("unexpected Status.Versions: %#v", co.Status.Versions)
	}

	err = client.ClientFor("").CRClient().Get(context.TODO(), types.NamespacedName{Namespace: "one", Name: "alpha"}, dsA)
	if err != nil {
		t.Fatalf("error getting DaemonSet: %v", err)
	}

	if val, set := dsA.GetAnnotations()[names.RolloutHungAnnotation]; set {
		t.Fatalf("Expected no rollout-hung annotation, but was present %s", val)
	}

	// Test non-critical DaemonSets
	dsNC := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  "one",
			Name:       "non-critical",
			Generation: 1,
			Annotations: map[string]string{
				names.NonCriticalAnnotation: "",
			},
			Labels: sl,
		},
		Status: appsv1.DaemonSetStatus{
			ObservedGeneration: 1,
			NumberUnavailable:  1,
		},
	}
	set(t, client, dsNC)
	status.SetFromPods()

	// We should now be Progressing, but not un-Available
	co, oc, err = getStatuses(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(oc.Status.Conditions, []operv1.OperatorCondition{
		{
			Type:   operv1.OperatorStatusTypeDegraded,
			Status: operv1.ConditionFalse,
		},
		{
			Type:   operv1.OperatorStatusTypeProgressing,
			Status: operv1.ConditionTrue,
		},
		{
			Type:   operv1.OperatorStatusTypeUpgradeable,
			Status: operv1.ConditionTrue,
		},
		{
			Type:   operv1.OperatorStatusTypeAvailable,
			Status: operv1.ConditionTrue,
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
	}
	if len(co.Status.Versions) != 1 {
		t.Fatalf("unexpected Status.Versions: %#v", co.Status.Versions)
	}

	// Mark the rollout as hung; should not change anything
	ps = getLastPodState(t, client, "testing")
	nsn = ClusteredName{Namespace: "one", Name: "non-critical"}
	for idx, ds := range ps.DaemonsetStates {
		if ds.ClusteredName == nsn {
			ps.DaemonsetStates[idx].LastChangeTime = time.Now().Add(-time.Hour)
			break
		}
	}
	setLastPodState(t, client, "testing", ps)
	status.SetFromPods()

	co, oc, err = getStatuses(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(oc.Status.Conditions, []operv1.OperatorCondition{
		{
			Type:   operv1.OperatorStatusTypeDegraded,
			Status: operv1.ConditionFalse,
		},
		{
			Type:   operv1.OperatorStatusTypeProgressing,
			Status: operv1.ConditionTrue,
		},
		{
			Type:   operv1.OperatorStatusTypeUpgradeable,
			Status: operv1.ConditionTrue,
		},
		{
			Type:   operv1.OperatorStatusTypeAvailable,
			Status: operv1.ConditionTrue,
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
	}
	if len(co.Status.Versions) != 1 {
		t.Fatalf("unexpected Status.Versions: %#v", co.Status.Versions)
	}

	// Now update
	dsNC.Status.NumberAvailable = 1
	dsNC.Status.NumberUnavailable = 0
	dsNC.Status.DesiredNumberScheduled = 1
	dsNC.Status.UpdatedNumberScheduled = 1
	set(t, client, dsNC)
	status.SetFromPods()

	co, oc, err = getStatuses(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(oc.Status.Conditions, []operv1.OperatorCondition{
		{
			Type:   operv1.OperatorStatusTypeDegraded,
			Status: operv1.ConditionFalse,
		},
		{
			Type:   operv1.OperatorStatusTypeProgressing,
			Status: operv1.ConditionFalse,
		},
		{
			Type:   operv1.OperatorStatusTypeUpgradeable,
			Status: operv1.ConditionTrue,
		},
		{
			Type:   operv1.OperatorStatusTypeAvailable,
			Status: operv1.ConditionTrue,
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
	}
	if len(co.Status.Versions) != 1 {
		t.Fatalf("unexpected Status.Versions: %#v", co.Status.Versions)
	}
}

func TestStatusManagerSetFromDeployments(t *testing.T) {
	client := fake.NewFakeClient()
	status := New(client, "testing")
	setFakeListers(status)
	no := &operv1.Network{ObjectMeta: metav1.ObjectMeta{Name: names.OPERATOR_CONFIG}}
	set(t, client, no)

	status.SetFromPods()

	co, oc, err := getStatuses(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(oc.Status.Conditions, []operv1.OperatorCondition{
		{
			Type:   operv1.OperatorStatusTypeProgressing,
			Status: operv1.ConditionTrue,
			Reason: "Deploying",
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
	}
	if len(co.Status.Versions) > 0 {
		t.Fatalf("Status.Versions unexpectedly already set: %#v", co.Status.Versions)
	}

	// Create a Deployment that isn't the one we're looking for

	// Create minimal Deployment
	depA := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "one", Name: "alpha", Labels: sl}}
	set(t, client, depA)

	status.SetFromPods()

	co, oc, err = getStatuses(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(oc.Status.Conditions, []operv1.OperatorCondition{
		{
			Type:   operv1.OperatorStatusTypeDegraded,
			Status: operv1.ConditionFalse,
		},
		{
			Type:   operv1.OperatorStatusTypeProgressing,
			Status: operv1.ConditionTrue,
			Reason: "Deploying",
		},
		{
			Type:   operv1.OperatorStatusTypeUpgradeable,
			Status: operv1.ConditionTrue,
		},
		{
			Type:   operv1.OperatorStatusTypeAvailable,
			Status: operv1.ConditionFalse,
			Reason: "Startup",
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
	}
	if len(co.Status.Versions) > 0 {
		t.Fatalf("Status.Versions unexpectedly already set: %#v", co.Status.Versions)
	}

	// Update to report expected deployment size
	depA.Status.UnavailableReplicas = 0
	depA.Status.AvailableReplicas = 1
	set(t, client, depA)
	status.SetFromPods()

	co, oc, err = getStatuses(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(oc.Status.Conditions, []operv1.OperatorCondition{
		{
			Type:   operv1.OperatorStatusTypeDegraded,
			Status: operv1.ConditionFalse,
		},
		{
			Type:   operv1.OperatorStatusTypeProgressing,
			Status: operv1.ConditionFalse,
		},
		{
			Type:   operv1.OperatorStatusTypeUpgradeable,
			Status: operv1.ConditionTrue,
		},
		{
			Type:   operv1.OperatorStatusTypeAvailable,
			Status: operv1.ConditionTrue,
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
	}
	if len(co.Status.Versions) != 1 {
		t.Fatalf("unexpected Status.Versions: %#v", co.Status.Versions)
	}

	depB := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "one", Name: "beta", Labels: sl},
		Status: appsv1.DeploymentStatus{
			UnavailableReplicas: 1,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "gamma"},
			},
		},
	}
	set(t, client, depB)

	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "one", Name: "gamma", Labels: sl},
		Status: appsv1.DaemonSetStatus{
			NumberUnavailable: 0,
			NumberAvailable:   1,
		},
	}
	set(t, client, ds)

	t0 := time.Now()
	time.Sleep(time.Second / 10)
	status.SetFromPods()

	co, oc, err = getStatuses(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	// We should now be progressing because both Deployments and the DaemonSet exist,
	// but depB is still incomplete. We're still Available though because we were
	// Available before
	if !conditionsInclude(oc.Status.Conditions, []operv1.OperatorCondition{
		{
			Type:   operv1.OperatorStatusTypeDegraded,
			Status: operv1.ConditionFalse,
		},
		{
			Type:   operv1.OperatorStatusTypeProgressing,
			Status: operv1.ConditionTrue,
		},
		{
			Type:   operv1.OperatorStatusTypeUpgradeable,
			Status: operv1.ConditionTrue,
		},
		{
			Type:   operv1.OperatorStatusTypeAvailable,
			Status: operv1.ConditionTrue,
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
	}
	if len(co.Status.Versions) != 1 {
		t.Fatalf("unexpected Status.Versions: %#v", co.Status.Versions)
	}

	ps := getLastPodState(t, client, "testing")
	// see that the pod state is sensible
	nsn := ClusteredName{Namespace: "one", Name: "beta"}
	found := false
	for _, ds := range ps.DeploymentStates {
		if ds.ClusteredName == nsn {
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
		if ds.ClusteredName == nsn {
			ps.DeploymentStates[idx].LastChangeTime = time.Now().Add(-time.Hour)
			break
		}
	}
	setLastPodState(t, client, "testing", ps)
	status.SetFromPods()

	co, oc, err = getStatuses(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	// We should still be Progressing, since nothing else has changed, but
	// now we're also Degraded, since rollout has not made any progress
	if !conditionsInclude(oc.Status.Conditions, []operv1.OperatorCondition{
		{
			Type:   operv1.OperatorStatusTypeDegraded,
			Status: operv1.ConditionTrue,
		},
		{
			Type:   operv1.OperatorStatusTypeProgressing,
			Status: operv1.ConditionTrue,
		},
		{
			Type:   operv1.OperatorStatusTypeUpgradeable,
			Status: operv1.ConditionTrue,
		},
		{
			Type:   operv1.OperatorStatusTypeAvailable,
			Status: operv1.ConditionTrue,
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
	}
	if len(co.Status.Versions) != 1 {
		t.Fatalf("unexpected Status.Versions: %#v", co.Status.Versions)
	}

	err = client.ClientFor("").CRClient().Get(context.TODO(), types.NamespacedName{Namespace: depB.Namespace, Name: depB.Name}, depB)
	if err != nil {
		t.Fatalf("error getting Deployment: %v", err)
	}

	depB.Status.UnavailableReplicas = 0
	depB.Status.AvailableReplicas = 1
	set(t, client, depB)
	status.SetFromPods()

	co, oc, err = getStatuses(client, "testing")
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(oc.Status.Conditions, []operv1.OperatorCondition{
		{
			Type:   operv1.OperatorStatusTypeDegraded,
			Status: operv1.ConditionFalse,
		},
		{
			Type:   operv1.OperatorStatusTypeProgressing,
			Status: operv1.ConditionFalse,
		},
		{
			Type:   operv1.OperatorStatusTypeUpgradeable,
			Status: operv1.ConditionTrue,
		},
		{
			Type:   operv1.OperatorStatusTypeAvailable,
			Status: operv1.ConditionTrue,
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
	}
	if len(co.Status.Versions) != 1 {
		t.Fatalf("unexpected Status.Versions: %#v", co.Status.Versions)
	}
}

func getLastPodState(t *testing.T, client cnoclient.Client, name string) podState {
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
func setLastPodState(t *testing.T, client cnoclient.Client, name string, ps podState) {
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
	err = client.ClientFor("").CRClient().Update(context.Background(), co)
	if err != nil {
		t.Fatal(err)
	}
}

func TestStatusManagerCheckCrashLoopBackOffPods(t *testing.T) {
	client := fake.NewFakeClient()
	status := New(client, "testing")
	setFakeListers(status)
	no := &operv1.Network{ObjectMeta: metav1.ObjectMeta{Name: names.OPERATOR_CONFIG}}
	set(t, client, no)

	dsA := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "one",
			Name:      "alpha",
			Labels:    sl,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "alpha"},
			},
		},
	}
	set(t, client, dsA)

	dsB := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "two",
			Name:      "beta",
			Labels:    sl,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "beta"},
			},
		},
	}
	set(t, client, dsB)

	podA := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "one",
			Name:      "alpha-x0x0",
			Labels:    map[string]string{"app": "alpha"},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{{
				Name: "ubuntu",
				State: v1.ContainerState{
					Waiting: &v1.ContainerStateWaiting{
						Reason: "CrashLoopBackOff",
					},
				},
			}},
		},
	}
	set(t, client, podA)

	podB := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "two",
			Name:      "beta-x0x0",
			Labels:    map[string]string{"app": "beta"},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{{
				Name: "fedora",
				State: v1.ContainerState{
					Running: &v1.ContainerStateRunning{StartedAt: metav1.Time{}},
				},
			}},
		},
	}
	set(t, client, podB)

	expected := []string{"DaemonSet \"/one/alpha\" rollout is not making progress - pod alpha-x0x0 is in CrashLoopBackOff State"}
	hung := status.CheckCrashLoopBackOffPods(ClusteredName{Namespace: "one", Name: "alpha"}, map[string]string{"app": "alpha"}, "DaemonSet")
	if !reflect.DeepEqual(hung, expected) {
		t.Fatalf("unexpected value in hung %v", hung)
	}

	expected = []string{}
	hung = status.CheckCrashLoopBackOffPods(ClusteredName{Namespace: "two", Name: "beta"}, map[string]string{"app": "beta"}, "DaemonSet")
	if !reflect.DeepEqual(hung, expected) {
		t.Fatalf("unexpected value in hung %v", hung)
	}

	// forget about dsA and dsB
	dsA.Labels = nil
	dsB.Labels = nil
	set(t, client, dsA)
	set(t, client, dsB)

	// Test non-critical DaemonSets - the operator should not be marked as degraded.
	dsNC := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "four",
			Name:      "non-critical",
			Annotations: map[string]string{
				names.NonCriticalAnnotation: "",
			},
			Labels: sl,
		},
		Status: appsv1.DaemonSetStatus{
			NumberUnavailable: 1,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "non-critical"},
			},
		},
	}
	set(t, client, dsNC)

	podnC := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "four",
			Name:      "nC-x0x0",
			Labels:    map[string]string{"app": "non-critical"},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{{
				Name: "ubuntu",
				State: v1.ContainerState{
					Waiting: &v1.ContainerStateWaiting{
						Reason: "CrashLoopBackOff",
					},
				},
			},
			}},
	}
	set(t, client, podnC)

	status.SetFromPods()

	oc, err := getOC(client)
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}
	if !conditionsInclude(oc.Status.Conditions, []operv1.OperatorCondition{
		{
			Type:   operv1.OperatorStatusTypeDegraded,
			Status: operv1.ConditionFalse,
		},
		{
			Type:   operv1.OperatorStatusTypeProgressing,
			Status: operv1.ConditionTrue,
		},
		{
			Type:   operv1.OperatorStatusTypeUpgradeable,
			Status: operv1.ConditionTrue,
		},
		{
			Type:   operv1.OperatorStatusTypeAvailable,
			Status: operv1.ConditionTrue,
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
	}

	dsNC.Labels = nil
	set(t, client, dsNC)

	// Check that crashlooping deployments also are detected
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "three",
			Name:      "gamma",
			Labels:    sl,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "gamma"},
			},
		},
		Status: appsv1.DeploymentStatus{
			UnavailableReplicas: 1,
		},
	}
	set(t, client, dep)

	podC := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "three",
			Name:      "gamma-x0x0",
			Labels:    map[string]string{"app": "gamma"},
		},
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{{
				Name: "fedora",
				State: v1.ContainerState{
					Waiting: &v1.ContainerStateWaiting{
						Reason: "CrashLoopBackOff",
					},
				},
			},
			}},
	}
	set(t, client, podC)

	status.SetFromPods()
	oc, err = getOC(client)
	if err != nil {
		t.Fatalf("error getting ClusterOperator: %v", err)
	}

	if !conditionsInclude(oc.Status.Conditions, []operv1.OperatorCondition{
		{
			Type:    operv1.OperatorStatusTypeDegraded,
			Status:  operv1.ConditionTrue,
			Reason:  "RolloutHung",
			Message: "Deployment \"/three/gamma\" rollout is not making progress - pod gamma-x0x0 is in CrashLoopBackOff State",
		},
		{
			Type:   operv1.OperatorStatusTypeProgressing,
			Status: operv1.ConditionTrue,
			Reason: "Deploying",
		},
		{
			Type:   operv1.OperatorStatusTypeAvailable,
			Status: operv1.ConditionTrue,
		},
	}) {
		t.Fatalf("unexpected Status.Conditions: %#v", oc.Status.Conditions)
	}
}
