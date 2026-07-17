package checkendpoints

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"time"

	operatorcontrolplaneclient "github.com/openshift/client-go/operatorcontrolplane/clientset/versioned"
	operatorcontrolplaneinformers "github.com/openshift/client-go/operatorcontrolplane/informers/externalversions"
	"github.com/openshift/cluster-network-operator/pkg/cmd/checkendpoints/controller"
	"github.com/openshift/cluster-network-operator/pkg/version"
	"github.com/openshift/library-go/pkg/crypto"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resource/retry"
	"github.com/openshift/library-go/pkg/serviceability"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextensionsinformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/logs"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func NewCheckEndpointsCommand() *cobra.Command {
	var (
		listenAddr      string
		tlsMinVersion   string
		tlsCipherSuites []string
	)

	cmd := &cobra.Command{
		Use:   "check-endpoints",
		Short: "Checks that a tcp connection can be opened to one or more endpoints.",
		Run: func(cmd *cobra.Command, args []string) {
			logs.InitLogs()
			defer logs.FlushLogs()

			ctx := ctrl.SetupSignalHandler()
			podName := os.Getenv("POD_NAME")
			namespace := os.Getenv("POD_NAMESPACE")

			if err := run(ctx, listenAddr, tlsMinVersion, tlsCipherSuites, podName, namespace); err != nil {
				klog.Fatal(err)
			}
		},
	}

	cmd.Flags().StringVar(&listenAddr, "listen", ":17698", "The ip:port to serve metrics on.")
	cmd.Flags().StringVar(&tlsMinVersion, "tls-min-version", "", "Minimum TLS version (e.g., VersionTLS12, VersionTLS13)")
	cmd.Flags().StringSliceVar(&tlsCipherSuites, "tls-cipher-suites", nil, "Comma-separated list of TLS cipher suites")

	return cmd
}

func run(ctx context.Context, listenAddr, tlsMinVersion string, tlsCipherSuites []string, podName, namespace string) error {
	restConfig := ctrl.GetConfigOrDie()

	tlsOpts, err := buildTLSOptions(tlsMinVersion, tlsCipherSuites)
	if err != nil {
		return fmt.Errorf("failed to build TLS options: %w", err)
	}

	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Metrics: metricsserver.Options{
			BindAddress:   listenAddr,
			SecureServing: true,
			TLSOpts:       tlsOpts,
		},
	})
	if err != nil {
		return fmt.Errorf("unable to create manager: %w", err)
	}

	defer serviceability.BehaviorOnPanic(os.Getenv("OPENSHIFT_ON_PANIC"), version.Get())()
	defer serviceability.Profile(os.Getenv("OPENSHIFT_PROFILE")).Stop()

	serviceability.StartProfiler()

	go func() {
		klog.Info("Starting the metrics server")
		if err := mgr.Start(ctx); err != nil {
			klog.Fatalf("Problem running metrics server: %v", err)
		}
	}()

	klog.Info("Running the controllers")

	return runControllers(ctx, restConfig, podName, namespace)
}

// runControllers runs the factory.Controller-based logic
func runControllers(ctx context.Context, restConfig *rest.Config, podName, namespace string) error {
	kubeClient := kubernetes.NewForConfigOrDie(restConfig)
	apiextensionsClient := apiextensionsclient.NewForConfigOrDie(restConfig)
	operatorcontrolplaneClient := operatorcontrolplaneclient.NewForConfigOrDie(restConfig)

	kubeInformers := informers.NewSharedInformerFactoryWithOptions(kubeClient, 10*time.Minute, informers.WithNamespace(namespace))
	operatorcontrolplaneInformers := operatorcontrolplaneinformers.NewSharedInformerFactoryWithOptions(operatorcontrolplaneClient,
		10*time.Minute, operatorcontrolplaneinformers.WithNamespace(namespace))
	apiextensionsInformers := apiextensionsinformers.NewSharedInformerFactory(apiextensionsClient, 10*time.Minute)

	// create a recorder that sets the pod node as the involved object in events
	var involvedObjectRef *corev1.ObjectReference
	err := retry.RetryOnConnectionErrors(ctx, func(context.Context) (bool, error) {
		pod, err := kubeClient.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		node, err := kubeClient.CoreV1().Nodes().Get(ctx, pod.Spec.NodeName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		involvedObjectRef = &corev1.ObjectReference{
			Kind:       "Node",
			Namespace:  namespace,
			Name:       node.Name,
			UID:        node.UID,
			APIVersion: node.APIVersion,
		}

		return true, nil
	})
	if err != nil {
		return err
	}

	recorder := events.NewRecorder(kubeClient.CoreV1().Events(namespace), "check-endpoint", involvedObjectRef, clock.RealClock{})

	check := controller.NewPodNetworkConnectivityCheckController(
		podName,
		namespace,
		operatorcontrolplaneClient.ControlplaneV1alpha1(),
		operatorcontrolplaneInformers.Controlplane().V1alpha1().PodNetworkConnectivityChecks(),
		kubeInformers.Core().V1().Secrets(),
		recorder,
	)

	timeToStart := newTimeToStartController(apiextensionsInformers.Apiextensions().V1().CustomResourceDefinitions(), recorder)

	stopController := newStopController(apiextensionsInformers.Apiextensions().V1().CustomResourceDefinitions(), recorder)

	controller.RegisterMetrics()

	// block until the PodNetworkConnectivityCheck CRD exists
	apiextensionsInformers.Start(ctx.Done())
	ttsContext, ttsCancel := context.WithCancel(ctx)
	go timeToStart.Run(ttsContext, 1)
	select {
	case err := <-timeToStart.Ready():
		ttsCancel()
		if err != nil {
			return err
		}
	case <-ctx.Done():
		ttsCancel()
		return nil
	}

	operatorcontrolplaneInformers.Start(ctx.Done())
	kubeInformers.Start(ctx.Done())
	go check.Run(ctx, 1)
	go stopController.Run(ctx, 1)
	<-ctx.Done()

	return nil
}

// buildTLSOptions creates TLS config functions from CLI flags or falls back to library-go defaults
func buildTLSOptions(tlsMinVersion string, tlsCipherSuites []string) ([]func(*tls.Config), error) {
	tlsMinVersionID, err := cliflag.TLSVersion(tlsMinVersion)
	if err != nil {
		return nil, fmt.Errorf("error parsing TLS min version %q: %w", tlsMinVersion, err)
	}

	tlsCipherSuiteIDs, err := cliflag.TLSCipherSuites(tlsCipherSuites)
	if err != nil {
		return nil, fmt.Errorf("error parsing TLS cipher suites %v: %w", tlsCipherSuites, err)
	}

	return []func(*tls.Config){func(cfg *tls.Config) {
		// Set from CLI args first
		cfg.MinVersion = tlsMinVersionID
		cfg.CipherSuites = tlsCipherSuiteIDs

		// Apply library-go strong defaults for any unset fields
		crypto.SecureTLSConfig(cfg)
	}}, nil
}
