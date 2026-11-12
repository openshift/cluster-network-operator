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
	"github.com/openshift/cluster-network-operator/pkg/names"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
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
		objs, err := renderNetworkNodeIdentity(t.Context(), networkConfig, bootstrapResult, manifestDir, client)
		g.Expect(err).NotTo(HaveOccurred())

		return objs
	}

	t.Run("should successfully render the network-node-identity DaemonSet", func(t *testing.T) {
		g := NewWithT(t)
		networkConfig, bootstrapResult, client := setupTest(t)
		objs := assertRenderSuccess(t, networkConfig, bootstrapResult, client)
		daemonSet := mustFindRenderedObj[*appsv1.DaemonSet](t, objs,
			"DaemonSet", "network-node-identity")
		expectReadOnlyRootFilesystems(t, objs, NetworkNodeIdentityNamespace)
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
			objs := assertRenderSuccess(t, networkConfig, bootstrapResult, client)
			deployment := mustFindRenderedObj[*appsv1.Deployment](t, objs,
				"Deployment", "network-node-identity-v2")
			expectReadOnlyRootFilesystems(t, objs, hostedClusterNS)
			expectNetworkNodeIdentitySecurityContexts(t, deployment.Spec.Template.Spec)
			expectNetworkNodeIdentityResources(t, deployment.Spec.Template.Spec)
			expectNetworkNodeIdentityProbes(t, deployment.Spec.Template.Spec)
			container := mustFindContainer(t, deployment.Spec.Template.Spec.Containers, "webhook")
			execStr := findOvnkubeIdentityExec(t, container.Command)

			g.Expect(execStr).To(ContainSubstring("--webhook-port=" + NetworkNodeIdentityWebhookPort))
			g.Expect(container.Image).To(Equal(ovnCtrlPlaneImage))
			g.Expect(deployment.Annotations["release.openshift.io/version"]).To(Equal(releaseVersion))
			g.Expect(deployment.Labels["hypershift.openshift.io/hosted-control-plane"]).To(Equal(hostedClusterNS))
			g.Expect(ptr.Deref(deployment.Spec.Replicas, 0)).To(Equal(int32(3)))
			g.Expect(deployment.Spec.Strategy.Type).To(Equal(appsv1.RollingUpdateDeploymentStrategyType))
			podAntiAffinity := deployment.Spec.Template.Spec.Affinity.PodAntiAffinity
			g.Expect(podAntiAffinity).NotTo(BeNil())
			g.Expect(podAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution).To(ConsistOf(
				corev1.PodAffinityTerm{
					LabelSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "network-node-identity-v2"},
					},
					TopologyKey: corev1.LabelHostname,
				},
			))
			g.Expect(podAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution).To(ConsistOf(
				corev1.WeightedPodAffinityTerm{
					Weight: 100,
					PodAffinityTerm: corev1.PodAffinityTerm{
						LabelSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"app": "network-node-identity-v2"},
						},
						TopologyKey: corev1.LabelTopologyZone,
					},
				},
			))
			g.Expect(deployment.Spec.Template.Spec.PriorityClassName).To(Equal(bootstrapResult.Infra.HostedControlPlane.PriorityClass))
			g.Expect(deployment.Spec.Template.Spec.NodeSelector).To(Equal(bootstrapResult.Infra.HostedControlPlane.NodeSelector))

			mustFindContainer(t, deployment.Spec.Template.Spec.Containers, "approver")

			container = mustFindContainer(t, deployment.Spec.Template.Spec.Containers, "token-minter")
			g.Expect(container.Image).To(Equal(tokenMinterImage))
			expectedArg := "--token-audience=" + tokenAudience
			g.Expect(container.Args).To(ContainElement(expectedArg))

			service := mustFindRenderedObj[*corev1.Service](t, objs, "Service", "network-node-identity")
			g.Expect(service.Spec.Selector).To(Equal(map[string]string{
				"hypershift.openshift.io/control-plane-component": "network-node-identity",
			}))
		})

		testTLSArgRendering(t, "webhook ovnkube-identity", "", "", func(t *testing.T, tlsProfile bootstrap.TLSProfile) string {
			bootstrapResult.TLSProfile = tlsProfile
			deployment := mustFindRenderedObj[*appsv1.Deployment](t, assertRenderSuccess(t, networkConfig, bootstrapResult, client),
				"Deployment", "network-node-identity-v2")
			return findOvnkubeIdentityExec(t, mustFindContainer(t, deployment.Spec.Template.Spec.Containers, "webhook").Command)
		})

		t.Run("should preserve the legacy Deployment while the v2 Deployment is not ready", func(t *testing.T) {
			g := NewWithT(t)
			seedManagedDeployment(t, client, hostedClusterNS, "network-node-identity", true)
			seedManagedDeployment(t, client, hostedClusterNS, "network-node-identity-v2", false)

			objs := assertRenderSuccess(t, networkConfig, bootstrapResult, client)
			legacyDeployment := mustFindRenderedObj[*appsv1.Deployment](t, objs, "Deployment", "network-node-identity")
			_, createOnly := legacyDeployment.Annotations[names.CreateOnlyAnnotation]
			g.Expect(createOnly).To(BeTrue())
			mustFindRenderedObj[*appsv1.Deployment](t, objs, "Deployment", "network-node-identity-v2")
		})

		t.Run("should preserve the legacy Deployment when v2 reports zero replicas", func(t *testing.T) {
			g := NewWithT(t)
			seedManagedDeployment(t, client, hostedClusterNS, "network-node-identity", true)
			seedManagedDeploymentWithStatus(t, client, hostedClusterNS, "network-node-identity-v2", 3,
				appsv1.DeploymentStatus{ObservedGeneration: 1})

			objs := assertRenderSuccess(t, networkConfig, bootstrapResult, client)
			legacyDeployment := mustFindRenderedObj[*appsv1.Deployment](t, objs, "Deployment", "network-node-identity")
			_, createOnly := legacyDeployment.Annotations[names.CreateOnlyAnnotation]
			g.Expect(createOnly).To(BeTrue())
			mustFindRenderedObj[*appsv1.Deployment](t, objs, "Deployment", "network-node-identity-v2")
		})

		t.Run("should remove the legacy Deployment after the v2 Deployment is ready", func(t *testing.T) {
			g := NewWithT(t)
			seedManagedDeployment(t, client, hostedClusterNS, "network-node-identity-v2", true)

			objs := assertRenderSuccess(t, networkConfig, bootstrapResult, client)
			deploymentNames := make([]string, 0)
			for _, obj := range objs {
				if obj.GetKind() == "Deployment" {
					deploymentNames = append(deploymentNames, obj.GetName())
				}
			}
			g.Expect(deploymentNames).To(ConsistOf("network-node-identity-v2"))
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

func seedManagedDeployment(t *testing.T, client cnoclient.Client, namespace, name string, ready bool) {
	t.Helper()

	replicas := int32(3)
	status := appsv1.DeploymentStatus{
		Replicas:           3,
		UpdatedReplicas:    3,
		ReadyReplicas:      3,
		AvailableReplicas:  3,
		ObservedGeneration: 1,
	}
	if !ready {
		status.UpdatedReplicas = 2
		status.ReadyReplicas = 2
		status.AvailableReplicas = 2
	}
	seedManagedDeploymentWithStatus(t, client, namespace, name, replicas, status)
}

func seedManagedDeploymentWithStatus(t *testing.T, client cnoclient.Client, namespace, name string, replicas int32, status appsv1.DeploymentStatus) {
	t.Helper()

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			Generation: 1,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
		},
		Status: status,
	}
	crClient := client.ClientFor(names.ManagementClusterName).CRClient()
	err := crClient.Get(t.Context(), types.NamespacedName{Namespace: namespace, Name: name}, deployment)
	if err == nil {
		deployment.Generation = 1
		deployment.Status = status
		if err := crClient.Status().Update(t.Context(), deployment); err != nil {
			t.Fatalf("failed to update Deployment %s: %v", name, err)
		}
		return
	}
	if !apierrors.IsNotFound(err) {
		t.Fatalf("failed to get Deployment %s: %v", name, err)
	}
	if err := crClient.Create(t.Context(), deployment); err != nil {
		t.Fatalf("failed to create Deployment %s: %v", name, err)
	}
}

