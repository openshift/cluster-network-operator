package network

import (
	"testing"
)

func TestValidateLabel(t *testing.T) {
	tests := []struct {
		name  string
		label string
		valid bool
	}{
		{
			name:  "valid label with empty value",
			label: "network.operator.openshift.io/dpu-host=",
			valid: true,
		},
		{
			name:  "valid label with value",
			label: "network.operator.openshift.io/dpu-host=true",
			valid: true,
		},
		{
			name:  "valid label with simple key",
			label: "dpu-host=enabled",
			valid: true,
		},
		{
			name:  "valid label with underscore in value",
			label: "my-label=test_value",
			valid: true,
		},
		{
			name:  "valid label with dots in value",
			label: "my-label=test.value",
			valid: true,
		},
		{
			name:  "valid label with hyphens in value",
			label: "my-label=test-value",
			valid: true,
		},
		{
			name:  "valid label with numbers in value",
			label: "my-label=123test",
			valid: true,
		},
		{
			name:  "key starting with number",
			label: "123key=value",
			valid: true,
		},
		{
			name:  "empty label",
			label: "",
			valid: false,
		},
		{
			name:  "missing equals sign",
			label: "invalid-label-no-equals",
			valid: false,
		},
		{
			name:  "empty key",
			label: "=value",
			valid: false,
		},
		{
			name:  "key with spaces",
			label: "key with spaces=value",
			valid: false,
		},
		{
			name:  "value with spaces",
			label: "key=value with spaces",
			valid: false,
		},
		{
			name:  "key too long",
			label: "very-long-key-that-exceeds-kubernetes-limits-and-should-fail-validation-because-it-is-too-long=value",
			valid: false,
		},
		{
			name:  "value too long",
			label: "key=very-long-value-that-exceeds-kubernetes-limits-and-should-fail-validation-because-it-is-too-long-to-be-valid",
			valid: false,
		},
		{
			name:  "key with invalid characters",
			label: "key@invalid=value",
			valid: false,
		},
		{
			name:  "value with invalid characters",
			label: "key=value@invalid",
			valid: false,
		},
		{
			name:  "key ending with hyphen",
			label: "key-=value",
			valid: false,
		},
		{
			name:  "value starting with hyphen",
			label: "key=-value",
			valid: false,
		},
		{
			name:  "value ending with hyphen",
			label: "key=value-",
			valid: false,
		},
		{
			name:  "multiple equals signs",
			label: "key=value1=value2",
			valid: false,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			result := validateLabel(testCase.label)
			if result != testCase.valid {
				t.Errorf("validateLabel(%q) = %v, expected %v", testCase.label, result, testCase.valid)
			}
		})
	}
}

func TestValidateLabelUsage(t *testing.T) {
	// Test some specific scenarios that would be used in the hardware-offload-config ConfigMap
	validLabels := []string{
		"network.operator.openshift.io/dpu-host=",
		"network.operator.openshift.io/dpu=",
		"network.operator.openshift.io/smart-nic=",
		"network.operator.openshift.io/dpu-host=true",
		"network.operator.openshift.io/smart-nic=enabled",
		"network.operator.openshift.io/hardware-type=dpu",
		"hardware-offload=true",
	}

	invalidLabels := []string{
		"invalid label without equals",
		"=no-key",
		"key with spaces=value",
		"key=value with spaces",
		"key@invalid=value",
		"key=value@invalid",
	}

	for _, label := range validLabels {
		t.Run("valid_"+label, func(t *testing.T) {
			if !validateLabel(label) {
				t.Errorf("Expected label %q to be valid, but validation failed", label)
			}
		})
	}

	for _, label := range invalidLabels {
		t.Run("invalid_"+label, func(t *testing.T) {
			if validateLabel(label) {
				t.Errorf("Expected label %q to be invalid, but validation passed", label)
			}
		})
	}
}
