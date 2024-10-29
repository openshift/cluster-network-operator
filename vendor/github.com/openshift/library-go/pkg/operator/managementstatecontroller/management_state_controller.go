package managementstatecontroller

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	operatorv1 "github.com/openshift/api/operator/v1"
	applyoperatorv1 "github.com/openshift/client-go/operator/applyconfigurations/operator/v1"

	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/condition"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/management"
	operatorv1helpers "github.com/openshift/library-go/pkg/operator/v1helpers"
)

// ManagementStateController watches changes of `managementState` field and react in case that field is set to an unsupported value.
// As each operator can opt-out from supporting `unmanaged` or `removed` states, this controller will add failing condition when the
// value for this field is set to this values for those operators.
type ManagementStateController struct {
	controllerInstanceName string
	operatorName           string
	operatorClient         operatorv1helpers.OperatorClient
}

func NewOperatorManagementStateController(
	instanceName string,
	operatorClient operatorv1helpers.OperatorClient,
	recorder events.Recorder,
) factory.Controller {
	c := &ManagementStateController{
		controllerInstanceName: factory.ControllerInstanceName(instanceName, "ManagementState"),
		operatorName:           instanceName,
		operatorClient:         operatorClient,
	}
	return factory.New().
		WithInformers(operatorClient.Informer()).
		WithSync(c.sync).
		ResyncEvery(time.Minute).
		ToController(
			c.controllerInstanceName,
			recorder.WithComponentSuffix("management-state-recorder"),
		)
}

func (c ManagementStateController) sync(ctx context.Context, syncContext factory.SyncContext) error {
	detailedSpec, _, _, err := c.operatorClient.GetOperatorState()
	if apierrors.IsNotFound(err) {
		if management.IsOperatorRemovable() {
			return nil
		}
		syncContext.Recorder().Warningf("StatusNotFound", "Unable to determine current operator status for %s", c.operatorName)
		return nil
	}
	if err != nil {
		return err
	}

	cond := applyoperatorv1.OperatorCondition().
		WithType(condition.ManagementStateDegradedConditionType).
		WithStatus(operatorv1.ConditionFalse)

	if management.IsOperatorAlwaysManaged() && detailedSpec.ManagementState == operatorv1.Unmanaged {
		cond = cond.
			WithStatus(operatorv1.ConditionTrue).
			WithReason("Unmanaged").
			WithMessage(fmt.Sprintf("Unmanaged is not supported for %s operator", c.operatorName))
	}

	if management.IsOperatorNotRemovable() && detailedSpec.ManagementState == operatorv1.Removed {
		cond = cond.
			WithStatus(operatorv1.ConditionTrue).
			WithReason("Removed").
			WithMessage(fmt.Sprintf("Removed is not supported for %s operator", c.operatorName))
	}

	if management.IsOperatorUnknownState(detailedSpec.ManagementState) {
		cond = cond.
			WithStatus(operatorv1.ConditionTrue).
			WithReason("Unknown").
			WithMessage(fmt.Sprintf("Unsupported management state %q for %s operator", detailedSpec.ManagementState, c.operatorName))
	}

	status := applyoperatorv1.OperatorStatus().WithConditions(cond)
	return c.operatorClient.ApplyOperatorStatus(ctx, c.controllerInstanceName, status)
}
