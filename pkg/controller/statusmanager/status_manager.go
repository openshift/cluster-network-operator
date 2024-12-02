package statusmanager

import (
	"context"
	"fmt"
	"log"
	"os"
	"reflect"
	"strings"
	"sync"

	"github.com/ghodss/yaml"
	"github.com/openshift/cluster-network-operator/pkg/hypershift"

	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	applyoperv1 "github.com/openshift/client-go/operator/applyconfigurations/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/apply"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/names"
	cohelpers "github.com/openshift/library-go/pkg/config/clusteroperator/v1helpers"
	operstatus "github.com/openshift/library-go/pkg/operator/status"
	"github.com/openshift/library-go/pkg/operator/v1helpers"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

type StatusLevel int

const (
	PanicLevel StatusLevel = iota // Special StatusLevel used when recovering from a panic
	ClusterConfig
	OperatorConfig
	OperatorRender
	ProxyConfig
	InjectorConfig
	MachineConfig
	PodDeployment
	PKIConfig
	EgressRouterConfig
	RolloutHung
	CertificateSigner
	InfrastructureConfig
	DashboardConfig
	maxStatusLevel
)

const (
	ClusteredNameSeparator = '/'
	fieldManager           = "cluster-network-operator/status-manager"
)

type ClusteredName struct {
	ClusterName string
	Namespace   string
	Name        string
}

func NewClusteredName(obj crclient.Object) ClusteredName {
	return ClusteredName{
		Namespace:   obj.GetNamespace(),
		Name:        obj.GetName(),
		ClusterName: apply.GetClusterName(obj),
	}
}

func (c ClusteredName) String() string {
	return c.ClusterName + string(ClusteredNameSeparator) + c.Namespace + string(ClusteredNameSeparator) + c.Name
}

// StatusManager coordinates changes to ClusterOperator.Status
type StatusManager struct {
	sync.Mutex

	client           cnoclient.Client
	name             string
	hyperShiftConfig *hypershift.HyperShiftConfig

	failing         [maxStatusLevel]*operv1.OperatorCondition
	installComplete bool

	// All our informers and listers
	dsInformers map[string]cache.SharedIndexInformer
	dsListers   map[string]DaemonSetLister

	depInformers map[string]cache.SharedIndexInformer
	depListers   map[string]DeploymentLister

	ssInformers map[string]cache.SharedIndexInformer
	ssListers   map[string]StatefulSetLister

	labelSelector labels.Selector

	relatedObjects []configv1.ObjectReference

	// local cache to store deployed network operator machine configs.
	availableMachineConfigs map[string]sets.Set[string]
	// local cache to store deleted network operator machine configs and
	// not yet removed from corresponding machine config pool.
	deletedMachineConfigs map[string]sets.Set[string]

	// used only for upgrades from <=4.13 to 4.14 with ovn-kubernetes
	// TODO: remove in 4.15
	isOVNKubernetes *bool
}

func New(client cnoclient.Client, name, cluster string) *StatusManager {
	status := &StatusManager{
		client:           client,
		name:             name,
		hyperShiftConfig: hypershift.NewHyperShiftConfig(),

		dsInformers:             map[string]cache.SharedIndexInformer{},
		dsListers:               map[string]DaemonSetLister{},
		depInformers:            map[string]cache.SharedIndexInformer{},
		depListers:              map[string]DeploymentLister{},
		ssInformers:             map[string]cache.SharedIndexInformer{},
		ssListers:               map[string]StatefulSetLister{},
		availableMachineConfigs: map[string]sets.Set[string]{},
		deletedMachineConfigs:   map[string]sets.Set[string]{},
	}
	var err error
	status.labelSelector, err = labels.Parse(fmt.Sprintf("%s==%s", names.GenerateStatusLabel, cluster))
	if err != nil {
		panic(err) // selector is guaranteed valid, unreachable
	}
	return status
}

// setClusterOperAnnotation sets an annotation on the clusterOperator network object
func (status *StatusManager) setClusterOperAnnotation(obj *configv1.ClusterOperator) error {
	value := []string{}
	for _, obj := range status.hyperShiftConfig.RelatedObjects {
		value = append(value, fmt.Sprintf("%s/%s/%s/%s/%s", obj.ClusterName, obj.Group, obj.Resource, obj.Namespace, obj.Name))
	}
	anno := strings.Join(value, ",")
	return status.setAnnotation(context.TODO(), obj, names.RelatedClusterObjectsAnnotation, &anno)
}

// getClusterOperAnnotation gets an annotation from the clusterOperator network object
func (status *StatusManager) getClusterOperAnnotation(obj *configv1.ClusterOperator) ([]hypershift.RelatedObject, error) {
	new := obj.DeepCopy()
	anno := new.GetAnnotations()
	objs := []hypershift.RelatedObject{}

	value, set := anno[names.RelatedClusterObjectsAnnotation]
	if !set || value == "" {
		return objs, nil
	}
	items := strings.Split(value, ",")
	if len(items) == 0 {
		return objs, nil
	}
	for _, res := range items {
		parts := strings.Split(res, "/")
		if len(parts) != 5 {
			return objs, fmt.Errorf("'%s' annotation is invalid, expected: ClusterName/Group/Resource/Namespace/Name, got: %s",
				names.RelatedClusterObjectsAnnotation, res)
		}
		objs = append(objs, hypershift.RelatedObject{
			ClusterName: parts[0],
			ObjectReference: configv1.ObjectReference{
				Group:     parts[1],
				Resource:  parts[2],
				Namespace: parts[3],
				Name:      parts[4],
			},
		})
	}

	return objs, nil
}

// deleteRelatedObjects checks for related objects attached to ClusterOperator and deletes
// whatever is not being rendered from manifests. This is a mechanism to cleanup objects
// that are no longer needed and are probably present from a previous version
func (status *StatusManager) deleteRelatedObjectsNotRendered(co *configv1.ClusterOperator) {
	if status.relatedObjects == nil && status.hyperShiftConfig.RelatedObjects == nil {
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
			gvk, err := status.client.ClientFor("").RESTMapper().KindFor(gvr)
			if err != nil {
				log.Printf("Error getting GVK of object for deletion: %v", err)
				status.relatedObjects = append(status.relatedObjects, currentObj)
				continue
			}
			if gvk.Kind == "Namespace" && gvk.Group == "" {
				// BZ 1820472: During SDN migration, deleting a namespace object may get stuck in 'Terminating' forever if the cluster network doesn't working as expected.
				// We choose to not delete the namespace here but to ask user do it manually after the cluster is back to normal state.
				log.Printf("Object Kind is Namespace, skip")
				continue
			}
			// @aconstan: remove this after having the PR implementing this change, integrated.
			if gvk.Kind == "Network" && gvk.Group == "operator.openshift.io" {
				log.Printf("Object Kind is network.operator.openshift.io, skip")
				continue
			}
			// @npinaeva objects without a name shouldn't be listed as relatedObjects in the first place,
			// and we should never try to delete them
			if currentObj.Name == "" {
				log.Printf("Object without a name GVK %+v, skip", gvk)
				continue
			}

			// Do not remove old rbac definitions before upgrade completes to avoid disruptions
			if !status.installComplete && gvk.Group == "rbac.authorization.k8s.io" {
				klog.Infof("Upgrade in progress, skipping removal of (%s) %s/%s for now", gvk, currentObj.Namespace, currentObj.Name)
				status.relatedObjects = append(status.relatedObjects, currentObj)
				continue
			}

			log.Printf("Detected related object with GVK %+v, namespace %v and name %v not rendered by manifests, deleting...", gvk, currentObj.Namespace, currentObj.Name)
			objToDelete := &uns.Unstructured{}
			objToDelete.SetName(currentObj.Name)
			objToDelete.SetNamespace(currentObj.Namespace)
			objToDelete.SetGroupVersionKind(gvk)
			err = status.client.ClientFor("").CRClient().Delete(context.TODO(), objToDelete, crclient.PropagationPolicy("Background"))
			if err != nil {
				log.Printf("Error deleting related object: %v", err)
				if !errors.IsNotFound(err) {
					status.relatedObjects = append(status.relatedObjects, currentObj)
				}
				continue
			}
		}
	}

	currentObjs, err := status.getClusterOperAnnotation(co)
	if err != nil {
		log.Printf("Error parsing related cluster objects: %v", err)
	}
	for _, currentObj := range currentObjs {
		var found bool = false
		for _, renderedObj := range status.hyperShiftConfig.RelatedObjects {
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
			gvk, err := status.client.ClientFor(currentObj.ClusterName).RESTMapper().KindFor(gvr)
			if err != nil {
				log.Printf("Error getting GVK of object for deletion: %v", err)
				status.hyperShiftConfig.RelatedObjects = append(status.hyperShiftConfig.RelatedObjects, currentObj)
				continue
			}

			log.Printf("Detected related cluster object with GVK %+v, namespace %v and name %v not rendered by manifests, deleting...", gvk, currentObj.Namespace, currentObj.Name)
			objToDelete := &uns.Unstructured{}
			objToDelete.SetName(currentObj.Name)
			objToDelete.SetNamespace(currentObj.Namespace)
			objToDelete.SetGroupVersionKind(gvk)
			err = status.client.ClientFor(currentObj.ClusterName).CRClient().Delete(context.TODO(), objToDelete, crclient.PropagationPolicy("Background"))
			if err != nil {
				log.Printf("Error deleting related cluser object: %v", err)
				if !errors.IsNotFound(err) {
					status.hyperShiftConfig.RelatedObjects = append(status.hyperShiftConfig.RelatedObjects, currentObj)
				}
				continue
			}
		}
	}
}

