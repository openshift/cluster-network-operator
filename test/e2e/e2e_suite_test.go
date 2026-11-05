package e2e

import (
	"flag"
	"os"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"k8s.io/klog/v2"
	"k8s.io/kubernetes/test/e2e/framework"
	e2econfig "k8s.io/kubernetes/test/e2e/framework/config"
)

func handleFlags() {
	e2econfig.CopyFlags(e2econfig.Flags, flag.CommandLine)
	framework.RegisterCommonFlags(flag.CommandLine)
	framework.RegisterClusterFlags(flag.CommandLine)
	flag.Parse()
}

// TestMain configures the k8s e2e framework from -kubeconfig / KUBECONFIG for
// native `go test` / `make test-e2e`. OTE does not call TestMain; it initializes
// TestContext via AddBeforeAll in openshift-tests-ext instead.
func TestMain(m *testing.M) {
	handleFlags()
	if framework.TestContext.Provider == "" {
		framework.TestContext.Provider = "skeleton"
	}
	var err error
	framework.TestContext.CloudConfig.Provider, err = framework.SetupProviderConfig(framework.TestContext.Provider)
	if err != nil {
		klog.Errorf("Failed to setup provider config for %q: %v", framework.TestContext.Provider, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// TestE2E runs the native CNO Ginkgo suite. CI uses the OpenShift Tests Extension.
func TestE2E(t *testing.T) {
	gomega.RegisterFailHandler(framework.Fail)
	ginkgo.RunSpecs(t, "CNO E2E Suite")
}
