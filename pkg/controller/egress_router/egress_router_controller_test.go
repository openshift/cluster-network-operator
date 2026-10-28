package egress_router

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	netopv1 "github.com/openshift/api/networkoperator/v1"
	"github.com/openshift/cluster-network-operator/pkg/render"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// renderNAD renders the production NAD template so assertions check the generated CNI config.
func renderNAD(t *testing.T, router *netopv1.EgressRouter) map[string]any {
	t.Helper()
	data := render.MakeRenderData()
	if err := buildEgressRouterRenderData(&data, "test-ns", router); err != nil {
		t.Fatalf("buildEgressRouterRenderData failed: %v", err)
	}
	manifests, err := render.RenderDir(filepath.Join(manifestDir, "egress-router"), &data)
	if err != nil {
		t.Fatalf("failed to render egress-router manifests: %v", err)
	}

	for _, obj := range manifests {
		if obj.GetKind() == "NetworkAttachmentDefinition" {
			configStr, found, nestedErr := unstructured.NestedString(obj.Object, "spec", "config")
			if nestedErr != nil || !found {
				t.Fatalf("NAD missing spec.config: found=%v, err=%v", found, nestedErr)
			}
			var config map[string]any
			if unmarshalErr := json.Unmarshal([]byte(configStr), &config); unmarshalErr != nil {
				t.Fatalf("NAD spec.config is not valid JSON: %v\nraw: %s", unmarshalErr, configStr)
			}
			return config
		}
	}
	t.Fatal("no NetworkAttachmentDefinition found in rendered manifests")
	return nil
}

// makeRouter supplies the address and redirect rule required by the render-data builder.
func makeRouter(master string, mode netopv1.MacvlanMode) *netopv1.EgressRouter {
	return &netopv1.EgressRouter{
		Spec: netopv1.EgressRouterSpec{
			Mode: netopv1.EgressRouterModeRedirect,
			Redirect: &netopv1.RedirectConfig{
				RedirectRules: []netopv1.L4RedirectRule{
					{
						DestinationIP: "192.168.1.100",
						Port:          8080,
						Protocol:      netopv1.ProtocolTypeTCP,
					},
				},
			},
			NetworkInterface: netopv1.EgressRouterInterface{
				Macvlan: netopv1.MacvlanConfig{
					Mode:   mode,
					Master: master,
				},
			},
			Addresses: []netopv1.EgressRouterAddress{
				{
					IP:      "10.0.0.10/24",
					Gateway: "10.0.0.1",
				},
			},
		},
	}
}

// TestMain keeps template-based tests independent of their package working directory.
func TestMain(m *testing.M) {
	var err error
	manifestDir, err = findBindataDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to locate bindata directory: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// findBindataDir locates repository templates when tests run from this package directory.
func findBindataDir() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	for {
		bindataDir := filepath.Join(dir, "bindata")
		if _, statErr := os.Stat(bindataDir); statErr == nil {
			return bindataDir + "/", nil
		} else if !os.IsNotExist(statErr) {
			return "", fmt.Errorf("stat %q: %w", bindataDir, statErr)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("bindata directory not found")
		}
		dir = parent
	}
}

// TestNADContainsMasterWhenSpecified verifies the configured parent reaches the rendered CNI config.
func TestNADContainsMasterWhenSpecified(t *testing.T) {
	for _, master := range []string{"eth0.100", "uplink+0"} {
		t.Run(master, func(t *testing.T) {
			router := makeRouter(master, netopv1.MacvlanModeBridge)
			config := renderNAD(t, router)

			args, ok := config["interfaceArgs"].(map[string]any)
			if !ok {
				t.Fatal("NAD config missing interfaceArgs")
			}
			if args["master"] != master {
				t.Errorf("expected master=%s, got %v", master, args["master"])
			}
			if args["mode"] != "bridge" {
				t.Errorf("expected mode=bridge, got %v", args["mode"])
			}
			if config["interfaceType"] != "macvlan" {
				t.Errorf("expected interfaceType=macvlan, got %v", config["interfaceType"])
			}
		})
	}
}