// WriteHypershiftStatus mirrors network.operator status to HostedControlPlane status
func (status *StatusManager) writeHypershiftStatus(operStatus *operv1.NetworkStatus) {
	if !status.hyperShiftConfig.Enabled {
		return
	}
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {

		hcp := &uns.Unstructured{}
		hcp.SetGroupVersionKind(hypershift.HostedControlPlaneGVK)
		err := status.client.ClientFor(names.ManagementClusterName).CRClient().Get(context.TODO(), types.NamespacedName{Namespace: status.hyperShiftConfig.Namespace, Name: status.hyperShiftConfig.Name}, hcp)
		if err != nil {
			return err
		}

		updatedConditions, err := hypershift.SetHostedControlPlaneConditions(hcp, operStatus)
		if err != nil {
			return fmt.Errorf("failed to set HostedControlPlane conditions: %v", err)
		}
		if len(updatedConditions) == 0 {
			// nothing changed, return
			return nil
		}

		if err := status.client.ClientFor(names.ManagementClusterName).CRClient().Status().Update(context.TODO(), hcp); err != nil {
			return err
		}
		log.Printf("Set HostedControlPlane conditions:\n%v", updatedConditions)
		return nil
	})
	if err != nil {
		log.Printf("Failed to set HostedControlPlane: %v", err)
	}
}

