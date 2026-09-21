package render

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ghodss/yaml"
	appsv1 "k8s.io/api/apps/v1"
)

func TestNetworkOperatorHasWritableTmp(t *testing.T) {
	manifests := []string{
		"0000_70_cluster-network-operator_03_deployment.yaml",
		"0000_70_cluster-network-operator_03_deployment-ibm-cloud-managed.yaml",
	}

	for _, manifest := range manifests {
		t.Run(manifest, func(t *testing.T) {
			manifestPath := filepath.Join("..", "..", "manifests", manifest)
			data, err := os.ReadFile(manifestPath)
			if err != nil {
				t.Fatalf("failed to read %s: %v", manifestPath, err)
			}

			deployment := &appsv1.Deployment{}
			decodeErr := yaml.Unmarshal(data, deployment)
			if decodeErr != nil {
				t.Fatalf("failed to decode %s: %v", manifestPath, decodeErr)
			}

			podSpec := deployment.Spec.Template.Spec
			if len(podSpec.Containers) != 1 {
				t.Fatalf("expected one container in %s, got %d", manifestPath, len(podSpec.Containers))
			}
			securityContext := podSpec.Containers[0].SecurityContext
			if securityContext == nil || securityContext.AllowPrivilegeEscalation == nil || *securityContext.AllowPrivilegeEscalation {
				t.Errorf("network-operator in %s must disable privilege escalation", manifestPath)
			}
			if securityContext == nil || securityContext.Capabilities == nil || len(securityContext.Capabilities.Drop) != 1 || securityContext.Capabilities.Drop[0] != "ALL" {
				t.Errorf("network-operator in %s must drop all Linux capabilities", manifestPath)
			}

			// CNO falls back to generating a self-signed serving certificate under
			// /tmp, so this path must remain writable even with a read-only root.
			mountFound := false
			for _, mount := range podSpec.Containers[0].VolumeMounts {
				if mount.Name == "tmp" && mount.MountPath == "/tmp" && !mount.ReadOnly {
					mountFound = true
					break
				}
			}
			if !mountFound {
				t.Errorf("network-operator in %s must have a writable tmp volume mounted at /tmp", manifestPath)
			}

			volumeFound := false
			for _, volume := range podSpec.Volumes {
				if volume.Name == "tmp" && volume.EmptyDir != nil {
					volumeFound = true
					break
				}
			}
			if !volumeFound {
				t.Errorf("network-operator in %s must define a tmp emptyDir volume", manifestPath)
			}
		})
	}
}
