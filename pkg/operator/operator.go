package operator

import (
	"context"
	"fmt"
	"net/http"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/openshift/library-go/pkg/operator/loglevel"
	"github.com/openshift/library-go/pkg/operator/management"
	"github.com/openshift/library-go/pkg/operator/managementstatecontroller"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/controller"
	"github.com/openshift/cluster-network-operator/pkg/controller/connectivitycheck"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/cluster-network-operator/pkg/hypershift"
	"github.com/openshift/cluster-network-operator/pkg/names"

	ctlog "sigs.k8s.io/controller-runtime/pkg/log"
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

var logger = klog.NewKlogr()

func RunOperator(ctx context.Context, controllerConfig *controllercmd.ControllerContext, inClusterClientName string, extraClusters map[string]string) error {
	o := &Operator{}

	var err error

	// Call SetLogger before adding controller-runtime client to prevent controller-runtime
	// complaining about it after 30 seconds of binaries lifetime
	// https://github.com/kubernetes-sigs/controller-runtime/blob/main/pkg/log/log.go#L54
	ctlog.SetLogger(logger)
	if o.client, err = cnoclient.NewClient(controllerConfig.KubeConfig, controllerConfig.ProtoKubeConfig, inClusterClientName, extraClusters); err != nil {
		return err
	}

	// initialize the controller-runtime environment
	o.manager, err = manager.New(o.client.Default().Config(), manager.Options{
		MapperProvider: func(cfg *rest.Config, httpClient *http.Client) (meta.RESTMapper, error) {
			return o.client.Default().RESTMapper(), nil
		},
		Metrics: metricsserver.Options{BindAddress: "0"},
		Logger:  klog.Background(),
	})
	if err != nil {
		return err
	}

	// In HyperShift use the infrastructure name to differentiate between resources deployed by the management cluster CNO and CNO deployed in the hosted clusters control plane namespace
	// Without that the CNO running against the management cluster would pick the resources rendered by the hosted cluster CNO
	cluster := names.StandAloneClusterName
	if hcp := hypershift.NewHyperShiftConfig(); hcp.Enabled {
		// retry every 5s up to 60s
		var backoff = wait.Backoff{
			Steps:    12,
			Duration: 5 * time.Second,
			Factor:   1.0,
			Jitter:   0.1,
		}
		infraConfig := &configv1.Infrastructure{}

		err := retry.OnError(backoff, func(error) bool { return true }, func() error {
			if err := o.client.Default().CRClient().Get(context.TODO(), types.NamespacedName{Name: "cluster"}, infraConfig); err != nil {
				return fmt.Errorf("failed to get infrastructure 'cluster': %v", err)
			}
			if infraConfig.Status.InfrastructureName == "" {
				return fmt.Errorf("infrastructureName not set in infrastructure 'cluster'")
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("failed to get infrastructure name: %v", err)
		}
		cluster = infraConfig.Status.InfrastructureName
	}

	klog.Infof("Creating status manager for %s cluster", cluster)
	o.StatusManager = statusmanager.New(o.client, "network", cluster)
	defer utilruntime.HandleCrash(o.StatusManager.SetDegradedOnPanicAndCrash)

	// Add controller-runtime controllers
	klog.Info("Adding controller-runtime controllers")
	if err := controller.AddToManager(o.manager, o.StatusManager, o.client); err != nil {
		return fmt.Errorf("failed to add controllers to manager: %w", err)
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
