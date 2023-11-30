package operator

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
	configinformers "github.com/openshift/client-go/config/informers/externalversions"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/controller"
	"github.com/openshift/cluster-network-operator/pkg/controller/connectivitycheck"
	"github.com/openshift/cluster-network-operator/pkg/controller/statusmanager"
	"github.com/openshift/cluster-network-operator/pkg/hypershift"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/loglevel"
	"github.com/openshift/library-go/pkg/operator/management"
	"github.com/openshift/library-go/pkg/operator/managementstatecontroller"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

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

func RunOperator(ctx context.Context, controllerConfig *controllercmd.ControllerContext, inClusterClientName string, extraClusters map[string]string) error {
	o := &Operator{}

	var err error
	if o.client, err = cnoclient.NewClient(controllerConfig.KubeConfig, controllerConfig.ProtoKubeConfig, inClusterClientName, extraClusters); err != nil {
		return err
	}

	// initialize the controller-runtime environment
	o.manager, err = manager.New(o.client.Default().Config(), manager.Options{
		Namespace: "",
		MapperProvider: func(cfg *rest.Config, httpClient *http.Client) (meta.RESTMapper, error) {
			return o.client.Default().RESTMapper(), nil
		},
		MetricsBindAddress: "0",
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
	klog.Infof("Creating feature gate accessor")
	featureGateAccessor, err := setupFeatureGateAccessor(ctx, o.client.Default())
	if err != nil {
		return fmt.Errorf("failed to setup featuregate accessor: %w", err)
	}
	featureGates, err := featureGateAccessor.CurrentFeatureGates()
	if err != nil {
		return fmt.Errorf("failed to get current featuregates: %w", err)
	}

	klog.Infof("Creating status manager for %s cluster", cluster)
	o.StatusManager = statusmanager.New(o.client, "network", cluster)
	defer utilruntime.HandleCrash(o.StatusManager.SetDegradedOnPanicAndCrash)

	// Add controller-runtime controllers
	klog.Info("Adding controller-runtime controllers")
	if err := controller.AddToManager(o.manager, o.StatusManager, o.client, featureGates); err != nil {
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

// setupFeatureGateAccessor creates and starts a new feature gate accessor.
// If the featuregates change at runtime, the process will exit(0)
func setupFeatureGateAccessor(ctx context.Context, c cnoclient.ClusterClient) (featuregates.FeatureGateAccess, error) {
	configClient, err := configclient.NewForConfig(c.Config())
	if err != nil {
		return nil, err
	}
	configInformers := configinformers.NewSharedInformerFactory(configClient, 10*time.Minute)
	desiredVersion := os.Getenv("RELEASE_VERSION")
	missingVersion := "0.0.1-snapshot"

	eventRecorder := events.NewKubeRecorder(c.Kubernetes().CoreV1().Events("openshift-network-operator"), "cluster-network-operator", &corev1.ObjectReference{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Namespace:  "openshift-network-operator",
		Name:       "network-operator",
	})

	// By default, this will exit(0) the process if the featuregates ever change to a different set of values.
	featureGateAccessor := featuregates.NewFeatureGateAccess(
		desiredVersion, missingVersion,
		configInformers.Config().V1().ClusterVersions(), configInformers.Config().V1().FeatureGates(),
		eventRecorder,
	)

	go featureGateAccessor.Run(ctx)
	go configInformers.Start(ctx.Done())
	klog.Infof("Waiting for feature gates initialization...")
	select {
	case <-featureGateAccessor.InitialFeatureGatesObserved():
		featureGates, err := featureGateAccessor.CurrentFeatureGates()
		if err != nil {
			return nil, err
		} else {
			klog.Infof("FeatureGates initialized: knownFeatureGates=%v", featureGates.KnownFeatures())
		}
	case <-time.After(1 * time.Minute):
		return nil, fmt.Errorf("timed out waiting for FeatureGate detection")
	}

	return featureGateAccessor, nil
}
