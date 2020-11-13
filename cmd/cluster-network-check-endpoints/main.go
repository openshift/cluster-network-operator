package main

import (
	"fmt"
	"os"

	"github.com/openshift/cluster-network-operator/pkg/cmd/checkendpoints"
)

func main() {
	command := checkendpoints.NewCheckEndpointsCommand()
	if err := command.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}
