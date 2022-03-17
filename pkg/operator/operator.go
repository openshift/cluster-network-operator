package operator

import (
	"context"
	"fmt"

	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/controller"
	"github.com/openshift/cluster-network-operator/pkg/controller/connectivitycheck"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/library-go/pkg/operator/managementstatecontroller"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/openshift/library-go/pkg/operator/loglevel"

	"github.com/openshift/library-go/pkg/operator/management"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	ctmanager "sigs.k8s.io/controller-runtime/pkg/manager"
)

// Operator is the higher-level manager that builds a client and starts the controllers.
// It also starts the controller-tools manager, which then manages all controllers
// that use the controller-tools scaffolding.
type Operator struct {
	client  cnoclient.Client
	manager ctmanager.Manager

	StatusManager *statusmanager.StatusManager
}

const LOCK_NAME = "cluster-network-operator"

func RunOperator(ctx context.Context, controllerConfig *controllercmd.ControllerContext, inClusterClientName string, extraClusters map[string]string) error {
	o := &Operator{}

	var err error
	if o.client, err = cnoclient.NewClient(controllerConfig.KubeConfig, controllerConfig.ProtoKubeConfig, inClusterClientName, extraClusters); err != nil {
		return err
	}

	// initialize the controller-runtime environment
	o.manager, err = manager.New(o.client.Default().Config(), manager.Options{
		Namespace: "",
		MapperProvider: func(cfg *rest.Config) (meta.RESTMapper, error) {
			return o.client.Default().RESTMapper(), nil
		},
		MetricsBindAddress: "0",
	})
	if err != nil {
		return err
	}

	o.StatusManager = statusmanager.New(o.client.Default().CRClient(), o.client.Default().RESTMapper(), "network")

	// Add controller-runtime controllers
	klog.Info("Adding controller-runtime controllers")
	if err := controller.AddToManager(o.manager, o.StatusManager, o.client); err != nil {
		return fmt.Errorf("Failed to add controllers to manager: %w", err)
	}

	// Initialize individual (non-controller-runtime) controllers

	// logLevelController reacts to changes in the operator spec loglevel
	logLevelController := loglevel.NewClusterOperatorLoggingController(o.client.Default().OperatorHelperClient(), controllerConfig.EventRecorder)

	// managementStateController syncs Operator.Spec.ManagementState down to
	// an Operator.Status.Condition
	managementStateController := managementstatecontroller.NewOperatorManagementStateController("cluster-network-operator", o.client.Default().OperatorHelperClient(), controllerConfig.EventRecorder)
	management.SetOperatorNotRemovable()

	// TODO: Enable the library-go ClusterOperatorStatusController once
	// https://github.com/openshift/library-go/issues/936 is resolved.

	// Start informers
	if err := o.client.Start(ctx); err != nil {
		return fmt.Errorf("Failed to start client: %w", err)
	}

	// Start controllers
	klog.Info("Starting controller-manager")
	go func() {
		err := o.manager.Start(ctx)
		if err != nil {
			klog.Fatalf("Failed to start controller-runtime manager: %v", err)
		}
	}()
	go logLevelController.Run(ctx, 1)
	go managementStateController.Run(ctx, 1)
	if err := connectivitycheck.Start(ctx, o.client.Default().Config()); err != nil {
		klog.Errorf("Failed to start connectivitycheck controller: %v", err)
	}

	<-ctx.Done()

	return nil
}
