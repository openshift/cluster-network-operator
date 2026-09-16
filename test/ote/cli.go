package ote

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	o "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	e2e "k8s.io/kubernetes/test/e2e/framework"
	admissionapi "k8s.io/pod-security-admission/api"
)

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

func (c *CLI) SetupNamespace() {
	nsName := fmt.Sprintf("e2e-test-%s-%s", c.kubeFramework.BaseName, getRandomString())

	_, err := c.asAdminInternal().withoutNamespaceInternal().run("create", "namespace", nsName).output()
	o.Expect(err).NotTo(o.HaveOccurred(), "Failed to create namespace")

	c.namespace = nsName
	c.namespacesToDelete = append(c.namespacesToDelete, nsName)

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

	c.kubeFramework.Namespace = &corev1.Namespace{}
	c.kubeFramework.Namespace.Name = nsName

	e2e.Logf("Created test namespace: %s", nsName)
}

func (c *CLI) TeardownNamespace() {
	if len(c.namespacesToDelete) == 0 {
		return
	}

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

func (c *CLI) Namespace() string {
	return c.namespace
}

func (c *CLI) KubeFramework() *e2e.Framework {
	return c.kubeFramework
}

func (c *CLI) AsAdmin() *CLI {
	return c.asAdminInternal()
}

func (c *CLI) asAdminInternal() *CLI {
	nc := *c
	nc.asAdmin = true
	nc.namespacesToDelete = append([]string(nil), c.namespacesToDelete...)
	return &nc
}

func (c *CLI) WithoutNamespace() *CLI {
	return c.withoutNamespaceInternal()
}

func (c *CLI) withoutNamespaceInternal() *CLI {
	nc := *c
	nc.withoutNamespace = true
	nc.namespacesToDelete = append([]string(nil), c.namespacesToDelete...)
	return &nc
}

func (c *CLI) Run(verb string) *CLI {
	nc := *c
	nc.verb = verb
	nc.args = []string{}
	nc.namespacesToDelete = append([]string(nil), c.namespacesToDelete...)
	return &nc
}

func (c *CLI) Args(args ...string) *CLI {
	nc := *c
	nc.args = append([]string(nil), c.args...)
	nc.args = append(nc.args, args...)
	nc.namespacesToDelete = append([]string(nil), c.namespacesToDelete...)
	return &nc
}

func (c *CLI) Output() (string, error) {
	return c.output()
}

func (c *CLI) Execute() error {
	out, err := c.output()
	if err != nil {
		e2e.Logf("Command failed with output:\n%s", out)
	}
	return err
}

func (c *CLI) run(verb string, args ...string) *CLI {
	nc := *c
	nc.verb = verb
	nc.args = args
	nc.namespacesToDelete = append([]string(nil), c.namespacesToDelete...)
	return &nc
}

func (c *CLI) output() (string, error) {
	var cmdArgs []string

	if c.kubeconfig != "" {
		cmdArgs = append(cmdArgs, fmt.Sprintf("--kubeconfig=%s", c.kubeconfig))
	}

	if !c.withoutNamespace && c.namespace != "" {
		cmdArgs = append(cmdArgs, fmt.Sprintf("--namespace=%s", c.namespace))
	}

	if c.verb != "" {
		cmdArgs = append(cmdArgs, c.verb)
	}
	cmdArgs = append(cmdArgs, c.args...)

	e2e.Logf("Running: %s %s", c.execPath, strings.Join(cmdArgs, " "))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.execPath, cmdArgs...)
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

	config.QPS = 20
	config.Burst = 50

	return config, nil
}
