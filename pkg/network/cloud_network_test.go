package network

import (
	"strings"
	"testing"

	"github.com/openshift/cluster-network-operator/pkg/render"
)

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
			data := render.MakeRenderData()
			// Set minimum required template data
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
			data.Data["GCPCredentialsPath"] = tc.gcpCredentialsPath

			objs, err := render.RenderDir("../../bindata/cloud-network-config-controller/managed", &data)
			if err != nil {
				t.Fatalf("failed to render managed controller: %v", err)
			}

			// Find the Deployment object
			var found bool
			for _, obj := range objs {
				if obj.GetKind() != "Deployment" {
					continue
				}
				yaml, err := obj.MarshalJSON()
				if err != nil {
					t.Fatalf("failed to marshal deployment: %v", err)
				}
				yamlStr := string(yaml)

				hasGoogleAppCreds := strings.Contains(yamlStr, "GOOGLE_APPLICATION_CREDENTIALS")
				if tc.expectGoogleAppCredentials && !hasGoogleAppCreds {
					t.Errorf("expected GOOGLE_APPLICATION_CREDENTIALS in deployment, but not found")
				}
				if !tc.expectGoogleAppCredentials && hasGoogleAppCreds {
					t.Errorf("expected GOOGLE_APPLICATION_CREDENTIALS to be absent, but found")
				}
				if tc.expectGoogleAppCredentials && !strings.Contains(yamlStr, tc.expectedValue) {
					t.Errorf("expected GOOGLE_APPLICATION_CREDENTIALS value %q in deployment", tc.expectedValue)
				}
				found = true
			}
			if !found {
				t.Fatal("Deployment object not found in rendered output")
			}
		})
	}
}

// TestCloudTokenMinterHasTokenAudience verifies that the cloud-token minter container
// has --token-audience=openshift in its args.
func TestCloudTokenMinterHasTokenAudience(t *testing.T) {
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

	objs, err := render.RenderDir("../../bindata/cloud-network-config-controller/managed", &data)
	if err != nil {
		t.Fatalf("failed to render managed controller: %v", err)
	}

	for _, obj := range objs {
		if obj.GetKind() != "Deployment" {
			continue
		}
		yaml, err := obj.MarshalJSON()
		if err != nil {
			t.Fatalf("failed to marshal deployment: %v", err)
		}
		yamlStr := string(yaml)
		if !strings.Contains(yamlStr, "--token-audience=openshift") {
			t.Error("expected cloud-token minter to have --token-audience=openshift arg")
		}
		return
	}
	t.Fatal("Deployment object not found in rendered output")
}
