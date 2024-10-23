package statusmanager

import (
	"context"

	"github.com/openshift/cluster-network-operator/pkg/platform"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/klog/v2"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

type MachineConfigsWatcher struct {
	status *StatusManager
}

// AddMachineConfigsWatcher wires up the MachineConfigWatcher and MachineConfigPoolWatcher
// to the controller-manager.
func (s *StatusManager) AddMachineConfigsWatcher(mgr manager.Manager) error {
	if s.hyperShiftConfig.Enabled {
		// MachineConfig is not supported in HyperShift cluster, so return without
		// initializing watcher.
		return nil
	}

	pw := &MachineConfigsWatcher{
		status: s,
	}
	c, err := controller.New("machineconfigs-watcher", mgr, controller.Options{Reconciler: pw})
	if err != nil {
		return err
	}

	err = c.Watch(source.Kind[crclient.Object](mgr.GetCache(), &mcfgv1.MachineConfig{},
		&handler.EnqueueRequestForObject{}, onMachineConfigPredicate()))
	if err != nil {
		return err
	}

	return c.Watch(source.Kind[crclient.Object](mgr.GetCache(), &mcfgv1.MachineConfigPool{},
		&handler.EnqueueRequestForObject{}, onMachineConfigPoolPredicate()))
}

// Reconcile triggers a re-update of Status.
func (p *MachineConfigsWatcher) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	defer utilruntime.HandleCrash(p.status.SetDegradedOnPanicAndCrash)
	p.status.SetFromMachineConfigs(ctx)
	return reconcile.Result{}, nil
}

func onMachineConfigPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			mc := e.Object.(*mcfgv1.MachineConfig)
			return hasRequiredLabel(mc)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			mc := e.ObjectNew.(*mcfgv1.MachineConfig)
			return hasRequiredLabel(mc)
		},
	}
}

func onMachineConfigPoolPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			mcp := e.Object.(*mcfgv1.MachineConfigPool)
			return hasRequiredMachineConfigSelector(mcp)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			mcp := e.ObjectNew.(*mcfgv1.MachineConfigPool)
			return hasRequiredMachineConfigSelector(mcp)
		},
	}
}

func hasRequiredLabel(mc *mcfgv1.MachineConfig) bool {
	isSubset := func(mcLabels, roleLabel map[string]string) bool {
		for roleLKey, roleLValue := range roleLabel {
			if mcLabelValue, ok := mcLabels[roleLKey]; !ok || mcLabelValue != roleLValue {
				return false
			}
		}
		return true
	}
	return isSubset(mc.Labels, platform.MasterRoleMachineConfigLabel) ||
		isSubset(mc.Labels, platform.WorkerRoleMachineConfigLabel)
}

func hasRequiredMachineConfigSelector(mcp *mcfgv1.MachineConfigPool) bool {
	mcSelector, err := metav1.LabelSelectorAsSelector(mcp.Spec.MachineConfigSelector)
	if err != nil {
		klog.Errorf("invalid machine config label selector in %s pool", mcp.Name)
		return false
	}
	var (
		masterLabelSet labels.Set = platform.MasterRoleMachineConfigLabel
		workerLabelSet labels.Set = platform.MasterRoleMachineConfigLabel
	)
	return mcSelector.Matches(masterLabelSet) || mcSelector.Matches(workerLabelSet)
}