// Set updates the operator and clusteroperator statuses with the provided conditions.
func (status *StatusManager) set(reachedAvailableLevel bool, conditions ...operv1.OperatorCondition) {
	var operStatus *operv1.NetworkStatus

	// Set status on the network.operator object
	err := func() error {
		oc, err := status.client.Default().OpenshiftOperatorClient().OperatorV1().Networks().Get(context.TODO(), names.OPERATOR_CONFIG, metav1.GetOptions{})
		if err != nil {
			// Should never happen outside of unit tests
			return err
		}

		oldStatus := oc.Status.DeepCopy()

		oc.Status.Version = os.Getenv("RELEASE_VERSION")

		// If there is no Available condition on the operator config then copy the
		// conditions from the ClusterOperator (which will either also be empty if
		// this is a new install, or will contain the pre-4.7 conditions if this is
		// a 4.6->4.7 upgrade).
		if v1helpers.FindOperatorCondition(oc.Status.Conditions, operv1.OperatorStatusTypeAvailable) == nil {
			co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: status.name}}
			err := status.client.ClientFor("").CRClient().Get(context.TODO(), types.NamespacedName{Name: status.name}, co)
			if err != nil {
				log.Printf("failed to retrieve ClusterOperator object: %v - continuing", err)
			}

			for _, condition := range co.Status.Conditions {
				v1helpers.SetOperatorCondition(&oc.Status.Conditions, operv1.OperatorCondition{
					Type:               string(condition.Type),
					Status:             operv1.ConditionStatus(condition.Status),
					LastTransitionTime: condition.LastTransitionTime,
					Reason:             condition.Reason,
					Message:            condition.Message,
				})
			}
		}

		for _, condition := range conditions {
			v1helpers.SetOperatorCondition(&oc.Status.Conditions, condition)
		}

		progressingCondition := v1helpers.FindOperatorCondition(oc.Status.Conditions, operv1.OperatorStatusTypeProgressing)
		availableCondition := v1helpers.FindOperatorCondition(oc.Status.Conditions, operv1.OperatorStatusTypeAvailable)
		if availableCondition == nil && progressingCondition != nil && progressingCondition.Status == operv1.ConditionTrue {
			v1helpers.SetOperatorCondition(&oc.Status.Conditions,
				operv1.OperatorCondition{
					Type:    operv1.OperatorStatusTypeAvailable,
					Status:  operv1.ConditionFalse,
					Reason:  "Startup",
					Message: "The network is starting up",
				},
			)
		}

		v1helpers.SetOperatorCondition(&oc.Status.Conditions,
			operv1.OperatorCondition{
				Type:   operv1.OperatorStatusTypeUpgradeable,
				Status: operv1.ConditionTrue,
			},
		)

		operStatus = &oc.Status

		if equality.Semantic.DeepEqual(&oc.Status, oldStatus) {
			return nil
		}

		buf, err := yaml.Marshal(oc.Status.Conditions)
		if err != nil {
			buf = []byte(fmt.Sprintf("(failed to convert to YAML: %s)", err))
		}

		// Use applyconfigurations to change only the specified fields
		net := applyoperv1.Network(names.OPERATOR_CONFIG).
			WithStatus(applyoperv1.NetworkStatus().WithVersion(oc.Status.Version))
		for _, condition := range oc.Status.Conditions {
			net.Status.WithConditions(applyoperv1.OperatorCondition().
				WithType(condition.Type).
				WithStatus(condition.Status).
				WithLastTransitionTime(condition.LastTransitionTime).
				WithReason(condition.Reason).
				WithMessage(condition.Message))
		}
		if _, err := status.client.ClientFor("").OpenshiftOperatorClient().OperatorV1().Networks().Apply(
			context.TODO(), net, metav1.ApplyOptions{Force: true, FieldManager: fieldManager}); err != nil {
			return err
		}
		log.Printf("Network operator config updated with conditions:\n%s", buf)

		return nil
	}()
	if err != nil {
		log.Printf("Failed to set operator status: %v", err)
	}

	// Set status conditions on the network clusteroperator object.
	// TODO: enable the library-go ClusterOperatorStatusController, which will
	// do this for us. We can't use that yet, because it doesn't allow dynamic RelatedObjects[].
	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: status.name}}
		err := status.client.ClientFor("").CRClient().Get(context.TODO(), types.NamespacedName{Name: status.name}, co)
		isNotFound := errors.IsNotFound(err)
		if err != nil && !isNotFound {
			return err
		}

		oldStatus := co.Status.DeepCopy()
		status.deleteRelatedObjectsNotRendered(co)
		if status.relatedObjects != nil {
			co.Status.RelatedObjects = status.relatedObjects
		}

		if status.hyperShiftConfig.RelatedObjects != nil {
			err := status.setClusterOperAnnotation(co)
			if err != nil {
				return err
			}
		}

		if operStatus == nil {
			cohelpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{
				Type:    configv1.OperatorDegraded,
				Status:  configv1.ConditionTrue,
				Reason:  "NoOperConfig",
				Message: "No networks.operator.openshift.io cluster found",
			})
		} else {
			if reachedAvailableLevel {
				co.Status.Versions = []configv1.OperandVersion{
					{Name: "operator", Version: operStatus.Version},
				}
			}

			for _, cond := range operStatus.Conditions {
				cohelpers.SetStatusCondition(&co.Status.Conditions, operstatus.OperatorConditionToClusterOperatorCondition(cond))
			}
		}

		if reflect.DeepEqual(*oldStatus, co.Status) {
			return nil
		}

		buf, err := yaml.Marshal(co.Status.Conditions)
		if err != nil {
			buf = []byte(fmt.Sprintf("(failed to convert to YAML: %s)", err))
		}

		if isNotFound {
			if err := status.client.ClientFor("").CRClient().Create(context.TODO(), co); err != nil {
				return err
			}
			log.Printf("ClusterOperator config: %s created with conditions:\n%s", co.Name, buf)
			return nil
		}
		if err := status.client.ClientFor("").CRClient().Status().Update(context.TODO(), co); err != nil {
			return err
		}
		log.Printf("ClusterOperator config status updated with conditions:\n%s", buf)
		return nil
	})
	if err != nil {
		log.Printf("Failed to set ClusterOperator: %v", err)
	}

	status.writeHypershiftStatus(operStatus)
}

