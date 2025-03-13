package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/openshift/cluster-network-operator/pkg/names"
	"github.com/openshift/cluster-network-operator/pkg/operator"
	"github.com/openshift/cluster-network-operator/pkg/version"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"

	utilflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/logs"
	"k8s.io/utils/clock"
)

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
	var extraClusters *map[string]string
	var inClusterClientName *string
	cmdcfg := controllercmd.NewControllerCommandConfig("network-operator", version.Get(), func(ctx context.Context, controllerConfig *controllercmd.ControllerContext) error {
		return operator.RunOperator(ctx, controllerConfig, *inClusterClientName, *extraClusters)
	}, clock.RealClock{})

	cmd2 := cmdcfg.NewCommand()
	cmd2.Use = "start"
	cmd2.Short = "Start the cluster network operator"
	extraClusters = cmd2.Flags().StringToString("extra-clusters", nil, "extra clusters, pairs of cluster name and kubeconfig path")
	inClusterClientName = cmd2.Flags().String("in-cluster-client-name", names.DefaultClusterName, "client name for in-cluster config(service account or kubeconfig)")
	cmd.AddCommand(cmd2)

	cmd.AddCommand(newMTUProberCommand())

	return cmd
}
