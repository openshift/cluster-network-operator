package main

import (
	"fmt"
	"os"
	"time"

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

	// Disruptive tests run one at a time under the disruptive cluster-stability
	// classification. Serial/parallel suites are intentionally omitted for now;
	// the migrated tests are all disruptive.
	// Extended timeout for disruptive tests that involve MCP rollouts.
	testTimeout := 300 * time.Minute // 5 hours
	ext.AddSuite(e.Suite{
		Name:             "cluster-network-operator/conformance/disruptive",
		Qualifiers:       []string{`labels.exists(l, l == "Disruptive")`},
		Parallelism:      1,
		ClusterStability: e.ClusterStabilityDisruptive,
		TestTimeout:      &testTimeout,
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
