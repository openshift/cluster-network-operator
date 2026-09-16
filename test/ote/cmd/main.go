package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/openshift-eng/openshift-tests-extension/pkg/cmd"
	"github.com/openshift-eng/openshift-tests-extension/pkg/util/sets"

	"github.com/spf13/cobra"

	e "github.com/openshift-eng/openshift-tests-extension/pkg/extension"
	et "github.com/openshift-eng/openshift-tests-extension/pkg/extension/extensiontests"
	g "github.com/openshift-eng/openshift-tests-extension/pkg/ginkgo"

	_ "github.com/openshift/cluster-network-operator/test/ote"
)

// generatePrependedLabelsStr builds a bracketed, sorted label prefix (e.g. "[Serial]")
// from a spec's labels. Origin serializes any test whose name contains "[Serial]", so
// prepending this to the test name lets serial tests run one-at-a-time without needing
// Suite.Parallelism.
func generatePrependedLabelsStr(labels sets.Set[string]) string {
	labelList := labels.UnsortedList()
	sort.Strings(labelList)

	var labelsStr string
	for _, label := range labelList {
		labelsStr += "[" + label + "]"
	}
	return labelsStr
}

func main() {
	registry := e.NewRegistry()

	ext := e.NewExtension("openshift", "payload", "cluster-network-operator")

	ext.AddSuite(e.Suite{
		Name:       "cluster-network-operator/conformance/serial",
		Qualifiers: []string{`labels.exists(l, l == "Serial") && !labels.exists(l, l == "Disruptive")`},
	})

	ext.AddSuite(e.Suite{
		Name:       "cluster-network-operator/conformance/parallel",
		Qualifiers: []string{`!labels.exists(l, l == "Serial") && !labels.exists(l, l == "Disruptive")`},
	})

	// Disruptive tests run one at a time under the disruptive cluster-stability
	// classification and are excluded from the serial/parallel suites above.
	ext.AddSuite(e.Suite{
		Name:             "cluster-network-operator/conformance/disruptive",
		Qualifiers:       []string{`labels.exists(l, l == "Disruptive")`},
		Parallelism:      1,
		ClusterStability: e.ClusterStabilityDisruptive,
	})

	specs, err := g.BuildExtensionTestSpecsFromOpenShiftGinkgoSuite()
	if err != nil {
		panic(fmt.Sprintf("couldn't build extension test specs from ginkgo: %+v", err.Error()))
	}

	specs.Walk(func(spec *et.ExtensionTestSpec) {
		spec.Lifecycle = et.LifecycleInforming
		// Prepend ginkgo labels (e.g. "[Serial]") to the test name so origin serializes
		// serial tests by name, removing the need for Parallelism:1 on the serial suite.
		if prefix := generatePrependedLabelsStr(spec.Labels); prefix != "" {
			spec.Name = prefix + " " + spec.Name
		}
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
