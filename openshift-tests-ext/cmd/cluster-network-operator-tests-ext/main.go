package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/openshift-eng/openshift-tests-extension/pkg/cmd"
	"github.com/openshift-eng/openshift-tests-extension/pkg/util/sets"

	"github.com/spf13/cobra"

	e "github.com/openshift-eng/openshift-tests-extension/pkg/extension"
	et "github.com/openshift-eng/openshift-tests-extension/pkg/extension/extensiontests"
	g "github.com/openshift-eng/openshift-tests-extension/pkg/ginkgo"

	exttest "github.com/openshift/cluster-network-operator/openshift-tests-ext/test"
	// Blank-import native CNO e2e specs so they are discovered by the extension.
	_ "github.com/openshift/cluster-network-operator/test/e2e"
)

const (
	suiteSerial     = "cluster-network-operator/conformance/serial"
	suiteParallel   = "cluster-network-operator/conformance/parallel"
	suiteDisruptive = "cluster-network-operator/conformance/disruptive"

	// extensionLabel is prepended to every spec name for suite identification.
	extensionLabel = "cluster-network-operator-ext"

	// discoverAllEnv, when set, includes every discovered Ginkgo spec as
	// LifecycleInforming so update-test-lists / validation can see unlisted tests.
	discoverAllEnv = "CNO_OTE_DISCOVER_ALL"
)

func shouldIncludeTest(spec *et.ExtensionTestSpec) bool {
	// Disable specs that are not explicitly assigned a lifecycle (opt-in lists).
	if spec.Lifecycle == "" {
		return false
	}
	if strings.Contains(spec.Name, "[Disabled:") {
		return false
	}
	return true
}

func suiteForSpec(spec *et.ExtensionTestSpec) string {
	switch {
	case spec.Labels.Has("Disruptive"):
		return suiteDisruptive
	case spec.Labels.Has("Serial"):
		return suiteSerial
	default:
		return suiteParallel
	}
}

// withExtensionLabel inserts [cluster-network-operator-ext] after [sig-network]
// when present so names stay `[sig-network][cluster-network-operator-ext] ...`.
func withExtensionLabel(name string) string {
	token := "[" + extensionLabel + "]"
	if strings.Contains(name, token) {
		return name
	}
	const sigNetwork = "[sig-network]"
	if idx := strings.Index(name, sigNetwork); idx >= 0 {
		insertAt := idx + len(sigNetwork)
		return name[:insertAt] + token + name[insertAt:]
	}
	return token + name
}

func main() {
	registry := e.NewRegistry()

	ext := e.NewExtension("openshift", "payload", "cluster-network-operator")

	ext.AddSuite(e.Suite{
		Name:        suiteSerial,
		Parallelism: 1,
		Qualifiers:  []string{fmt.Sprintf(`name.contains("[Suite:%s]")`, suiteSerial)},
	})

	ext.AddSuite(e.Suite{
		Name:       suiteParallel,
		Qualifiers: []string{fmt.Sprintf(`name.contains("[Suite:%s]")`, suiteParallel)},
	})

	ext.AddSuite(e.Suite{
		Name:             suiteDisruptive,
		Parallelism:      1,
		ClusterStability: e.ClusterStabilityDisruptive,
		Qualifiers:       []string{fmt.Sprintf(`name.contains("[Suite:%s]")`, suiteDisruptive)},
	})

	// Include specs from replaced/vendored modules such as test/e2e.
	specs, err := g.BuildExtensionTestSpecsFromOpenShiftGinkgoSuite(et.AllTestsIncludingVendored())
	if err != nil {
		panic(fmt.Sprintf("couldn't build extension test specs from ginkgo: %+v", err.Error()))
	}

	// Initialize framework.TestContext when KUBECONFIG is present.
	// Listing/info without a kubeconfig leaves cfgErr set;
	// AddBeforeAll panics only when a test actually runs.
	cfg, cfgErr := getKubeConfig()
	// Only initialize framework.TestContext here. Specs obtain clients via
	// framework.NewDefaultFramework (BeforeEach).
	specs.AddBeforeAll(func() {
		if cfgErr != nil {
			panic(cfgErr)
		}
		if err := initializeTestFramework(os.Getenv("TEST_PROVIDER"), cfg); err != nil {
			panic(err)
		}
	})

	informingTests := sets.New(exttest.InformingTests...)
	blockingTests := sets.New(exttest.BlockingTests...)
	discoverAll := os.Getenv(discoverAllEnv) != ""

	// Tag specs with the extension label, append [Suite:...] for suite qualifiers,
	// then assign lifecycle from InformingTests / BlockingTests (opt-in lists).
	specs.Walk(func(spec *et.ExtensionTestSpec) {
		spec.Labels.Insert(extensionLabel)
		spec.Name = withExtensionLabel(spec.Name)

		suite := suiteForSpec(spec)
		if !strings.Contains(spec.Name, "[Suite:") {
			spec.Name = spec.Name + " [Suite:" + suite + "]"
		}
		switch {
		case informingTests.Has(spec.Name):
			spec.Lifecycle = et.LifecycleInforming
		case blockingTests.Has(spec.Name):
			spec.Lifecycle = et.LifecycleBlocking
		default:
			if discoverAll {
				spec.Lifecycle = et.LifecycleInforming
			} else {
				spec.Lifecycle = ""
			}
		}
	})
	specs = specs.Select(shouldIncludeTest)

	ext.AddSpecs(specs)
	registry.Register(ext)

	root := &cobra.Command{
		Long: "OpenShift Tests Extension for Cluster Network Operator",
	}
	root.AddCommand(cmd.DefaultExtensionCommands(registry)...)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
