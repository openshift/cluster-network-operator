package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/hypershift"
	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/network"
	"github.com/openshift/cluster-network-operator/pkg/operator"
	"github.com/openshift/cluster-network-operator/pkg/version"
	libgoclient "github.com/openshift/library-go/pkg/config/client"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/openshift/library-go/pkg/crypto"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/serviceability"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/apiserver/pkg/server"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/client-go/rest"
	utilflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/logs"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
)

const componentName = "network-operator"

func main() {
	pflag.CommandLine.SetNormalizeFunc(utilflag.WordSepNormalizeFunc)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)

	logs.InitLogs()
	defer logs.FlushLogs()

	command := newNetworkOperatorCommand()

	if err := command.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func newNetworkOperatorCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "network-operator",
		Short: "Openshift Cluster Network Operator",
		Long:  "Run the network operator",
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Help()
			os.Exit(1)
		},
	}

	cmdcfg := controllercmd.NewControllerCommandConfig(componentName, version.Get(), nil, clock.RealClock{})

	cmd2 := newCommandWithTLSCustomization(cmdcfg)
	cmd2.Use = "start"
	cmd2.Short = "Start the cluster network operator"
	cmd.AddCommand(cmd2)

	cmd.AddCommand(newMTUProberCommand())

	return cmd
}

// newCommandWithTLSCustomization creates a custom command that allows customizing TLS
// settings based on the cluster's TLS security profile
func newCommandWithTLSCustomization(cmdcfg *controllercmd.ControllerCommandConfig) *cobra.Command {
	cmd := cmdcfg.NewCommandWithContext(context.Background())

	// Add custom flags
	var extraClusters map[string]string
	var inClusterClientName string
	cmd.Flags().StringToStringVar(&extraClusters, "extra-clusters", nil, "extra clusters, pairs of cluster name and kubeconfig path")
	cmd.Flags().StringVar(&inClusterClientName, "in-cluster-client-name", names.DefaultClusterName, "client name for in-cluster config(service account or kubeconfig)")

	// Replace with custom Run that intercepts to customize TLS
	cmd.Run = func(cmd *cobra.Command, args []string) {
		// Standard boilerplate from library-go
		logs.InitLogs()

		ctx := server.SetupSignalContext()

		defer logs.FlushLogs()
		defer serviceability.BehaviorOnPanic(os.Getenv("OPENSHIFT_ON_PANIC"), version.Get())()
		defer serviceability.Profile(os.Getenv("OPENSHIFT_PROFILE")).Stop()

		serviceability.StartProfiler()

		// Get kubeconfig and namespace from the parsed flags. Unfortunately we can't access cmdcfg.basicFlags directly.
		kubeConfigFile, _ := cmd.Flags().GetString("kubeconfig")
		namespace, _ := cmd.Flags().GetString("namespace")
		bindAddress, _ := cmd.Flags().GetString("listen")

		if err := startControllerWithTLSCustomization(ctx, cmdcfg, extraClusters, inClusterClientName, kubeConfigFile,
			namespace, bindAddress); err != nil {
			klog.Fatal(err)
		}
	}

	return cmd
}

