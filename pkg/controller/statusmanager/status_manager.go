package statusmanager

import (
	"context"
	"fmt"
	"log"
	"os"
	"reflect"
	"sync"

	"github.com/ghodss/yaml"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/library-go/pkg/config/clusteroperator/v1helpers"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

type StatusLevel int

const (
	ClusterConfig StatusLevel = iota
	OperatorConfig
	ProxyConfig
	InjectorConfig
	PodDeployment
	PKIConfig
	RolloutHung
	maxStatusLevel
)

// StatusManager coordinates changes to ClusterOperator.Status
type StatusManager struct {
	sync.Mutex

	client  client.Client
	mapper  meta.RESTMapper
	name    string
	version string

	failing [maxStatusLevel]*configv1.ClusterOperatorStatusCondition

	daemonSets     []types.NamespacedName
	deployments    []types.NamespacedName
	relatedObjects []configv1.ObjectReference
}

func New(client client.Client, mapper meta.RESTMapper, name, version string) *StatusManager {
	return &StatusManager{client: client, mapper: mapper, name: name, version: version}
}

// deleteRelatedObjects checks for related objects attached to ClusterOperator and deletes
// whatever is not been rendered from manifests. This is a mechanism to cleanup objects
// that are no longer needed and are probably present from a previous version
func (status *StatusManager) deleteRelatedObjectsNotRendered(co *configv1.ClusterOperator) {
	if status.relatedObjects == nil {
		return
	}

	for _, currentObj := range co.Status.RelatedObjects {
		var found bool = false
		for _, renderedObj := range status.relatedObjects {
			found = reflect.DeepEqual(currentObj, renderedObj)

			if found {
				break
			}
		}
		if !found {
			gvr := schema.GroupVersionResource{
				Group:    currentObj.Group,
				Resource: currentObj.Resource,
			}
			gvk, err := status.mapper.KindFor(gvr)
			if err != nil {
				log.Printf("Error getting GVK of object for deletion: %v", err)
				status.relatedObjects = append(status.relatedObjects, currentObj)
				continue
			}
			log.Printf("Detected related object with GVK %+v, namespace %v and name %v not rendered by manifests, deleting...", gvk, currentObj.Namespace, currentObj.Name)
			objToDelete := &uns.Unstructured{}
			objToDelete.SetName(currentObj.Name)
			objToDelete.SetNamespace(currentObj.Namespace)
			objToDelete.SetGroupVersionKind(gvk)
			err = status.client.Delete(context.TODO(), objToDelete, client.PropagationPolicy("Background"))
			if err != nil {
				log.Printf("Error deleting related object: %v", err)
				if !errors.IsNotFound(err) {
					status.relatedObjects = append(status.relatedObjects, currentObj)
				}
				continue
			}
		}
	}
}

// Set updates the ClusterOperator.Status with the provided conditions
func (status *StatusManager) set(reachedAvailableLevel bool, conditions ...configv1.ClusterOperatorStatusCondition) {
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: status.name}}
		err := status.client.Get(context.TODO(), types.NamespacedName{Name: status.name}, co)
		isNotFound := errors.IsNotFound(err)
		if err != nil && !isNotFound {
			return err
		}

		oldStatus := co.Status.DeepCopy()
		status.deleteRelatedObjectsNotRendered(co)
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
			return nil
		}

		buf, err := yaml.Marshal(co.Status.Conditions)
		if err != nil {
			buf = []byte(fmt.Sprintf("(failed to convert to YAML: %s)", err))
		}
		if isNotFound {
			if err := status.client.Create(context.TODO(), co); err != nil {
				return err
			}
			log.Printf("Created ClusterOperator with conditions:\n%s", string(buf))
			return nil
		}
		if err := status.client.Status().Update(context.TODO(), co); err != nil {
			return err
		}
		log.Printf("Updated ClusterOperator with conditions:\n%s", string(buf))
		return nil
	})
	if err != nil {
		log.Printf("Failed to set ClusterOperator: %v", err)
	}
}

// syncDegraded syncs the current Degraded status
func (status *StatusManager) syncDegraded() {
	for _, c := range status.failing {
		if c != nil {
			status.set(false, *c)
			return
		}
	}
	status.set(
		false,
		configv1.ClusterOperatorStatusCondition{
			Type:   configv1.OperatorDegraded,
			Status: configv1.ConditionFalse,
		},
	)
}

func (status *StatusManager) setDegraded(statusLevel StatusLevel, reason, message string) {
	status.failing[statusLevel] = &configv1.ClusterOperatorStatusCondition{
		Type:    configv1.OperatorDegraded,
		Status:  configv1.ConditionTrue,
		Reason:  reason,
		Message: message,
	}
	status.syncDegraded()
}

func (status *StatusManager) setNotDegraded(statusLevel StatusLevel) {
	if status.failing[statusLevel] != nil {
		status.failing[statusLevel] = nil
	}
	status.syncDegraded()
}

func (status *StatusManager) SetDegraded(statusLevel StatusLevel, reason, message string) {
	status.Lock()
	defer status.Unlock()
	status.setDegraded(statusLevel, reason, message)
}

func (status *StatusManager) SetNotDegraded(statusLevel StatusLevel) {
	status.Lock()
	defer status.Unlock()
	status.setNotDegraded(statusLevel)
}

func (status *StatusManager) SetDaemonSets(daemonSets []types.NamespacedName) {
	status.Lock()
	defer status.Unlock()
	status.daemonSets = daemonSets
}

func (status *StatusManager) SetDeployments(deployments []types.NamespacedName) {
	status.Lock()
	defer status.Unlock()
	status.deployments = deployments
}

func (status *StatusManager) SetRelatedObjects(relatedObjects []configv1.ObjectReference) {
	status.Lock()
	defer status.Unlock()
	status.relatedObjects = relatedObjects
}
