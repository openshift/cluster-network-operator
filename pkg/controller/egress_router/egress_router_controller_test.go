package egress_router

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	netopv1 "github.com/openshift/api/networkoperator/v1"
	"github.com/openshift/cluster-network-operator/pkg/render"
)

func renderNAD(t *testing.T, router *netopv1.EgressRouter) map[string]interface{} {
	t.Helper()
	data := render.MakeRenderData()
	data.Data["ReleaseVersion"] = "test"
	data.Data["EgressRouterNamespace"] = "test-ns"
	data.Data["Addresses"] = router.Spec.Addresses[0].IP
	data.Data["Gateway"] = router.Spec.Addresses[0].Gateway
	if router.Spec.Redirect != nil {
		dest, _ := getAllowedDestinationsConfigJSON(router.Spec.Redirect.RedirectRules)
		data.Data["AllowedDestinations"] = dest
		data.Data["FallbackIP"] = router.Spec.Redirect.FallbackIP
	} else {
		data.Data["AllowedDestinations"] = "[]"
		data.Data["FallbackIP"] = ""
	}
	data.Data["mode"] = router.Spec.Mode
	data.Data["MacvlanMaster"] = router.Spec.NetworkInterface.Macvlan.Master
	data.Data["MacvlanMode"] = strings.ToLower(string(router.Spec.NetworkInterface.Macvlan.Mode))
	data.Data["EgressRouterPodImage"] = "test-image"

	manifests, err := render.RenderDir(filepath.Join(manifestDir, "egress-router"), &data)
	if err != nil {
		t.Fatalf("failed to render egress-router manifests: %v", err)
	}

	for _, obj := range manifests {
		if obj.GetKind() == "NetworkAttachmentDefinition" {
			configStr, found, err := unstructuredNestedString(obj.Object, "spec", "config")
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

func unstructuredNestedString(obj map[string]interface{}, fields ...string) (string, bool, error) {
	current := obj
	for i, field := range fields {
		if i == len(fields)-1 {
			val, ok := current[field]
			if !ok {
				return "", false, nil
			}
			s, ok := val.(string)
			return s, ok, nil
		}
		next, ok := current[field]
		if !ok {
			return "", false, nil
		}
		current, ok = next.(map[string]interface{})
		if !ok {
			return "", false, nil
		}
	}
	return "", false, nil
}

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
	manifestDir = findBindataDir()
	os.Exit(m.Run())
}

func findBindataDir() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "bindata")); err == nil {
			return filepath.Join(dir, "bindata") + "/"
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "bindata/"
		}
		dir = parent
	}
}

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

			args := config["interfaceArgs"].(map[string]interface{})
			if args["mode"] != tt.expected {
				t.Errorf("mode %s: expected %q, got %v", tt.apiMode, tt.expected, args["mode"])
			}
		})
	}
}

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
		t.Fatalf("unexpected error: %v", err)
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
