package connectivitycheck

import (
	"testing"
)

func TestNodeNameForLabel(t *testing.T) {
	testCases := []struct {
		name     string
		nodeName string
		expected string
	}{
		{
			name:     "FQDN returns only the segment before the first dot",
			nodeName: "node1.example.com",
			expected: "node1",
		},
		{
			name:     "simple name with no dots is returned unchanged",
			nodeName: "node999",
			expected: "node999",
		},
		{
			name:     "valid IPv6 address has colons replaced with dashes",
			nodeName: "fd00::1",
			expected: "fd00--1",
		},
		{
			name:     "valid IPv4 address has dots replaced with dashes",
			nodeName: "192.168.0.1",
			expected: "192-168-0-1",
		},
		{
			name:     "invalid IPv4 address (octet out of range) is treated as an FQDN",
			nodeName: "192.168.0.300",
			expected: "192",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			actual := nodeNameForLabel(tc.nodeName)
			if actual != tc.expected {
				t.Errorf("nodeNameForLabel(%q): expected %q, got %q", tc.nodeName, tc.expected, actual)
			}
		})
	}
}
