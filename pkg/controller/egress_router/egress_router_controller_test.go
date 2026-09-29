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

// renderNAD uses the production buildEgressRouterRenderData path to render
// the egress-router NAD template and returns the parsed CNI config.
func renderNAD(t *testing.T, router *netopv1.EgressRouter) map[string]interface{} {
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
			configStr, found, err := unstructured.NestedString(obj.Object, "spec", "config")
			if err != nil || !found {
				t.Fatalf("NAD missing spec.config: found=%v, err=%v", found, err)
			}
			var config map[string]interface{}
			if err := json.Unmarshal([]byte(configStr), &config); err != nil {
				t.Fatalf("NAD spec.config is not valid JSON: %v\nraw: %s", err, configStr)
			}
			return config
		}
	}
	t.Fatal("no NetworkAttachmentDefinition found in rendered manifests")
	return nil
}

// makeRouter builds a minimal EgressRouter CR suitable for NAD rendering tests.
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

func TestMain(m *testing.M) {
	var err error
	manifestDir, err = findBindataDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to locate bindata directory: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// findBindataDir walks up from the working directory to locate the bindata directory.
func findBindataDir() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	for {
		bindataDir := filepath.Join(dir, "bindata")
		if _, err := os.Stat(bindataDir); err == nil {
			return bindataDir + "/", nil
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("stat %q: %w", bindataDir, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("bindata directory not found")
		}
		dir = parent
	}
}

// TestNADContainsMasterWhenSpecified ensures the user's chosen parent interface lands in the CNI config.
func TestNADContainsMasterWhenSpecified(t *testing.T) {
	router := makeRouter("eth0.100", netopv1.MacvlanModeBridge)
	config := renderNAD(t, router)

	args, ok := config["interfaceArgs"].(map[string]interface{})
	if !ok {
		t.Fatal("NAD config missing interfaceArgs")
	}
	if args["master"] != "eth0.100" {
		t.Errorf("expected master=eth0.100, got %v", args["master"])
	}
	if args["mode"] != "bridge" {
		t.Errorf("expected mode=bridge, got %v", args["mode"])
	}
	if config["interfaceType"] != "macvlan" {
		t.Errorf("expected interfaceType=macvlan, got %v", config["interfaceType"])
	}
}

// TestNADOmitsMasterWhenEmpty ensures backward compat — CNI plugin auto-detects when master is unset.
func TestNADOmitsMasterWhenEmpty(t *testing.T) {
	router := makeRouter("", netopv1.MacvlanModeBridge)
	config := renderNAD(t, router)

	args, ok := config["interfaceArgs"].(map[string]interface{})
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

// TestNADModeLowercased catches the API→CNI case mismatch (e.g. "Bridge" must become "bridge").
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

			args, ok := config["interfaceArgs"].(map[string]interface{})
			if !ok {
				t.Fatal("NAD config missing interfaceArgs")
			}
			if args["mode"] != tt.expected {
				t.Errorf("mode %s: expected %q, got %v", tt.apiMode, tt.expected, args["mode"])
			}
		})
	}
}

// TestGetAllowedDestinationsConfigJSON checks the "port proto ip [targetPort]" format egress-router-cni expects.
func TestGetAllowedDestinationsConfigJSON(t *testing.T) {
	rules := []netopv1.L4RedirectRule{
		{
			DestinationIP: "192.168.1.100",
			Port:          8080,
			Protocol:      netopv1.ProtocolTypeTCP,
		},
		{
			DestinationIP: "192.168.1.200",
			Port:          9090,
			Protocol:      netopv1.ProtocolTypeUDP,
			TargetPort:    9091,
		},
	}

	result, err := getAllowedDestinationsConfigJSON(rules)
	if err != nil {
		t.Fatalf("getAllowedDestinationsConfigJSON returned an error: %v", err)
	}

	var destinations []string
	if err := json.Unmarshal([]byte(result), &destinations); err != nil {
		t.Fatalf("result is not valid JSON array: %v", err)
	}

	if len(destinations) != 2 {
		t.Fatalf("expected 2 destinations, got %d", len(destinations))
	}
	if destinations[0] != "8080 TCP 192.168.1.100" {
		t.Errorf("unexpected destination[0]: %s", destinations[0])
	}
	if destinations[1] != "9090 UDP 192.168.1.200 9091" {
		t.Errorf("unexpected destination[1]: %s", destinations[1])
	}
}

// TestValidateMacvlanMaster guards against JSON injection via crafted interface names in the NAD template.
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
		{"valid with colon", "ens3f0np0:1", false},
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
