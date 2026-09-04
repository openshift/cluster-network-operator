package main

import (
	"fmt"
	"os"

	"github.com/openshift-eng/openshift-tests-extension/pkg/cmd"

	"github.com/spf13/cobra"

	e "github.com/openshift-eng/openshift-tests-extension/pkg/extension"
	et "github.com/openshift-eng/openshift-tests-extension/pkg/extension/extensiontests"
	g "github.com/openshift-eng/openshift-tests-extension/pkg/ginkgo"

	_ "github.com/openshift/cluster-network-operator/test/ote"
)

func main() {
	registry := e.NewRegistry()

	ext := e.NewExtension("openshift", "payload", "cluster-network-operator")

	ext.AddSuite(e.Suite{
		Name:       "cluster-network-operator/conformance/serial",
		Qualifiers: []string{`labels.exists(l, l == "Serial")`},
	})

	ext.AddSuite(e.Suite{
		Name:       "cluster-network-operator/conformance/parallel",
		Qualifiers: []string{`!labels.exists(l, l == "Serial")`},
	})

	specs, err := g.BuildExtensionTestSpecsFromOpenShiftGinkgoSuite()
	if err != nil {
		panic(fmt.Sprintf("couldn't build extension test specs from ginkgo: %+v", err.Error()))
	}

	specs.Walk(func(spec *et.ExtensionTestSpec) {
		spec.Lifecycle = et.LifecycleInforming
	})
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
