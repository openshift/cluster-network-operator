package network

import (
	"slices"
	"strings"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"github.com/openshift/cluster-network-operator/pkg/hypershift"
	"github.com/openshift/cluster-network-operator/pkg/render"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestCloudNetworkConfigControllerOpenStackMaxAllowedAddressPairs(t *testing.T) {
	for _, managed := range []bool{false, true} {
		mode := "self-hosted"
		if managed {
			mode = "managed"
		}
		t.Run(mode, func(t *testing.T) {
			for _, tc := range []struct {
				name  string
				limit int32
				want  string
			}{
				{name: "unset"},
				{name: "minimum", limit: 1, want: "-platform-os-max-allowed-address-pairs=1"},
				{name: "configured", limit: 42, want: "-platform-os-max-allowed-address-pairs=42"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					t.Setenv("HYPERSHIFT", "false")
					if managed {
						t.Setenv("HYPERSHIFT", "true")
					}
					t.Setenv("HOSTED_CLUSTER_NAMESPACE", "test-ns")
					t.Setenv("CLOUD_NETWORK_CONFIG_CONTROLLER_IMAGE", "test-image")
					t.Setenv("GCP_CNCC_CREDENTIALS_FILE", "")
					conf := &operv1.NetworkSpec{DefaultNetwork: operv1.DefaultNetworkDefinition{Type: operv1.NetworkTypeOVNKubernetes}}
					boot := &bootstrap.BootstrapResult{Infra: bootstrap.InfraStatus{
						PlatformType:                    configv1.OpenStackPlatformType,
						OpenStackMaxAllowedAddressPairs: tc.limit,
						HostedControlPlane:              &hypershift.HostedControlPlane{},
						APIServers: map[string]bootstrap.APIServer{
							bootstrap.APIServerDefaultLocal: {Host: "localhost", Port: "6443"},
						},
					}}
					objs, err := renderCloudNetworkConfigController(conf, boot, "../../bindata")
					if err != nil {
						t.Fatalf("failed to render CNCC: %v", err)
					}
					for _, obj := range objs {
						if obj.GetKind() != "Deployment" {
							continue
						}
						container, found := findUnstructuredContainer(t, obj.Object, "controller")
						if !found {
							t.Fatal("controller container not found in Deployment")
						}
						field := "args"
						if managed {
							field = "command"
						}
						args, found, err := uns.NestedStringSlice(container, field)
						if err != nil || !found {
							t.Fatalf("controller %s not found: %v", field, err)
						}
						var flags []string
						for _, arg := range args {
							for field := range strings.FieldsSeq(arg) {
								if strings.HasPrefix(field, "-platform-os-max-allowed-address-pairs") {
									flags = append(flags, field)
								}
							}
						}
						if tc.want == "" {
							if len(flags) != 0 {
								t.Errorf("unset limit should omit flag, got %v", flags)
							}
						} else if len(flags) != 1 || flags[0] != tc.want {
							t.Errorf("expected exactly one %q flag, got %v", tc.want, flags)
						}
						return
					}
					t.Fatal("Deployment not found in rendered output")
				})
			}
		})
	}
}

func makeManagedControllerRenderData() render.RenderData {
	data := render.MakeRenderData()
	data.Data["ReleaseVersion"] = "4.18.0"
	data.Data["PlatformType"] = "GCP"
	data.Data["PlatformRegion"] = "us-central1"
	data.Data["PlatformTypeAWS"] = "AWS"
	data.Data["PlatformTypeAzure"] = "Azure"
	data.Data["PlatformTypeGCP"] = "GCP"
	data.Data["CloudNetworkConfigControllerImage"] = "test-image"
	data.Data["KubernetesServiceURL"] = "https://localhost:6443"
	data.Data["ExternalControlPlane"] = true
	data.Data["PlatformAzureEnvironment"] = ""
	data.Data["PlatformAWSCAPath"] = ""
	data.Data["OpenStackMaxAllowedAddressPairs"] = int32(0)
	data.Data["PlatformAPIURL"] = ""
	data.Data["CLIImage"] = "cli-image"
	data.Data["TokenMinterImage"] = "token-minter-image"
	data.Data["TokenAudience"] = "https://issuer.example.com"
	data.Data["ManagementClusterName"] = "test-cluster"
	data.Data["HostedClusterNamespace"] = "test-ns"
	data.Data["ReleaseImage"] = "release-image"
	data.Data["HCPNodeSelector"] = map[string]string{}
	data.Data["HCPLabels"] = map[string]string{}
	data.Data["HCPTolerations"] = []string{}
	data.Data["RunAsUser"] = ""
	data.Data["PriorityClass"] = ""
	data.Data["HTTP_PROXY"] = ""
	data.Data["HTTPS_PROXY"] = ""
	data.Data["NO_PROXY"] = ""
	data.Data["AzureManagedCertDirectory"] = ""
	data.Data["AzureManagedCredsPath"] = ""
	data.Data["AzureManagedSecretProviderClass"] = ""
	data.Data["GCPCredentialsPath"] = ""
	return data
}

// getEnvVar looks up an env var by name from a container map and returns its value.
func getEnvVar(t *testing.T, container map[string]any, name string) (string, bool) {
	t.Helper()
	envSlice, found, err := uns.NestedSlice(container, "env")
	if err != nil || !found {
		return "", false
	}
	for _, e := range envSlice {
		em := e.(map[string]any)
		n, _, _ := uns.NestedString(em, "name")
		if n == name {
			v, _, _ := uns.NestedString(em, "value")
			return v, true
		}
	}
	return "", false
}

