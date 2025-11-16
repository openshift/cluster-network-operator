package main

import (
	"fmt"
	"os"

	"github.com/openshift-eng/openshift-tests-extension/pkg/cmd"

	"github.com/spf13/cobra"

	e "github.com/openshift-eng/openshift-tests-extension/pkg/extension"
	g "github.com/openshift-eng/openshift-tests-extension/pkg/ginkgo"

	_ "github.com/openshift/cluster-network-operator/test/e2e"
)

func main() {
	registry := e.NewRegistry()

	// Tests should only be run as part of or via openshift-tests, running tests via the extension is not supported.
	ext := e.NewExtension("openshift", "payload", "cluster-network-operator")
	ext.AddSuite(e.Suite{
		Name: "openshift/cluster-network-operator/conformance/parallel",
		Parents: []string{
			"openshift/conformance/parallel",
		},
		Qualifiers: []string{
			"name.contains('[Suite:openshift/cluster-network-operator/conformance/parallel')",
		},
	})

	specs, err := g.BuildExtensionTestSpecsFromOpenShiftGinkgoSuite()
	if err != nil {
		panic(fmt.Sprintf("couldn't build extension test specs from ginkgo: %+v", err.Error()))
	}

	ext.AddSpecs(specs)
	registry.Register(ext)

	root := &cobra.Command{
		Long: "OpenShift Tests Extension for Cluster Network Operator",
	}
	root.AddCommand(cmd.DefaultExtensionCommands(registry)...)

	if err := func() error {
		return root.Execute()
	}(); err != nil {
		os.Exit(1)
	}
}
