package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/onsi/gomega"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/kubernetes/test/e2e/framework"
)

// clusterConfiguration is the JSON shape of TEST_PROVIDER.
type clusterConfiguration struct {
	ProviderName string   `json:"type"`
	ProjectID    string   `json:"project_id,omitempty"`
	Region       string   `json:"region,omitempty"`
	Zone         string   `json:"zone,omitempty"`
	Zones        []string `json:"zones,omitempty"`
	NumNodes     int      `json:"num_nodes,omitempty"`
	MultiMaster  bool     `json:"multi_master,omitempty"`
	MultiZone    bool     `json:"multi_zone,omitempty"`
	ConfigFile   string   `json:"config_file,omitempty"`
}

// initializeTestFramework configures k8s e2e framework.TestContext before specs run.
func initializeTestFramework(provider string, cfg *rest.Config) error {
	if len(provider) == 0 {
		provider = `{"type":"skeleton"}`
	}
	config := &clusterConfiguration{}
	if err := json.Unmarshal([]byte(provider), config); err != nil {
		return fmt.Errorf("provider must decode into a JSON object with a 'type' key: %w", err)
	}
	if len(config.ProviderName) == 0 {
		return fmt.Errorf("provider must be a JSON object with the 'type' key")
	}

	framework.TestContext.Provider = config.ProviderName
	framework.TestContext.CloudConfig = framework.CloudConfig{
		ProjectID:   config.ProjectID,
		Region:      config.Region,
		Zone:        config.Zone,
		Zones:       config.Zones,
		NumNodes:    config.NumNodes,
		MultiMaster: config.MultiMaster,
		MultiZone:   config.MultiZone,
		ConfigFile:  config.ConfigFile,
		Provider:    framework.NullProvider{},
	}
	framework.TestContext.AllowedNotReadyNodes = 0
	framework.TestContext.MinStartupPods = -1
	framework.TestContext.MaxNodesToGather = 0
	framework.TestContext.KubeConfig = os.Getenv("KUBECONFIG")
	gomega.Expect(framework.TestContext.KubeConfig).NotTo(gomega.BeEmpty())
	framework.TestContext.DeleteNamespace = os.Getenv("DELETE_NAMESPACE") != "false"
	framework.TestContext.VerifyServiceAccount = true
	framework.TestContext.KubectlPath = "oc"
	if ad := os.Getenv("ARTIFACT_DIR"); len(strings.TrimSpace(ad)) == 0 {
		_ = os.Setenv("ARTIFACT_DIR", filepath.Join(os.TempDir(), "artifacts"))
	}
	framework.TestContext.NodeOSDistro = "custom"
	framework.TestContext.MasterOSDistro = "custom"
	gomega.Expect(cfg).NotTo(gomega.BeNil())
	framework.TestContext.Host = cfg.Host
	framework.TestContext.DumpLogsOnFailure = true
	framework.TestContext.ReportDir = os.Getenv("TEST_JUNIT_DIR")
	return nil
}

func getKubeConfig() (*rest.Config, error) {
	kubeConfig := os.Getenv("KUBECONFIG")
	if kubeConfig == "" {
		return nil, fmt.Errorf("KUBECONFIG env variable not set")
	}
	if _, err := os.Stat(kubeConfig); err != nil {
		return nil, fmt.Errorf("KUBECONFIG file %q not accessible: %w", kubeConfig, err)
	}
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeConfig},
		&clientcmd.ConfigOverrides{},
	)
	return clientConfig.ClientConfig()
}
