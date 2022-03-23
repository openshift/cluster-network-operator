package main

import (
	"context"
	"flag"
	"fmt"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	"log"
	"math/rand"
	"net/url"
	"os"
	"time"

	"github.com/openshift/cluster-network-operator/pkg/operator"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/client-go/tools/clientcmd"

	_ "github.com/openshift/cluster-network-operator/pkg/client"
	"github.com/openshift/cluster-network-operator/pkg/version"

	utilflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/logs"
)

const ENV_URL_KUBECONFIG = "URL_ONLY_KUBECONFIG"

func main() {
	rand.Seed(time.Now().UTC().UnixNano())

	pflag.CommandLine.SetNormalizeFunc(utilflag.WordSepNormalizeFunc)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)

	logs.InitLogs()
	defer logs.FlushLogs()

	command := newNetworkOperatorCommand()

	// Hack: the network operator can't use the apiserver service ip, since there's
	// no network. We also can't hard-code it to 127.0.0.1, because we run during
	// bootstrap. Instead, we bind-mount in the kubelet's kubeconfig, but just
	// use it to get the apiserver url.
	if kc := os.Getenv(ENV_URL_KUBECONFIG); kc != "" {
		kubeconfig, err := clientcmd.LoadFromFile(kc)
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

	if err := command.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func newNetworkOperatorCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "network-operator",
		Short: "Openshift Cluster Network Operator",
		Long: `Run the network operator.
This supports an additional environment variable, URL_ONLY_KUBECONFIG,
which is a kubeconfig from which to take just the URL to the apiserver`,
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Help()
			os.Exit(1)
		},
	}
	var extraClusters *map[string]string
	var inClusterClientName *string
	cmdcfg := controllercmd.NewControllerCommandConfig("network-operator", version.Get(), func(ctx context.Context, controllerConfig *controllercmd.ControllerContext) error {
		return operator.RunOperator(ctx, controllerConfig, *inClusterClientName, *extraClusters)
	})

	cmd2 := cmdcfg.NewCommand()
	cmd2.Use = "start"
	cmd2.Short = "Start the cluster network operator"
	extraClusters = cmd2.Flags().StringToString("extra-clusters", nil, "extra clusters, pairs of cluster name and kubeconfig path")
	inClusterClientName = cmd2.Flags().String("in-cluster-client-name", cnoclient.DefaultClusterName, "client name for in-cluster config(service account or kubeconfig)")
	cmd.AddCommand(cmd2)

	cmd.AddCommand(newMTUProberCommand())

	return cmd
}