// TestNADOmitsMasterWhenEmpty verifies the CNI plugin can still auto-detect an unset parent.
func TestNADOmitsMasterWhenEmpty(t *testing.T) {
	router := makeRouter("", netopv1.MacvlanModeBridge)
	config := renderNAD(t, router)

	args, ok := config["interfaceArgs"].(map[string]any)
	if !ok {
		t.Fatal("NAD config missing interfaceArgs")
	}
	if _, hasMaster := args["master"]; hasMaster {
		t.Errorf("expected no master key when empty, got %v", args["master"])
	}
	if args["mode"] != "bridge" {
		t.Errorf("expected mode=bridge, got %v", args["mode"])
	}
}

// TestNADModeLowercased verifies the API mode is rendered in the lowercase form required by the CNI plugin.
func TestNADModeLowercased(t *testing.T) {
	tests := []struct {
		apiMode  netopv1.MacvlanMode
		expected string
	}{
		{netopv1.MacvlanModeBridge, "bridge"},
		{netopv1.MacvlanModePrivate, "private"},
		{netopv1.MacvlanModeVEPA, "vepa"},
		{netopv1.MacvlanModePassthru, "passthru"},
	}

	for _, tt := range tests {
		t.Run(string(tt.apiMode), func(t *testing.T) {
			router := makeRouter("", tt.apiMode)
			config := renderNAD(t, router)

			args, ok := config["interfaceArgs"].(map[string]any)
			if !ok {
				t.Fatal("NAD config missing interfaceArgs")
			}
			if args["mode"] != tt.expected {
				t.Errorf("mode %s: expected %q, got %v", tt.apiMode, tt.expected, args["mode"])
			}
		})
	}
}

// TestValidateMacvlanMaster checks accepted interface names and rejects unsafe or invalid values.
func TestValidateMacvlanMaster(t *testing.T) {
	tests := []struct {
		name    string
		master  string
		wantErr bool
	}{
		{"valid interface", "eth0.100", false},
		{"valid bond", "bond0", false},
		{"valid vlan subinterface", "bond0.4094", false},
		{"valid with at sign", "macvlan@eth0", false},
		{"valid with plus sign", "uplink+0", false},
		{"colon rejected", "ens3f0np0:1", true},
		{"single dot rejected", ".", true},
		{"double dot rejected", "..", true},
		{"empty allowed", "", false},
		{"double quote rejected", `eth"0`, true},
		{"single quote rejected", "eth'0", true},
		{"backslash rejected", `eth\0`, true},
		{"newline rejected", "eth0\n", true},
		{"tab rejected", "eth0\t", true},
		{"carriage return rejected", "eth0\r", true},
		{"null byte rejected", "eth0\x00", true},
		{"space rejected", "eth 0", true},
		{"unicode rejected", "eth0é", true},
		{"16 chars rejected (IFNAMSIZ)", "abcdefghijklmnop", true},
		{"15 chars allowed (IFNAMSIZ)", "abcdefghijklmno", false},
		{"injection attempt rejected", `eth0", "injected": true, "x`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMacvlanMaster(tt.master)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateMacvlanMaster(%q) error = %v, wantErr = %v", tt.master, err, tt.wantErr)
			}
		})
	}
}

// TestBuildRenderDataRejectsBadMaster ensures invalid masters fail before template rendering.
func TestBuildRenderDataRejectsBadMaster(t *testing.T) {
	router := makeRouter(`eth0"x`, netopv1.MacvlanModeBridge)
	data := render.MakeRenderData()
	err := buildEgressRouterRenderData(&data, "test-ns", router)
	if err == nil {
		t.Fatal("expected error for invalid master, got nil")
	}
	if !strings.Contains(err.Error(), "validate macvlan master") {
		t.Errorf("expected validation context in error, got: %v", err)
	}
}
