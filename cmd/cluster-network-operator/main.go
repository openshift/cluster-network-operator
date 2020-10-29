package main

import (
	"context"
	"flag"
	"log"
	"net/url"
	"os"
	"runtime"

	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	netopv1 "github.com/openshift/cluster-network-operator/pkg/apis/network/v1"
	"github.com/openshift/cluster-network-operator/pkg/controller"
	"github.com/openshift/cluster-network-operator/pkg/controller/connectivitycheck"
	k8sutil "github.com/openshift/cluster-network-operator/pkg/util/k8s"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/runtime/signals"
)

func printVersion() {
	log.Printf("Go Version: %s", runtime.Version())
	log.Printf("Go OS/Arch: %s/%s", runtime.GOOS, runtime.GOARCH)
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
	err = BecomeLeader(context.TODO(), LOCK_NAME)
	if err != nil {
		log.Fatal(err)
	}

	// Create a new Cmd to provide shared dependencies and start components
	mgr, err := manager.New(cfg, manager.Options{
		Namespace:          namespace,
		MapperProvider:     k8sutil.NewDynamicRESTMapper,
		MetricsBindAddress: "0",
	})
	if err != nil {
		log.Fatal(err)
	}

	log.Print("Registering Components.")
	if err := operv1.Install(mgr.GetScheme()); err != nil {
		log.Fatal(err)
	}
	if err := configv1.Install(mgr.GetScheme()); err != nil {
		log.Fatal(err)
	}
	if err := netopv1.Install(mgr.GetScheme()); err != nil {
		log.Fatal(err)
	}

	// setup signal handler for controller-runtime and context
	// for library-go controllers
	stopCh := signals.SetupSignalHandler()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-stopCh
		cancel()
	}()
	defer cancel()

	log.Print("Starting the Cmd.")

	// start library-go controllers
	func() {
		if err := connectivitycheck.Start(ctx, mgr.GetConfig()); err != nil {
			log.Fatal(err)
		}
	}()

	// Setup all controller-runtime Controllers
	log.Print("Configuring Controllers")
	if err := controller.AddToManager(mgr); err != nil {
		log.Fatal(err)
	}

	// Start the Cmd
	if err := mgr.Start(stopCh); err != nil {
		log.Fatal(err)
	}
}
