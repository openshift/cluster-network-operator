package checkendpoints

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorcontrolplaneclient "github.com/openshift/client-go/operatorcontrolplane/clientset/versioned"
	operatorcontrolplaneinformers "github.com/openshift/client-go/operatorcontrolplane/informers/externalversions"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/cmd/checkendpoints/controller"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/network"
	openshifttls "github.com/openshift/controller-runtime-common/pkg/tls"
	"github.com/openshift/library-go/pkg/config/configdefaults"
	"github.com/openshift/library-go/pkg/crypto"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resource/retry"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextensionsinformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/component-base/logs"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func NewCheckEndpointsCommand() *cobra.Command {
	var listenAddr string

	cmd := &cobra.Command{
		Use:   "check-endpoints",
		Short: "Checks that a tcp connection can be opened to one or more endpoints.",
		Run: func(cmd *cobra.Command, args []string) {
			logs.InitLogs()
			defer logs.FlushLogs()

			ctx := ctrl.SetupSignalHandler()
			podName := os.Getenv("POD_NAME")
			namespace := os.Getenv("POD_NAMESPACE")

			if err := run(ctx, listenAddr, podName, namespace); err != nil {
				klog.Fatal(err)
			}
		},
	}

	cmd.Flags().StringVar(&listenAddr, "listen", ":17698", "The ip:port to serve metrics on.")

	return cmd
}

func run(ctx context.Context, listenAddr, podName, namespace string) error {
	restConfig := ctrl.GetConfigOrDie()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	tlsProfile, err := fetchClusterTLSProfile(restConfig)
	if err != nil {
		return fmt.Errorf("failed to fetch TLS profile: %w", err)
	}

	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Metrics: metricsserver.Options{
			BindAddress:   listenAddr,
			SecureServing: true,
			TLSOpts:       buildTLSOptions(tlsProfile),
		},
	})
	if err != nil {
		return fmt.Errorf("unable to create manager: %w", err)
	}

	tlsProfileWatcher := &openshifttls.SecurityProfileWatcher{
		Client:                    mgr.GetClient(),
		InitialTLSProfileSpec:     tlsProfile.Spec,
		InitialTLSAdherencePolicy: tlsProfile.Adherence,
		OnProfileChange: func(_ context.Context, oldProfile, newProfile configv1.TLSProfileSpec) {
			klog.Info(fmt.Sprintf("TLS security profile changed. Old: MinVersion=%s Ciphers=%d, New: MinVersion=%s Ciphers=%d",
				oldProfile.MinTLSVersion, len(oldProfile.Ciphers), newProfile.MinTLSVersion, len(newProfile.Ciphers)))
			cancel()
		},
		OnAdherencePolicyChange: func(_ context.Context, oldTLSAdherencePolicy, newTLSAdherencePolicy configv1.TLSAdherencePolicy) {
			klog.Info(fmt.Sprintf("TLS Adherence policy changed. Old: %s, New: %s", oldTLSAdherencePolicy, newTLSAdherencePolicy))
			cancel()
		},
	}

	if err = tlsProfileWatcher.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("failed to setup TLS profile watcher: %w", err)
	}

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

// fetchClusterTLSProfile fetches the TLS profile from the cluster
func fetchClusterTLSProfile(restConfig *rest.Config) (*bootstrap.TLSProfile, error) {
	client, err := cnoclient.NewClient(restConfig, restConfig, names.DefaultClusterName, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create client: %w", err)
	}

	tlsProfile, err := network.GetTLSProfile(client, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get TLS profile: %w", err)
	}

	return &tlsProfile, nil
}

// buildTLSOptions creates TLS config functions from the cluster TLS profile
func buildTLSOptions(tlsProfile *bootstrap.TLSProfile) []func(*tls.Config) {
	var profileSpec configv1.TLSProfileSpec

	if crypto.ShouldHonorClusterTLSProfile(tlsProfile.Adherence) {
		// Use cluster-specified TLS profile
		profileSpec = tlsProfile.Spec
		klog.Infof("Using cluster TLS profile: minVersion=%s, ciphers=%d", profileSpec.MinTLSVersion, len(profileSpec.Ciphers))
	} else {
		// Use library-go strong defaults. This ensures we don't fall back to weaker Go defaults
		servingInfo := &configv1.ServingInfo{}
		configdefaults.SetRecommendedServingInfoDefaults(servingInfo)
		profileSpec = configv1.TLSProfileSpec{
			MinTLSVersion: configv1.TLSProtocolVersion(servingInfo.MinTLSVersion),
			Ciphers:       servingInfo.CipherSuites,
		}

		klog.Infof("TLS adherence policy is %q, using library-go default TLS settings (TLS 1.2 + strong ciphers)",
			tlsProfile.Adherence)
	}

	tlsConfigFunc, unsupportedCiphers := openshifttls.NewTLSConfigFromProfile(profileSpec)
	if len(unsupportedCiphers) > 0 {
		klog.Warningf("Unsupported TLS cipher suites: %v", unsupportedCiphers)
	}
	return []func(*tls.Config){tlsConfigFunc}
}
