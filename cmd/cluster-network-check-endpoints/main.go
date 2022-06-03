package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/openshift/cluster-network-operator/pkg/cmd/checkendpoints"
	"k8s.io/klog/v2"
)

func init() {
	klog.InitFlags(flag.CommandLine)
}

func main() {
	command := checkendpoints.NewCheckEndpointsCommand()
	if err := command.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}