// findUnstructuredContainer finds a container by name from a deployment's unstructured object.
func findUnstructuredContainer(t *testing.T, obj map[string]any, containerName string) (map[string]any, bool) {
	t.Helper()
	containers, found, err := uns.NestedSlice(obj, "spec", "template", "spec", "containers")
	if err != nil || !found {
		return nil, false
	}
	for _, c := range containers {
		cm := c.(map[string]any)
		name, _, _ := uns.NestedString(cm, "name")
		if name == containerName {
			return cm, true
		}
	}
	return nil, false
}

// TestGCPCredentialsPathTemplateRendering tests that the managed controller.yaml template
// correctly renders GOOGLE_APPLICATION_CREDENTIALS when GCPCredentialsPath is set.
func TestGCPCredentialsPathTemplateRendering(t *testing.T) {
	tests := []struct {
		name                       string
		gcpCredentialsPath         string
		expectGoogleAppCredentials bool
		expectedValue              string
	}{
		{
			name:                       "GCPCredentialsPath set renders GOOGLE_APPLICATION_CREDENTIALS",
			gcpCredentialsPath:         "/etc/secret/cloudprovider/application_default_credentials.json",
			expectGoogleAppCredentials: true,
			expectedValue:              "/etc/secret/cloudprovider/application_default_credentials.json",
		},
		{
			name:                       "GCPCredentialsPath empty omits GOOGLE_APPLICATION_CREDENTIALS",
			gcpCredentialsPath:         "",
			expectGoogleAppCredentials: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := makeManagedControllerRenderData()
			data.Data["GCPCredentialsPath"] = tc.gcpCredentialsPath

			objs, err := render.RenderDir("../../bindata/cloud-network-config-controller/managed", &data)
			if err != nil {
				t.Fatalf("failed to render managed controller: %v", err)
			}

			for _, obj := range objs {
				if obj.GetKind() != "Deployment" {
					continue
				}

				container, found := findUnstructuredContainer(t, obj.Object, "controller")
				if !found {
					t.Fatal("controller container not found in Deployment")
				}

				val, found := getEnvVar(t, container, "GOOGLE_APPLICATION_CREDENTIALS")
				if tc.expectGoogleAppCredentials && !found {
					t.Errorf("expected GOOGLE_APPLICATION_CREDENTIALS in deployment, but not found")
				}
				if !tc.expectGoogleAppCredentials && found {
					t.Errorf("expected GOOGLE_APPLICATION_CREDENTIALS to be absent, but found")
				}
				if tc.expectGoogleAppCredentials && val != tc.expectedValue {
					t.Errorf("expected GOOGLE_APPLICATION_CREDENTIALS value %q, got %q", tc.expectedValue, val)
				}
				return
			}
			t.Fatal("Deployment object not found in rendered output")
		})
	}
}

// TestCloudTokenMinterHasTokenAudience verifies that the cloud-token minter container
// has --token-audience=openshift in its args.
func TestCloudTokenMinterHasTokenAudience(t *testing.T) {
	data := makeManagedControllerRenderData()

	objs, err := render.RenderDir("../../bindata/cloud-network-config-controller/managed", &data)
	if err != nil {
		t.Fatalf("failed to render managed controller: %v", err)
	}

	for _, obj := range objs {
		if obj.GetKind() != "Deployment" {
			continue
		}

		container, found := findUnstructuredContainer(t, obj.Object, "cloud-token")
		if !found {
			t.Fatal("cloud-token container not found in Deployment")
		}

		args, found, err := uns.NestedStringSlice(container, "args")
		if err != nil || !found {
			t.Fatal("args not found in cloud-token-minter container")
		}

		if slices.Contains(args, "--token-audience=openshift") {
			return
		}
		t.Error("expected cloud-token minter to have --token-audience=openshift arg")
		return
	}
	t.Fatal("Deployment object not found in rendered output")
}

func TestCloudNetworkConfigControllerReadOnlyRootFilesystems(t *testing.T) {
	tests := []struct {
		name      string
		directory string
		namespace string
	}{
		{name: "self-hosted", directory: "self-hosted", namespace: "openshift-cloud-network-config-controller"},
		{name: "managed", directory: "managed", namespace: "test-ns"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := makeManagedControllerRenderData()
			objs, err := render.RenderDir("../../bindata/cloud-network-config-controller/"+test.directory, &data)
			if err != nil {
				t.Fatalf("failed to render %s controller: %v", test.name, err)
			}
			expectReadOnlyRootFilesystems(t, objs, test.namespace)
		})
	}
}

func TestManagedCloudNetworkConfigControllerSafeToEvictLocalVolumes(t *testing.T) {
	data := makeManagedControllerRenderData()
	objs, err := render.RenderDir("../../bindata/cloud-network-config-controller/managed", &data)
	if err != nil {
		t.Fatalf("failed to render managed controller: %v", err)
	}

	for _, obj := range objs {
		if obj.GetKind() != "Deployment" {
			continue
		}

		annotation, found, err := uns.NestedString(obj.Object, "spec", "template", "metadata", "annotations", "cluster-autoscaler.kubernetes.io/safe-to-evict-local-volumes")
		if err != nil || !found {
			t.Fatal("safe-to-evict-local-volumes annotation not found in managed controller Deployment")
		}
		if !slices.Contains(strings.Split(annotation, ","), "tmp") {
			t.Errorf("expected safe-to-evict-local-volumes annotation %q to include tmp", annotation)
		}
		return
	}
	t.Fatal("Deployment object not found in rendered output")
}