func expectNetworkNodeIdentitySecurityContexts(t *testing.T, podSpec corev1.PodSpec) {
	t.Helper()

	g := NewWithT(t)
	g.Expect(podSpec.SecurityContext).NotTo(BeNil())
	g.Expect(podSpec.SecurityContext.RunAsNonRoot).NotTo(BeNil())
	g.Expect(*podSpec.SecurityContext.RunAsNonRoot).To(BeTrue())

	containers := make([]corev1.Container, 0, len(podSpec.InitContainers)+len(podSpec.Containers))
	containers = append(containers, podSpec.InitContainers...)
	containers = append(containers, podSpec.Containers...)
	g.Expect(containers).To(HaveLen(4))

	for _, container := range containers {
		securityContext := container.SecurityContext
		g.Expect(securityContext).NotTo(BeNil())
		g.Expect(securityContext.AllowPrivilegeEscalation).NotTo(BeNil())
		g.Expect(*securityContext.AllowPrivilegeEscalation).To(BeFalse())
		g.Expect(securityContext.Capabilities).NotTo(BeNil())
		g.Expect(securityContext.Capabilities.Drop).To(ConsistOf(corev1.Capability("ALL")))
	}
}

func expectNetworkNodeIdentityResources(t *testing.T, podSpec corev1.PodSpec) {
	t.Helper()

	g := NewWithT(t)
	containers := make([]corev1.Container, 0, len(podSpec.InitContainers)+len(podSpec.Containers))
	containers = append(containers, podSpec.InitContainers...)
	containers = append(containers, podSpec.Containers...)
	g.Expect(containers).To(HaveLen(4))

	for _, container := range containers {
		resources := container.Resources
		g.Expect(resources.Requests).To(HaveKey(corev1.ResourceCPU))
		g.Expect(resources.Requests).To(HaveKey(corev1.ResourceMemory))
		g.Expect(resources.Limits.Cpu().String()).To(Equal("100m"))
		g.Expect(resources.Limits.Memory().String()).To(Equal("100Mi"))
	}
}

