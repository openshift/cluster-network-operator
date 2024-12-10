package statusmanager

import (
	"context"

	"github.com/openshift/cluster-network-operator/pkg/platform"
	"github.com/openshift/cluster-network-operator/pkg/util/k8s"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

type MachineConfigWatcher struct {
	status *StatusManager
	cache  cache.Cache
}

type MachineConfigPoolWatcher struct {
	status *StatusManager
	cache  cache.Cache
}

// AddMachineConfigWatcher wires up the MachineConfigWatcher to the controller-manager.
func (s *StatusManager) AddMachineConfigWatcher(mgr manager.Manager) error {
	if s.hyperShiftConfig.Enabled {
		// MachineConfig is not supported in HyperShift cluster, so return without
		// initializing watcher.
		return nil
	}

	operatorCache := mgr.GetCache()
	pw := &MachineConfigWatcher{
		status: s,
		cache:  operatorCache,
	}
	c, err := controller.New("machineconfig-watcher", mgr, controller.Options{Reconciler: pw})
	if err != nil {
		return err
	}

	s.Lock()
	defer s.Unlock()
	if s.renderedMachineConfigs == nil {
		s.renderedMachineConfigs, err = s.getLastRenderedMachineConfigState()
		if err != nil {
			return err
		}
	}

	return c.Watch(source.Kind[crclient.Object](operatorCache, &mcfgv1.MachineConfig{},
		&handler.EnqueueRequestForObject{}, onMachineConfigPredicate()))
}

// Reconcile triggers a re-update of Status.
func (m *MachineConfigWatcher) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	defer utilruntime.HandleCrash(m.status.SetDegradedOnPanicAndCrash)
	mcPools := &mcfgv1.MachineConfigPoolList{}
	err := m.cache.List(ctx, mcPools)
	if err != nil {
		klog.Errorf("failed to retrieve machine config pools: %v", err)
		return reconcile.Result{}, nil
	}
	m.status.SetFromMachineConfigPool(mcPools.Items)
	return reconcile.Result{}, nil
}

// AddMachineConfigPoolWatcher wires up the MachineConfigPoolWatcher to the controller-manager.
func (s *StatusManager) AddMachineConfigPoolWatcher(mgr manager.Manager) error {
	if s.hyperShiftConfig.Enabled {
		// MachineConfig is not supported in HyperShift cluster, so return without
		// initializing watcher.
		return nil
	}

	operatorCache := mgr.GetCache()
	pw := &MachineConfigPoolWatcher{
		status: s,
		cache:  operatorCache,
	}
	c, err := controller.New("machineconfigpool-watcher", mgr, controller.Options{Reconciler: pw})
	if err != nil {
		return err
	}

	s.Lock()
	defer s.Unlock()
	if s.renderedMachineConfigs == nil {
		s.renderedMachineConfigs, err = s.getLastRenderedMachineConfigState()
		if err != nil {
			return err
		}
	}

	return c.Watch(source.Kind[crclient.Object](operatorCache, &mcfgv1.MachineConfigPool{},
		&handler.EnqueueRequestForObject{}, onMachineConfigPoolPredicate()))
}

// Reconcile triggers a re-update of Status.
func (p *MachineConfigPoolWatcher) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	defer utilruntime.HandleCrash(p.status.SetDegradedOnPanicAndCrash)
	mcPools := &mcfgv1.MachineConfigPoolList{}
	err := p.cache.List(ctx, mcPools)
	if err != nil {
		klog.Errorf("failed to retrieve machine config pools: %v", err)
		return reconcile.Result{}, nil
	}
	p.status.SetFromMachineConfigPool(mcPools.Items)
	return reconcile.Result{}, nil
}

func onMachineConfigPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			mc := e.Object.(*mcfgv1.MachineConfig)
			return k8s.ContainsNetworkOwnerRef(mc.OwnerReferences)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			mc := e.ObjectNew.(*mcfgv1.MachineConfig)
			return k8s.ContainsNetworkOwnerRef(mc.OwnerReferences)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			mc := e.Object.(*mcfgv1.MachineConfig)
			return k8s.ContainsNetworkOwnerRef(mc.OwnerReferences)
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
			mcpOld := e.ObjectOld.(*mcfgv1.MachineConfigPool)
			mcpNew := e.ObjectNew.(*mcfgv1.MachineConfigPool)
			return hasRequiredMachineConfigSelector(mcpOld) ||
				hasRequiredMachineConfigSelector(mcpNew)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			mcp := e.Object.(*mcfgv1.MachineConfigPool)
			return hasRequiredMachineConfigSelector(mcp)
		},
	}
}

func hasRequiredMachineConfigSelector(mcp *mcfgv1.MachineConfigPool) bool {
	mcSelector, err := metav1.LabelSelectorAsSelector(mcp.Spec.MachineConfigSelector)
	if err != nil {
		klog.Errorf("invalid machine config label selector in %s pool", mcp.Name)
		return false
	}
	matches := func(mcSelector labels.Selector, masterLabelSet labels.Set) bool {
		return mcSelector.Matches(masterLabelSet)
	}
	return matches(mcSelector, platform.MasterRoleMachineConfigLabel) ||
		matches(mcSelector, platform.WorkerRoleMachineConfigLabel)
}
