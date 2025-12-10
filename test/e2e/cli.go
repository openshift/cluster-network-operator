package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	o "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	e2e "k8s.io/kubernetes/test/e2e/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

// CLI provides a simple wrapper around the oc CLI tool
type CLI struct {
	execPath           string
	kubeconfig         string
	namespace          string
	withoutNamespace   bool
	asAdmin            bool
	verb               string
	args               []string
	kubeFramework      *e2e.Framework
	namespacesToDelete []string
}

// NewCLIWithPodSecurityLevel creates a new CLI instance with namespace management.
// Note: This does NOT create a namespace automatically. Call SetupNamespace() in a
// BeforeEach hook and TeardownNamespace() in an AfterEach hook in your test.
func NewCLIWithPodSecurityLevel(baseName string, level admissionapi.Level) *CLI {
	cli := &CLI{
		execPath:   "oc",
		kubeconfig: os.Getenv("KUBECONFIG"),
		kubeFramework: &e2e.Framework{
			BaseName:                  baseName,
			SkipNamespaceCreation:     false,
			NamespacePodSecurityLevel: level,
			Options: e2e.Options{
				ClientQPS:   20,
				ClientBurst: 50,
			},
			Timeouts: e2e.NewTimeoutContext(),
		},
	}

	// Initialize the ClientSet in the framework
	config, err := cli.getConfig()
	if err != nil {
		e2e.Failf("Failed to get kubeconfig: %v", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		e2e.Failf("Failed to create Kubernetes clientset: %v", err)
	}
	cli.kubeFramework.ClientSet = clientset

	return cli
}

// SetupNamespace creates a test namespace for the test.
func (c *CLI) SetupNamespace() {
	// Generate namespace name
	nsName := fmt.Sprintf("e2e-test-%s-%s", c.kubeFramework.BaseName, getRandomString())

	// Create namespace using oc
	_, err := c.asAdminInternal().withoutNamespaceInternal().run("create", "namespace", nsName).output()
	o.Expect(err).NotTo(o.HaveOccurred(), "Failed to create namespace")

	c.namespace = nsName
	c.namespacesToDelete = append(c.namespacesToDelete, nsName)

	// Label namespace with pod security level
	if c.kubeFramework.NamespacePodSecurityLevel != "" {
		level := string(c.kubeFramework.NamespacePodSecurityLevel)
		_, err = c.asAdminInternal().withoutNamespaceInternal().run("label", "namespace", nsName,
			fmt.Sprintf("pod-security.kubernetes.io/enforce=%s", level),
			fmt.Sprintf("pod-security.kubernetes.io/warn=%s", level),
			fmt.Sprintf("pod-security.kubernetes.io/audit=%s", level),
			"security.openshift.io/scc.podSecurityLabelSync=false",
			"--overwrite",
		).output()
		o.Expect(err).NotTo(o.HaveOccurred(), "Failed to label namespace")
	}

	// Initialize the framework's namespace object
	c.kubeFramework.Namespace = &corev1.Namespace{}
	c.kubeFramework.Namespace.Name = nsName

	e2e.Logf("Created test namespace: %s", nsName)
}

// TeardownNamespace cleans up the test namespace.
func (c *CLI) TeardownNamespace() {
	if len(c.namespacesToDelete) == 0 {
		return
	}

	// Only delete if DeleteNamespace is enabled
	if !e2e.TestContext.DeleteNamespace {
		e2e.Logf("Skipping namespace deletion (DELETE_NAMESPACE=false)")
		return
	}

	for _, ns := range c.namespacesToDelete {
		e2e.Logf("Deleting namespace: %s", ns)
		_, err := c.asAdminInternal().withoutNamespaceInternal().run("delete", "namespace", ns, "--wait=false").output()
		if err != nil {
			e2e.Logf("Warning: failed to delete namespace %s: %v", ns, err)
		}
	}
}

// Namespace returns the current namespace
func (c *CLI) Namespace() string {
	return c.namespace
}

// KubeFramework returns the Kubernetes framework
func (c *CLI) KubeFramework() *e2e.Framework {
	return c.kubeFramework
}

// AsAdmin returns a copy of the CLI configured to run as admin
func (c *CLI) AsAdmin() *CLI {
	return c.asAdminInternal()
}

// asAdminInternal is the internal implementation
func (c *CLI) asAdminInternal() *CLI {
	nc := *c
	nc.asAdmin = true
	// Deep copy the slice to avoid shared references
	nc.namespacesToDelete = append([]string(nil), c.namespacesToDelete...)
	return &nc
}

// WithoutNamespace returns a copy of the CLI that doesn't include namespace flag
func (c *CLI) WithoutNamespace() *CLI {
	return c.withoutNamespaceInternal()
}

// withoutNamespaceInternal is the internal implementation
func (c *CLI) withoutNamespaceInternal() *CLI {
	nc := *c
	nc.withoutNamespace = true
	// Deep copy the slice to avoid shared references
	nc.namespacesToDelete = append([]string(nil), c.namespacesToDelete...)
	return &nc
}

// Run prepares the CLI to execute a command
func (c *CLI) Run(verb string) *CLI {
	nc := *c
	nc.verb = verb
	nc.args = []string{}
	// Deep copy the slice to avoid shared references
	nc.namespacesToDelete = append([]string(nil), c.namespacesToDelete...)
	return &nc
}

// Args adds arguments to the command
func (c *CLI) Args(args ...string) *CLI {
	nc := *c
	// Deep copy args slice before appending to avoid shared references
	nc.args = append([]string(nil), c.args...)
	nc.args = append(nc.args, args...)
	// Deep copy the slice to avoid shared references
	nc.namespacesToDelete = append([]string(nil), c.namespacesToDelete...)
	return &nc
}

// Output executes the command and returns the output
func (c *CLI) Output() (string, error) {
	return c.output()
}

// Execute executes the command and prints output to Ginkgo
func (c *CLI) Execute() error {
	out, err := c.output()
	if err != nil {
		e2e.Logf("Command failed with output:\n%s", out)
	}
	return err
}

// run is an internal method to prepare a command
func (c *CLI) run(verb string, args ...string) *CLI {
	nc := *c
	nc.verb = verb
	nc.args = args
	// Deep copy the slice to avoid shared references
	nc.namespacesToDelete = append([]string(nil), c.namespacesToDelete...)
	return &nc
}

// output is the internal method that actually executes the command
func (c *CLI) output() (string, error) {
	var cmdArgs []string

	// Add kubeconfig if specified
	if c.kubeconfig != "" {
		cmdArgs = append(cmdArgs, fmt.Sprintf("--kubeconfig=%s", c.kubeconfig))
	}

	// Add namespace if not WithoutNamespace and namespace is set
	if !c.withoutNamespace && c.namespace != "" {
		cmdArgs = append(cmdArgs, fmt.Sprintf("--namespace=%s", c.namespace))
	}

	// Add verb and args
	if c.verb != "" {
		cmdArgs = append(cmdArgs, c.verb)
	}
	cmdArgs = append(cmdArgs, c.args...)

	// Log the command
	e2e.Logf("Running: %s %s", c.execPath, strings.Join(cmdArgs, " "))

	// Execute the command
	cmd := exec.Command(c.execPath, cmdArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	outStr := strings.TrimSpace(stdout.String())
	errStr := strings.TrimSpace(stderr.String())

	if err != nil {
		return outStr, fmt.Errorf("command failed: %w\nstdout: %s\nstderr: %s", err, outStr, errStr)
	}

	return outStr, nil
}

// getConfig returns the rest config
func (c *CLI) getConfig() (*rest.Config, error) {
	kubeconfig := c.kubeconfig
	if kubeconfig == "" {
		kubeconfig = os.Getenv("KUBECONFIG")
	}
	if kubeconfig == "" {
		return nil, fmt.Errorf("KUBECONFIG not set")
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, err
	}

	// Set QPS and Burst
	config.QPS = 20
	config.Burst = 50

	return config, nil
}