// startControllerWithTLSCustomization starts the controller with customized TLS settings
func startControllerWithTLSCustomization(ctx context.Context, cmdcfg *controllercmd.ControllerCommandConfig,
	extraClusters map[string]string, inClusterClientName string, kubeConfigFile string, namespace string, bindAddress string) error {
	// Get the base config
	unstructuredConfig, config, configContent, err := cmdcfg.Config()
	if err != nil {
		return err
	}

	// Let library-go set up certificate rotation (handles service-serving-cert)
	startingFileContent, observedFiles, err := cmdcfg.AddDefaultRotationToConfig(config, configContent)
	if err != nil {
		return err
	}

	// Apply the --listen flag to override the default bind address (mimics library-go's StartController behavior)
	if len(bindAddress) != 0 {
		config.ServingInfo.BindAddress = bindAddress
	}

	// Customize TLS settings based on cluster profile
	if err := applyClusterTLSProfile(ctx, config, kubeConfigFile, inClusterClientName, extraClusters); err != nil {
		return fmt.Errorf("failed to apply cluster TLS profile: %w", err)
	}

	controllerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// exitOnChangeReactorCh is used by the file watcher to trigger restart on cert file changes
	exitOnChangeReactorCh := make(chan struct{})
	go func() {
		<-exitOnChangeReactorCh
		klog.Infof("Certificate file change detected, triggering graceful restart")
		cancel()
	}()

	// Create startFunc that passes the cancel function to RunOperator
	// The TLS controller will call cancel() when TLS profile changes are detected
	startFunc := func(ctx context.Context, controllerConfig *controllercmd.ControllerContext) error {
		return operator.RunOperator(ctx, controllerConfig, inClusterClientName, extraClusters, cancel)
	}

	// Build the controller with our customized ServingInfo
	builder := controllercmd.NewController(componentName, startFunc, clock.RealClock{}).
		WithKubeConfigFile(kubeConfigFile, nil).
		WithComponentNamespace(namespace).
		WithLeaderElection(config.LeaderElection, namespace, componentName+"-lock").
		WithVersion(version.Get()).
		WithEventRecorderOptions(events.RecommendedClusterSingletonCorrelatorOptions()).
		WithRestartOnChange(exitOnChangeReactorCh, startingFileContent, observedFiles...).
		WithComponentOwnerReference(cmdcfg.ComponentOwnerReference).
		WithServer(config.ServingInfo, config.Authentication, config.Authorization)

	return builder.Run(controllerCtx, unstructuredConfig)
}

// applyClusterTLSProfile fetches the cluster's TLS security profile and applies it to the config
func applyClusterTLSProfile(ctx context.Context, config *operatorv1alpha1.GenericOperatorConfig, kubeConfigFile string, inClusterClientName string, extraClusters map[string]string) error {
	restConfig, err := libgoclient.GetKubeConfigOrInClusterConfig(kubeConfigFile, nil)
	if err != nil {
		return fmt.Errorf("failed to build kubeconfig: %w", err)
	}

	// Create protoConfig for performance (used by kubernetes.Interface)
	protoConfig := rest.CopyConfig(restConfig)
	protoConfig.AcceptContentTypes = "application/vnd.kubernetes.protobuf,application/json"
	protoConfig.ContentType = "application/vnd.kubernetes.protobuf"

	client, err := cnoclient.NewClient(restConfig, protoConfig, inClusterClientName, extraClusters)
	if err != nil {
		return fmt.Errorf("failed to create CNO client: %w", err)
	}

	// Fetch HostedControlPlane for HyperShift (if applicable)
	hcp, err := hypershift.GetHostedControlPlane(client)
	if err != nil {
		return fmt.Errorf("failed to get HostedControlPlane: %w", err)
	}

	// Fetch TLS profile using network.GetTLSProfile (handles both standalone and HyperShift)
	tlsProfile, err := network.GetTLSProfile(client, hcp)
	if err != nil {
		return fmt.Errorf("failed to get TLS profile: %w", err)
	}

	// Check if we should honor the cluster TLS profile
	if !crypto.ShouldHonorClusterTLSProfile(tlsProfile.Adherence) {
		klog.Infof("TLS adherence policy is %q, using default TLS settings", tlsProfile.Adherence)
		return nil
	}

	// Apply the TLS settings to the serving config
	config.ServingInfo.MinTLSVersion = string(tlsProfile.Spec.MinTLSVersion)
	config.ServingInfo.CipherSuites = crypto.OpenSSLToIANACipherSuites(tlsProfile.Spec.Ciphers)

	klog.Infof("Applied cluster TLS profile: minTLSVersion=%s, ciphers=%v",
		tlsProfile.Spec.MinTLSVersion, tlsProfile.Spec.Ciphers)

	return nil
}