// syncDegraded syncs the current Degraded status
func (status *StatusManager) syncDegraded() {
	for _, c := range status.failing {
		if c != nil && c.Type == operv1.OperatorStatusTypeDegraded {
			status.set(false, *c)
			return
		}
	}
	status.set(
		false,
		operv1.OperatorCondition{
			Type:   operv1.OperatorStatusTypeDegraded,
			Status: operv1.ConditionFalse,
		},
	)
}

func (status *StatusManager) setDegraded(statusLevel StatusLevel, reason, message string) {
	status.failing[statusLevel] = &operv1.OperatorCondition{
		Type:    operv1.OperatorStatusTypeDegraded,
		Status:  operv1.ConditionTrue,
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

func (status *StatusManager) SetDegradedOnPanicAndCrash(panicVal interface{}) {
	status.Lock()
	defer status.Unlock()
	status.setDegraded(PanicLevel, "ReconcileError", fmt.Sprintf("Panic detected: %v", panicVal))
	panic(panicVal)
}

func (status *StatusManager) SetNotDegraded(statusLevel StatusLevel) {
	status.Lock()
	defer status.Unlock()
	status.setNotDegraded(statusLevel)
}

// syncProgressing syncs the current Progressing status
func (status *StatusManager) syncProgressing() {
	for _, c := range status.failing {
		if c != nil && c.Type == operv1.OperatorStatusTypeProgressing {
			status.set(false, *c)
			return
		}
	}
	status.set(
		false,
		operv1.OperatorCondition{
			Type:   operv1.OperatorStatusTypeProgressing,
			Status: operv1.ConditionFalse,
		},
	)
}

func (status *StatusManager) setProgressing(statusLevel StatusLevel, reason, message string) {
	status.failing[statusLevel] = &operv1.OperatorCondition{
		Type:    operv1.OperatorStatusTypeProgressing,
		Status:  operv1.ConditionTrue,
		Reason:  reason,
		Message: message,
	}
	status.syncProgressing()
}

func (status *StatusManager) unsetProgressing(statusLevel StatusLevel) {
	if status.failing[statusLevel] != nil {
		status.failing[statusLevel] = nil
	}
	status.syncProgressing()
}

func (status *StatusManager) SetProgressing(statusLevel StatusLevel, reason, message string) {
	status.Lock()
	defer status.Unlock()
	status.setProgressing(statusLevel, reason, message)
}

func (status *StatusManager) UnsetProgressing(statusLevel StatusLevel) {
	status.Lock()
	defer status.Unlock()
	status.unsetProgressing(statusLevel)
}

func (status *StatusManager) SetRelatedObjects(relatedObjects []configv1.ObjectReference) {
	status.Lock()
	defer status.Unlock()
	status.relatedObjects = relatedObjects
}

func (status *StatusManager) SetRelatedClusterObjects(relatedObjects []hypershift.RelatedObject) {
	status.Lock()
	defer status.Unlock()
	status.hyperShiftConfig.RelatedObjects = relatedObjects
}
