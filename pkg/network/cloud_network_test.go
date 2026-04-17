package network

import (
	"testing"

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
	return data
}

// getEnvVar looks up an env var by name from a container map and returns its value.
func getEnvVar(t *testing.T, container map[string]interface{}, name string) (string, bool) {
	t.Helper()
	envSlice, found, err := uns.NestedSlice(container, "env")
	if err != nil || !found {
		return "", false
	}
	for _, e := range envSlice {
		em := e.(map[string]interface{})
		n, _, _ := uns.NestedString(em, "name")
		if n == name {
			v, _, _ := uns.NestedString(em, "value")
			return v, true
		}
	}
	return "", false
}

// findUnstructuredContainer finds a container by name from a deployment's unstructured object.
func findUnstructuredContainer(t *testing.T, obj map[string]interface{}, containerName string) (map[string]interface{}, bool) {
	t.Helper()
	containers, found, err := uns.NestedSlice(obj, "spec", "template", "spec", "containers")
	if err != nil || !found {
		return nil, false
	}
	for _, c := range containers {
		cm := c.(map[string]interface{})
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

		for _, arg := range args {
			if arg == "--token-audience=openshift" {
				return
			}
		}
		t.Error("expected cloud-token minter to have --token-audience=openshift arg")
		return
	}
	t.Fatal("Deployment object not found in rendered output")
}
