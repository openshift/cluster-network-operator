package statusmanager

import (
	"context"

	configv1client "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned"
	mcfgv1informer "github.com/openshift/machine-config-operator/pkg/generated/informers/externalversions/machineconfiguration.openshift.io/v1"
	mcfgv1lister "github.com/openshift/machine-config-operator/pkg/generated/listers/machineconfiguration.openshift.io/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

type MachineConfigsWatcher struct {
	status *StatusManager
}

func (s *StatusManager) initInformerForMachineConfigs(cfg *rest.Config) error {
	configClient, err := configv1client.NewForConfig(cfg)
	if err != nil {
		return err
	}
	labelSelector, err := labels.Parse("machineconfiguration.openshift.io/role in (master, worker)")
	if err != nil {
		return err
	}
	inf := mcfgv1informer.NewFilteredMachineConfigInformer(configClient, 0,
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
		func(options *metav1.ListOptions) {
			options.LabelSelector = labelSelector.String()
		})
	s.client.ClientFor("").AddCustomInformer(inf)
	s.mcInformer = inf
	s.mcLister = mcfgv1lister.NewMachineConfigLister(inf.GetIndexer())

	inf = mcfgv1informer.NewMachineConfigPoolInformer(configClient, 0,
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	s.client.ClientFor("").AddCustomInformer(inf)
	s.mcpInformer = inf
	s.mcpLister = mcfgv1lister.NewMachineConfigPoolLister(inf.GetIndexer())

	return nil
}

// AddMachineConfigsWatcher wires up the MachineConfigWatcher and MachineConfigPoolWatcher
// to the controller-manager.
func (s *StatusManager) AddMachineConfigsWatcher(cfg *rest.Config, mgr manager.Manager) error {
	if s.hyperShiftConfig.Enabled {
		// MachineConfig is not supported in HyperShift cluster, so return without
		// initializing watcher.
		return nil
	}
	err := s.initInformerForMachineConfigs(cfg)
	if err != nil {
		return err
	}

	pw := &MachineConfigsWatcher{
		status: s,
	}
	c, err := controller.New("machineconfigs-watcher", mgr, controller.Options{Reconciler: pw})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Informer{Informer: s.mcInformer,
		Handler: handler.EnqueueRequestsFromMapFunc(enqueueRP)})
	if err != nil {
		return err
	}

	return c.Watch(&source.Informer{Informer: s.mcpInformer,
		Handler: handler.EnqueueRequestsFromMapFunc(enqueueRP)})
}

// Reconcile triggers a re-update of Status.
func (p *MachineConfigsWatcher) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	defer utilruntime.HandleCrash(p.status.SetDegradedOnPanicAndCrash)
	p.status.SetFromMachineConfigs()
	return reconcile.Result{}, nil
}
