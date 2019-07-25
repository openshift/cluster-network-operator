package main

import (
	"context"
	"flag"
	"log"
	"net/url"
	"os"
	"runtime"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/controller"
	k8sutil "github.com/openshift/cluster-network-operator/pkg/util/k8s"
	"github.com/operator-framework/operator-sdk/pkg/leader"
	sdkVersion "github.com/operator-framework/operator-sdk/version"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
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
	mgr, err := manager.New(cfg, manager.Options{
		Namespace:      namespace,
		MapperProvider: k8sutil.NewDynamicRESTMapper,
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

	// Setup all Controllers
	log.Print("Configuring Controllers")
	if err := controller.AddToManager(mgr); err != nil {
		log.Fatal(err)
	}

	log.Print("Starting the Cmd.")

	// Create the stop channel and start the operator
	stop := signals.SetupSignalHandler()
	if err := start(mgr, stop); err != nil {
		log.Fatal(err)
	}
}

// start creates the default Proxy if it does not exist and then
// starts the operator synchronously until a message is received
// on the stop channel.
func start(mgr manager.Manager, stop <-chan struct{}) error {
	// Periodically ensure the default proxy exists.
	go wait.Until(func() {
		if !mgr.GetCache().WaitForCacheSync(stop) {
			log.Print("failed to sync cache before ensuring default proxy")
			return
		}
		err := ensureDefaultProxy(mgr)
		if err != nil {
			log.Print(err, "failed to ensure default proxy")
		}
	}, 1*time.Minute, stop)

	errChan := make(chan error)
	go func() {
		errChan <- mgr.Start(stop)
	}()

	// Wait for the manager to exit or an explicit stop.
	select {
	case <-stop:
		return nil
	case err := <-errChan:
		return err
	}
}

// ensureDefaultProxy creates the default proxy if it doesn't exist.
func ensureDefaultProxy(mgr manager.Manager) error {
	proxy := &configv1.Proxy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
	}
	client := mgr.GetClient()
	err := client.Get(context.TODO(), types.NamespacedName{Name: proxy.Name}, proxy)
	if err == nil {
		return nil
	}
	if !errors.IsNotFound(err) {
		return err
	}
	err = client.Create(context.TODO(), proxy)
	if err != nil {
		return err
	}
	log.Printf("created default proxy %q", proxy.Name)
	return nil
}