func expectNetworkNodeIdentityProbes(t *testing.T, podSpec corev1.PodSpec) {
	t.Helper()

	g := NewWithT(t)
	approver := mustFindContainer(t, podSpec.Containers, "approver")
	tokenMinter := mustFindContainer(t, podSpec.Containers, "token-minter")

	approverCommand := strings.Join(approver.Command, " ")
	g.Expect(approverCommand).To(ContainSubstring("--metrics-address=:" + NetworkNodeIdentityApproverMetricsPort))
	g.Expect(approver.Ports).To(ContainElement(corev1.ContainerPort{
		Name:          "metrics",
		ContainerPort: 9402,
		Protocol:      corev1.ProtocolTCP,
	}))
	g.Expect(approver.LivenessProbe).NotTo(BeNil())
	g.Expect(approver.LivenessProbe.HTTPGet).NotTo(BeNil())
	g.Expect(approver.LivenessProbe.HTTPGet.Path).To(Equal("/metrics"))
	g.Expect(approver.LivenessProbe.HTTPGet.Port.String()).To(Equal("metrics"))
	g.Expect(approver.ReadinessProbe).NotTo(BeNil())
	g.Expect(approver.ReadinessProbe.HTTPGet).NotTo(BeNil())
	g.Expect(approver.ReadinessProbe.HTTPGet.Path).To(Equal("/metrics"))
	g.Expect(approver.ReadinessProbe.HTTPGet.Port.String()).To(Equal("metrics"))

	g.Expect(tokenMinter.LivenessProbe).NotTo(BeNil())
	g.Expect(tokenMinter.LivenessProbe.Exec).NotTo(BeNil())
	g.Expect(tokenMinter.LivenessProbe.Exec.Command).To(ContainElement(
		`[ -s /var/run/secrets/hosted_cluster/token ] && [ $(( $(date +%s) - $(stat -c %Y /var/run/secrets/hosted_cluster/token) )) -lt 4200 ]`,
	))
	g.Expect(tokenMinter.ReadinessProbe).NotTo(BeNil())
	g.Expect(tokenMinter.ReadinessProbe.Exec).NotTo(BeNil())
	g.Expect(tokenMinter.ReadinessProbe.Exec.Command).To(ContainElement("test -s /var/run/secrets/hosted_cluster/token"))
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
