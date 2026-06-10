package network

import (
	"fmt"
	"net"
	"strings"
	"testing"

	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	cnoclient "github.com/openshift/cluster-network-operator/pkg/client"
	cnofake "github.com/openshift/cluster-network-operator/pkg/client/fake"
	"github.com/openshift/cluster-network-operator/pkg/hypershift"
	appsv1 "k8s.io/api/apps/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
)

const (
	tlsMinVersionArg   = "--tls-min-version"
	tlsCipherSuitesArg = "--tls-cipher-suites"
)

func TestRenderNetworkNodeIdentity(t *testing.T) {
	const (
		ovnImage          = "test-ovn-image"
		releaseVersion    = "5.0.0"
		ovnCtrlPlaneImage = "test-ovn-ctrl-plane-image"
		tokenMinterImage  = "test-token-minter-image"
		tokenAudience     = "test-token-audience"
		hostedClusterNS   = "hosted-cluster-ns"
	)

	setupTest := func(t *testing.T) (*operv1.NetworkSpec, *bootstrap.BootstrapResult, cnoclient.Client) {
		networkConfig := &operv1.NetworkSpec{
			ServiceNetwork: []string{"172.30.0.0/16"},
			ClusterNetwork: []operv1.ClusterNetworkEntry{
				{
					CIDR:       "10.128.0.0/15",
					HostPrefix: 23,
				},
			},
			DefaultNetwork: operv1.DefaultNetworkDefinition{
				Type:                operv1.NetworkTypeOVNKubernetes,
				OVNKubernetesConfig: &operv1.OVNKubernetesConfig{},
			},
		}

		bootstrapResult := fakeBootstrapResult()
		bootstrapResult.Infra.NetworkNodeIdentityEnabled = true
		bootstrapResult.TLSProfile = bootstrap.TLSProfile{
			Spec: configv1.TLSProfileSpec{
				MinTLSVersion: configv1.VersionTLS12,
				Ciphers:       []string{"TLS_AES_128_GCM_SHA256", "TLS_AES_256_GCM_SHA384"},
			},
			Adherence: configv1.TLSAdherencePolicyLegacyAdheringComponentsOnly,
		}

		// Set required environment variables
		t.Setenv("RELEASE_VERSION", releaseVersion)
		t.Setenv("OVN_IMAGE", ovnImage)

		client := cnofake.NewFakeClient()

		return networkConfig, bootstrapResult, client
	}

	assertRenderSuccess := func(t *testing.T, networkConfig *operv1.NetworkSpec, bootstrapResult *bootstrap.BootstrapResult,
		client cnoclient.Client) []*uns.Unstructured {
		g := NewWithT(t)
		objs, err := renderNetworkNodeIdentity(networkConfig, bootstrapResult, manifestDir, client)
		g.Expect(err).NotTo(HaveOccurred())

		return objs
	}

	t.Run("should successfully render the network-node-identity DaemonSet", func(t *testing.T) {
		g := NewWithT(t)
		networkConfig, bootstrapResult, client := setupTest(t)
		daemonSet := mustFindRenderedObj[*appsv1.DaemonSet](t, assertRenderSuccess(t, networkConfig, bootstrapResult, client),
			"DaemonSet", "network-node-identity")
		container := mustFindContainer(t, daemonSet.Spec.Template.Spec.Containers, "webhook")
		execStr := findOvnkubeIdentityExec(t, container.Command)
		apiserverArg := "--k8s-apiserver=https://" + net.JoinHostPort(bootstrapResult.Infra.APIServers[bootstrap.APIServerDefault].Host,
			bootstrapResult.Infra.APIServers[bootstrap.APIServerDefault].Port)

		g.Expect(execStr).To(ContainSubstring(apiserverArg))
		g.Expect(execStr).To(ContainSubstring("--webhook-host=127.0.0.1"))
		g.Expect(execStr).To(ContainSubstring("--webhook-port=" + NetworkNodeIdentityWebhookPort))
		g.Expect(container.Image).To(Equal(ovnImage))
		g.Expect(daemonSet.Annotations["release.openshift.io/version"]).To(Equal(releaseVersion))

		container = mustFindContainer(t, daemonSet.Spec.Template.Spec.Containers, "approver")
		execStr = findOvnkubeIdentityExec(t, container.Command)
		g.Expect(execStr).To(ContainSubstring(apiserverArg))
	})

	testTLSArgRendering(t, "webhook ovnkube-identity", "", "", func(t *testing.T, tlsProfile bootstrap.TLSProfile) string {
		networkConfig, bootstrapResult, client := setupTest(t)
		bootstrapResult.TLSProfile = tlsProfile
		daemonSet := mustFindRenderedObj[*appsv1.DaemonSet](t, assertRenderSuccess(t, networkConfig, bootstrapResult, client),
			"DaemonSet", "network-node-identity")
		return findOvnkubeIdentityExec(t, mustFindContainer(t, daemonSet.Spec.Template.Spec.Containers, "webhook").Command)
	})

	t.Run("HyperShift enabled", func(t *testing.T) {
		networkConfig, bootstrapResult, client := setupTest(t)

		// Set HyperShift environment variables
		t.Setenv("HYPERSHIFT", "true")
		t.Setenv("HOSTED_CLUSTER_NAME", "test-cluster")
		t.Setenv("HOSTED_CLUSTER_NAMESPACE", hostedClusterNS)
		t.Setenv("OVN_CONTROL_PLANE_IMAGE", ovnCtrlPlaneImage)
		t.Setenv("CLI_IMAGE", "quay.io/openshift/cli:latest")
		t.Setenv("TOKEN_MINTER_IMAGE", tokenMinterImage)
		t.Setenv("TOKEN_AUDIENCE", tokenAudience)

		bootstrapResult.Infra.HostedControlPlane = &hypershift.HostedControlPlane{
			ControllerAvailabilityPolicy: hypershift.HighlyAvailable,
			NodeSelector:                 map[string]string{"node-selector-key": "node-selector-value"},
			Labels:                       map[string]string{"hypershift.openshift.io/cluster": "test"},
			PriorityClass:                "hypershift-control-plane",
		}

		// Add local API server for HyperShift
		bootstrapResult.Infra.APIServers[bootstrap.APIServerDefaultLocal] = bootstrap.APIServer{
			Host: "kube-apiserver",
			Port: "6443",
		}

		t.Run("should successfully render the network-node-identity Deployment", func(t *testing.T) {
			g := NewWithT(t)
			deployment := mustFindRenderedObj[*appsv1.Deployment](t, assertRenderSuccess(t, networkConfig, bootstrapResult, client),
				"Deployment", "network-node-identity")
			container := mustFindContainer(t, deployment.Spec.Template.Spec.Containers, "webhook")
			execStr := findOvnkubeIdentityExec(t, container.Command)

			g.Expect(execStr).To(ContainSubstring("--webhook-port=" + NetworkNodeIdentityWebhookPort))
			g.Expect(container.Image).To(Equal(ovnCtrlPlaneImage))
			g.Expect(deployment.Annotations["release.openshift.io/version"]).To(Equal(releaseVersion))
			g.Expect(deployment.Labels["hypershift.openshift.io/hosted-control-plane"]).To(Equal(hostedClusterNS))
			g.Expect(ptr.Deref(deployment.Spec.Replicas, 0)).To(Equal(int32(3)))
			g.Expect(deployment.Spec.Strategy.Type).To(Equal(appsv1.RollingUpdateDeploymentStrategyType))
			g.Expect(deployment.Spec.Template.Spec.Affinity.PodAntiAffinity).NotTo(BeNil())
			g.Expect(deployment.Spec.Template.Spec.PriorityClassName).To(Equal(bootstrapResult.Infra.HostedControlPlane.PriorityClass))
			g.Expect(deployment.Spec.Template.Spec.NodeSelector).To(Equal(bootstrapResult.Infra.HostedControlPlane.NodeSelector))

			mustFindContainer(t, deployment.Spec.Template.Spec.Containers, "approver")

			container = mustFindContainer(t, deployment.Spec.Template.Spec.Containers, "token-minter")
			g.Expect(container.Image).To(Equal(tokenMinterImage))
			expectedArg := "--token-audience=" + tokenAudience
			g.Expect(container.Args).To(ContainElement(expectedArg))
		})

		testTLSArgRendering(t, "webhook ovnkube-identity", "", "", func(t *testing.T, tlsProfile bootstrap.TLSProfile) string {
			bootstrapResult.TLSProfile = tlsProfile
			deployment := mustFindRenderedObj[*appsv1.Deployment](t, assertRenderSuccess(t, networkConfig, bootstrapResult, client),
				"Deployment", "network-node-identity")
			return findOvnkubeIdentityExec(t, mustFindContainer(t, deployment.Spec.Template.Spec.Containers, "webhook").Command)
		})
	})

	t.Run("NetworkNodeIdentity is disabled", func(t *testing.T) {
		g := NewWithT(t)
		networkConfig, bootstrapResult, client := setupTest(t)
		bootstrapResult.Infra.NetworkNodeIdentityEnabled = false

		objs := assertRenderSuccess(t, networkConfig, bootstrapResult, client)
		g.Expect(objs).To(BeEmpty())
	})
}

