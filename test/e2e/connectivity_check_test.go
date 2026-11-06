//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/onsi/gomega"
	controlplaneclient "github.com/openshift/client-go/operatorcontrolplane/clientset/versioned"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	kubeAPIServerNamespace = "openshift-kube-apiserver"
	diagnosticsNamespace   = "openshift-network-diagnostics"
	kubeAPIServerCheckName = "-to-kubernetes-apiserver-endpoint-"
	kubeAPIServerHTTPSPort = 6443
)

// TestKubeAPIServerConnectivityChecksUseHTTPS verifies the live controller's endpoint targets.
func TestKubeAPIServerConnectivityChecksUseHTTPS(t *testing.T) {
	g := gomega.NewWithT(t)
	ctx := context.Background()
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{},
	)
	config, err := clientConfig.ClientConfig()
	g.Expect(err).NotTo(gomega.HaveOccurred(), "load the current kubeconfig")
	config.Timeout = 30 * time.Second

	kubeClient, err := kubernetes.NewForConfig(config)
	g.Expect(err).NotTo(gomega.HaveOccurred(), "create the Kubernetes client")
	controlplaneClient, err := controlplaneclient.NewForConfig(config)
	g.Expect(err).NotTo(gomega.HaveOccurred(), "create the control-plane client")

	endpoints, err := kubeClient.CoreV1().Endpoints(kubeAPIServerNamespace).Get(ctx, "apiserver", metav1.GetOptions{}) //nolint:staticcheck // CNO still watches core/v1 Endpoints.
	if err != nil {
		t.Fatalf("failed to get kube-apiserver Endpoints (error type %T)", err)
	}
	expectedTargets := kubeAPIServerTargets(endpoints)
	g.Expect(expectedTargets).NotTo(gomega.BeEmpty(), "the kube-apiserver Endpoints must contain a TCP 6443 address")

	g.Eventually(func(g gomega.Gomega) {
		checks, checksErr := controlplaneClient.ControlplaneV1alpha1().PodNetworkConnectivityChecks(diagnosticsNamespace).List(ctx, metav1.ListOptions{})
		if checksErr != nil {
			message := fmt.Sprintf("failed to list generated connectivity checks (error type %T)", checksErr)
			g.Expect(checksErr == nil).To(gomega.BeTrue(), message)
			return
		}

		actualTargets := make(map[string]struct{})
		allTargetsHaveValidAddress := true
		for _, check := range checks.Items {
			if !strings.Contains(check.Name, kubeAPIServerCheckName) {
				continue
			}

			_, port, splitErr := net.SplitHostPort(check.Spec.TargetEndpoint)
			if splitErr != nil {
				allTargetsHaveValidAddress = false
				continue
			}
			g.Expect(port).To(gomega.Equal(strconv.Itoa(kubeAPIServerHTTPSPort)), "generated kube-apiserver endpoint checks must use HTTPS")
			actualTargets[check.Spec.TargetEndpoint] = struct{}{}
		}
		g.Expect(allTargetsHaveValidAddress).To(gomega.BeTrue(), "generated kube-apiserver endpoint targets must be host:port")

		matchesExpectedTargets := len(actualTargets) == len(expectedTargets)
		for target := range expectedTargets {
			if _, ok := actualTargets[target]; !ok {
				matchesExpectedTargets = false
				break
			}
		}
		g.Expect(matchesExpectedTargets).To(gomega.BeTrue(), "checks must match the kube-apiserver HTTPS targets")
	}).WithTimeout(3 * time.Minute).WithPolling(5 * time.Second).Should(gomega.Succeed())
}

// kubeAPIServerTargets returns the TCP 6443 targets published by the kube-apiserver Endpoints.
func kubeAPIServerTargets(endpoints *corev1.Endpoints) map[string]struct{} { //nolint:staticcheck // CNO still watches core/v1 Endpoints.
	targets := make(map[string]struct{})
	for _, subset := range endpoints.Subsets {
		for _, address := range subset.Addresses {
			for _, port := range subset.Ports {
				if port.Port != kubeAPIServerHTTPSPort || (port.Protocol != "" && port.Protocol != corev1.ProtocolTCP) {
					continue
				}
				targets[net.JoinHostPort(address.IP, strconv.Itoa(int(port.Port)))] = struct{}{}
			}
		}
	}
	return targets
}
