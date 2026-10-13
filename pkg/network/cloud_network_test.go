package network

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"github.com/openshift/cluster-network-operator/pkg/render"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

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
	data.Data["OpenStackMaxAllowedAddressPairs"] = 0
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

func makeSelfHostedControllerRenderData() render.RenderData {
	data := render.MakeRenderData()
	data.Data["ReleaseVersion"] = "4.18.0"
	data.Data["PlatformType"] = "OpenStack"
	data.Data["PlatformRegion"] = "regionOne"
	data.Data["PlatformTypeAWS"] = "AWS"
	data.Data["PlatformTypeAzure"] = "Azure"
	data.Data["PlatformTypeGCP"] = "GCP"
	data.Data["CloudNetworkConfigControllerImage"] = "test-image"
	data.Data["KubernetesServiceURL"] = "https://localhost:6443"
	data.Data["ExternalControlPlane"] = false
	data.Data["PlatformAzureEnvironment"] = ""
	data.Data["PlatformAWSCAPath"] = ""
	data.Data["PlatformAPIURL"] = ""
	data.Data["HTTP_PROXY"] = ""
	data.Data["HTTPS_PROXY"] = ""
	data.Data["NO_PROXY"] = ""
	data.Data["OpenStackMaxAllowedAddressPairs"] = 0
	return data
}

func TestOpenStackMaxAllowedAddressPairsManagedTemplateRendering(t *testing.T) {
	tests := []struct {
		name        string
		value       int
		expectFlag  bool
		expectValue string
	}{
		{
			name:       "not set - flag absent",
			value:      0,
			expectFlag: false,
		},
		{
			name:        "valid value 20 - flag present",
			value:       20,
			expectFlag:  true,
			expectValue: "20",
		},
		{
			name:        "valid value 1 - flag present",
			value:       1,
			expectFlag:  true,
			expectValue: "1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := makeManagedControllerRenderData()
			data.Data["OpenStackMaxAllowedAddressPairs"] = tc.value

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

				cmdSlice, found, err := uns.NestedStringSlice(container, "command")
				if err != nil || !found || len(cmdSlice) < 3 {
					t.Fatal("command not found in controller container")
				}
				shellScript := cmdSlice[2]

				flagStr := fmt.Sprintf("-platform-os-max-allowed-address-pairs=%s", tc.expectValue)
				if tc.expectFlag && !strings.Contains(shellScript, flagStr) {
					t.Errorf("expected shell script to contain %q, but it does not.\nScript:\n%s", flagStr, shellScript)
				}
				if !tc.expectFlag && strings.Contains(shellScript, "-platform-os-max-allowed-address-pairs=") {
					t.Errorf("expected shell script to NOT contain the flag, but it does.\nScript:\n%s", shellScript)
				}
				return
			}
			t.Fatal("Deployment object not found in rendered output")
		})
	}
}

func TestOpenStackMaxAllowedAddressPairsSelfHostedTemplateRendering(t *testing.T) {
	tests := []struct {
		name        string
		value       int
		expectFlag  bool
		expectValue string
	}{
		{
			name:       "not set - flag absent",
			value:      0,
			expectFlag: false,
		},
		{
			name:        "valid value 20 - flag present",
			value:       20,
			expectFlag:  true,
			expectValue: "20",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := makeSelfHostedControllerRenderData()
			data.Data["OpenStackMaxAllowedAddressPairs"] = tc.value

			objs, err := render.RenderDir("../../bindata/cloud-network-config-controller/self-hosted", &data)
			if err != nil {
				t.Fatalf("failed to render self-hosted controller: %v", err)
			}

			for _, obj := range objs {
				if obj.GetKind() != "Deployment" {
					continue
				}

				container, found := findUnstructuredContainer(t, obj.Object, "controller")
				if !found {
					t.Fatal("controller container not found in Deployment")
				}

				args, found, err := uns.NestedStringSlice(container, "args")
				if err != nil || !found {
					t.Fatal("args not found in controller container")
				}

				flagStr := fmt.Sprintf("-platform-os-max-allowed-address-pairs=%s", tc.expectValue)
				var hasFlag bool
				for _, arg := range args {
					if tc.expectFlag && arg == flagStr {
						hasFlag = true
						break
					}
					if !tc.expectFlag && strings.Contains(arg, "-platform-os-max-allowed-address-pairs=") {
						t.Errorf("expected args to NOT contain the flag, but found %q", arg)
						return
					}
				}
				if tc.expectFlag && !hasFlag {
					t.Errorf("expected args to contain %q, but it was not found.\nArgs: %v", flagStr, args)
				}
				return
			}
			t.Fatal("Deployment object not found in rendered output")
		})
	}
}

func TestOpenStackMaxAllowedAddressPairsRender(t *testing.T) {
	tests := []struct {
		name  string
		value int32
	}{
		{
			name:  "valid positive value",
			value: 20,
		},
		{
			name:  "not set",
			value: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			conf := &operv1.NetworkSpec{
				DefaultNetwork: operv1.DefaultNetworkDefinition{
					Type: operv1.NetworkTypeOVNKubernetes,
				},
			}
			br := &bootstrap.BootstrapResult{
				Infra: bootstrap.InfraStatus{
					PlatformType:                    configv1.OpenStackPlatformType,
					OpenStackMaxAllowedAddressPairs: tc.value,
					PlatformStatus: &configv1.PlatformStatus{
						Type: configv1.OpenStackPlatformType,
					},
					APIServers: map[string]bootstrap.APIServer{
						bootstrap.APIServerDefault:      {Host: "localhost", Port: "6443"},
						bootstrap.APIServerDefaultLocal: {Host: "localhost", Port: "6443"},
					},
					KubeCloudConfig: map[string]string{},
				},
			}

			manifests, err := renderCloudNetworkConfigController(conf, br, "../../bindata")
			if err != nil {
				t.Fatalf("failed to render cloud network config controller: %v", err)
			}

			manifestJSON, err := json.Marshal(manifests)
			if err != nil {
				t.Fatalf("failed to marshal rendered manifests: %v", err)
			}
			rendered := string(manifestJSON)
			flag := "-platform-os-max-allowed-address-pairs="
			if tc.value == 0 {
				if strings.Contains(rendered, flag) {
					t.Errorf("did not expect %q in rendered manifests", flag)
				}
			} else if expected := fmt.Sprintf("%s%d", flag, tc.value); !strings.Contains(rendered, expected) {
				t.Errorf("expected rendered manifests to contain %q", expected)
			}
		})
	}
}
