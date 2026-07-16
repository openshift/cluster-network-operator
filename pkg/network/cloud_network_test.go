package network

import (
	"context"
	"fmt"
	"strings"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	operv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/cluster-network-operator/pkg/bootstrap"
	"github.com/openshift/cluster-network-operator/pkg/render"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
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
	data.Data["OSMaxAllowedAddressPairs"] = 0
	data.Data["OSMaxAllowedAddressPairsIsSet"] = false
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
	data.Data["OSMaxAllowedAddressPairs"] = 0
	data.Data["OSMaxAllowedAddressPairsIsSet"] = false
	return data
}

func TestOSMaxAllowedAddressPairsManagedTemplateRendering(t *testing.T) {
	tests := []struct {
		name        string
		value       int
		isSet       bool
		expectFlag  bool
		expectValue string
	}{
		{
			name:       "not set - flag absent",
			value:      0,
			isSet:      false,
			expectFlag: false,
		},
		{
			name:        "valid value 20 - flag present",
			value:       20,
			isSet:       true,
			expectFlag:  true,
			expectValue: "20",
		},
		{
			name:        "valid value 1 - flag present",
			value:       1,
			isSet:       true,
			expectFlag:  true,
			expectValue: "1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := makeManagedControllerRenderData()
			data.Data["OSMaxAllowedAddressPairs"] = tc.value
			data.Data["OSMaxAllowedAddressPairsIsSet"] = tc.isSet

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

func TestOSMaxAllowedAddressPairsSelfHostedTemplateRendering(t *testing.T) {
	tests := []struct {
		name        string
		value       int
		isSet       bool
		expectFlag  bool
		expectValue string
	}{
		{
			name:       "not set - flag absent",
			value:      0,
			isSet:      false,
			expectFlag: false,
		},
		{
			name:        "valid value 20 - flag present",
			value:       20,
			isSet:       true,
			expectFlag:  true,
			expectValue: "20",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := makeSelfHostedControllerRenderData()
			data.Data["OSMaxAllowedAddressPairs"] = tc.value
			data.Data["OSMaxAllowedAddressPairsIsSet"] = tc.isSet

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

func TestOSMaxAllowedAddressPairsRenderValidation(t *testing.T) {
	tests := []struct {
		name      string
		value     int
		isSet     bool
		rawValue  string
		expectErr bool
		errSubstr string
	}{
		{
			name:      "zero when set - returns error",
			value:     0,
			isSet:     true,
			rawValue:  "0",
			expectErr: true,
			errSubstr: `"0"`,
		},
		{
			name:      "negative when set - returns error",
			value:     -5,
			isSet:     true,
			rawValue:  "-5",
			expectErr: true,
			errSubstr: `"-5"`,
		},
		{
			name:      "non-integer parse failure - returns error",
			value:     0,
			isSet:     true,
			rawValue:  "abc",
			expectErr: true,
			errSubstr: `"abc"`,
		},
		{
			name:      "valid positive value - no error",
			value:     20,
			isSet:     true,
			rawValue:  "20",
			expectErr: false,
		},
		{
			name:      "not set - no error",
			value:     0,
			isSet:     false,
			expectErr: false,
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
					PlatformType: configv1.OpenStackPlatformType,
					PlatformStatus: &configv1.PlatformStatus{
						Type: configv1.OpenStackPlatformType,
					},
					APIServers: map[string]bootstrap.APIServer{
						bootstrap.APIServerDefault:      {Host: "localhost", Port: "6443"},
						bootstrap.APIServerDefaultLocal: {Host: "localhost", Port: "6443"},
					},
					KubeCloudConfig: map[string]string{},
				},
				CloudNetworkConfig: bootstrap.CloudNetworkConfigBootstrapResult{
					OSMaxAllowedAddressPairs: bootstrap.OSMaxAllowedAddressPairs{
						Value:    tc.value,
						IsSet:    tc.isSet,
						RawValue: tc.rawValue,
					},
				},
			}

			_, err := renderCloudNetworkConfigController(conf, br, "../../bindata")
			if tc.expectErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.expectErr && err != nil {
				t.Errorf("expected no error, got: %v", err)
			}
			if tc.expectErr && err != nil && !strings.Contains(err.Error(), tc.errSubstr) {
				t.Errorf("expected error to contain %q, got: %v", tc.errSubstr, err)
			}
		})
	}
}

type erroringReader struct {
	err error
}

func (r *erroringReader) Get(_ context.Context, _ crclient.ObjectKey, _ crclient.Object, _ ...crclient.GetOption) error {
	return r.err
}

func (r *erroringReader) List(_ context.Context, _ crclient.ObjectList, _ ...crclient.ListOption) error {
	return r.err
}

func TestCloudNetworkConfigBootstrapErrorPropagation(t *testing.T) {
	tests := []struct {
		name      string
		reader    crclient.Reader
		expectErr bool
		errSubstr string
		expectRes bootstrap.CloudNetworkConfigBootstrapResult
	}{
		{
			name:      "transient API error is propagated",
			reader:    &erroringReader{err: fmt.Errorf("connection refused")},
			expectErr: true,
			errSubstr: "connection refused",
		},
		{
			name: "forbidden error is propagated",
			reader: &erroringReader{err: apierrors.NewForbidden(
				schema.GroupResource{Group: "", Resource: "configmaps"}, "cloud-network-config", fmt.Errorf("access denied"),
			)},
			expectErr: true,
			errSubstr: "forbidden",
		},
		{
			name: "not-found error returns empty result without error",
			reader: &erroringReader{err: apierrors.NewNotFound(
				schema.GroupResource{Group: "", Resource: "configmaps"}, "cloud-network-config",
			)},
			expectErr: false,
			expectRes: bootstrap.CloudNetworkConfigBootstrapResult{},
		},
		{
			name: "ConfigMap present with valid value succeeds",
			reader: crfake.NewClientBuilder().WithObjects(&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cloud-network-config",
					Namespace: "openshift-network-operator",
				},
				Data: map[string]string{
					"platform-os-max-allowed-address-pairs": "15",
				},
			}).Build(),
			expectErr: false,
			expectRes: bootstrap.CloudNetworkConfigBootstrapResult{
				OSMaxAllowedAddressPairs: bootstrap.OSMaxAllowedAddressPairs{
					Value:    15,
					IsSet:    true,
					RawValue: "15",
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := cloudNetworkConfigBootstrap(tc.reader)
			if tc.expectErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tc.errSubstr) {
					t.Errorf("expected error containing %q, got: %v", tc.errSubstr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result != tc.expectRes {
				t.Errorf("result mismatch:\n  got:  %+v\n  want: %+v", result, tc.expectRes)
			}
		})
	}
}