func testTLSArgRendering(t *testing.T, name string, defaultMinVersion string, defaultCiphers string, getCommandStr func(*testing.T, bootstrap.TLSProfile) string) {
	t.Run("when TLS profile adherence is LegacyAdheringComponentsOnly", func(t *testing.T) {
		t.Run(fmt.Sprintf("should render the %s command with default TLS CLI args", name), func(t *testing.T) {
			g := NewWithT(t)
			tlsProfile := bootstrap.TLSProfile{
				Spec: configv1.TLSProfileSpec{
					MinTLSVersion: configv1.VersionTLS12,
					Ciphers:       []string{"TLS_AES_128_GCM_SHA256", "TLS_AES_256_GCM_SHA384"},
				},
				Adherence: configv1.TLSAdherencePolicyLegacyAdheringComponentsOnly,
			}

			commandStr := getCommandStr(t, tlsProfile)
			if defaultMinVersion != "" {
				g.Expect(commandStr).To(ContainSubstring(tlsMinVersionArg + "=" + defaultMinVersion))
			} else {
				g.Expect(commandStr).NotTo(ContainSubstring(tlsMinVersionArg))
			}
			if defaultCiphers != "" {
				g.Expect(commandStr).To(ContainSubstring(tlsCipherSuitesArg + "=" + defaultCiphers))
			} else {
				g.Expect(commandStr).NotTo(ContainSubstring(tlsCipherSuitesArg))
			}
		})
	})

	t.Run("when TLS profile adherence is StrictAllComponents", func(t *testing.T) {
		t.Run(fmt.Sprintf("should render the %s command with the TLS CLI args", name), func(t *testing.T) {
			g := NewWithT(t)
			tlsProfile := bootstrap.TLSProfile{
				Spec: configv1.TLSProfileSpec{
					MinTLSVersion: configv1.VersionTLS12,
					Ciphers:       []string{"TLS_AES_128_GCM_SHA256", "TLS_AES_256_GCM_SHA384"},
				},
				Adherence: configv1.TLSAdherencePolicyStrictAllComponents,
			}

			commandStr := getCommandStr(t, tlsProfile)
			expectedMinVersion := tlsMinVersionArg + "=" + string(tlsProfile.Spec.MinTLSVersion)
			expectedCiphers := tlsCipherSuitesArg + "=" + strings.Join(tlsProfile.Spec.Ciphers, ",")
			g.Expect(commandStr).To(ContainSubstring(expectedMinVersion))
			g.Expect(commandStr).To(ContainSubstring(expectedCiphers))
		})

		t.Run("with empty cipher list", func(t *testing.T) {
			t.Run(fmt.Sprintf("should not render the --tls-cipher-suites arg for the %s command", name), func(t *testing.T) {
				g := NewWithT(t)
				tlsProfile := bootstrap.TLSProfile{
					Spec: configv1.TLSProfileSpec{
						MinTLSVersion: configv1.VersionTLS13,
						Ciphers:       nil,
					},
					Adherence: configv1.TLSAdherencePolicyStrictAllComponents,
				}

				commandStr := getCommandStr(t, tlsProfile)
				expectedMinVersion := tlsMinVersionArg + "=" + string(tlsProfile.Spec.MinTLSVersion)
				g.Expect(commandStr).To(ContainSubstring(expectedMinVersion))
				g.Expect(commandStr).NotTo(ContainSubstring(tlsCipherSuitesArg))
			})
		})
	})
}

func findOvnkubeIdentityExec(t *testing.T, cmdArgs []string) string {
	return findExecCommand(t, cmdArgs, "ovnkube-identity")
}
