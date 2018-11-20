package main

import (
	"context"
	"flag"
	"log"
	"net/url"
	"os"
	"runtime"

	"github.com/openshift/cluster-network-operator/pkg/apis"
	"github.com/openshift/cluster-network-operator/pkg/controller"
	"github.com/operator-framework/operator-sdk/pkg/leader"
	sdkVersion "github.com/operator-framework/operator-sdk/version"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/runtime/signals"
)

func printVersion() {
	log.Printf("Go Version: %s", runtime.Version())
	log.Printf("Go OS/Arch: %s/%s", runtime.GOOS, runtime.GOARCH)
	log.Printf("operator-sdk Version: %v", sdkVersion.Version)
}

const LOCK_NAME = "cluster-network-operator"

// urlOnlyKubeconfig is a slight hack; we need to get the apiserver from the
// kubeconfig but should use the in-cluster service account
var urlOnlyKubeconfig string

func init() {
	flag.StringVar(&urlOnlyKubeconfig, "url-only-kubeconfig", "",
		"Path to a kubeconfig, but only for the apiserver url.")
}

func main() {
	printVersion()
	flag.Parse()

	namespace := "" // non-namespaced

	// TODO: Expose metrics port after SDK uses controller-runtime's dynamic client
	// sdk.ExposeMetricsPort()

	// Hack: the network operator can't use the apiserver service ip, since there's
	// no network. We also can't hard-code it to 127.0.0.1, because we run during
	// bootstrap. Instead, we bind-mount in the kubelet's kubeconfig, but just
	// use it to get the apiserver url.
	if urlOnlyKubeconfig != "" {
		kubeconfig, err := clientcmd.LoadFromFile(urlOnlyKubeconfig)
		if err != nil {
			log.Fatal(err)
		}
		clusterName := kubeconfig.Contexts[kubeconfig.CurrentContext].Cluster
		apiURL := kubeconfig.Clusters[clusterName].Server

		url, err := url.Parse(apiURL)
		if err != nil {
			log.Fatal(err)
		}

		// The kubernetes in-cluster functions don't let you override the apiserver
		// directly; gotta "pass" it via environment vars.
		log.Printf("overriding kubernetes api to %s", apiURL)
		os.Setenv("KUBERNETES_SERVICE_HOST", url.Hostname())
		os.Setenv("KUBERNETES_SERVICE_PORT", url.Port())
	}

	// Get a config to talk to the apiserver
	cfg, err := config.GetConfig()
	if err != nil {
		log.Fatal(err)
	}

	// become leader
	err = leader.Become(context.TODO(), LOCK_NAME)
	if err != nil {
		log.Fatal(err)
	}

	// Create a new Cmd to provide shared dependencies and start components
	mgr, err := manager.New(cfg, manager.Options{Namespace: namespace})
	if err != nil {
		log.Fatal(err)
	}

	log.Print("Registering Components.")

	// Setup Scheme for all resources
	if err := apis.AddToScheme(mgr.GetScheme()); err != nil {
		log.Fatal(err)
	}

	// Setup all Controllers
	if err := controller.AddToManager(mgr); err != nil {
		log.Fatal(err)
	}

	log.Print("Starting the Cmd.")

	// Start the Cmd
	log.Fatal(mgr.Start(signals.SetupSignalHandler()))
}
