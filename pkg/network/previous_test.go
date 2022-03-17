package network

import (
	"strings"
	"testing"

	k8syaml "k8s.io/apimachinery/pkg/util/yaml"

	operv1 "github.com/openshift/api/operator/v1"

	. "github.com/onsi/gomega"
)

// TestPreviousConversion ensures that types and defaults are compatable with
// previous deployed versions of the operator.
// One important principle is that the generated state with defaults applied
// *must* always be safe, even as the API evolves
func TestPreviousVersionsSafe(t *testing.T) {
	testcases := []struct {
		name string

		// The configuration expected to be provided by the user.
		inputConfig string

		// The configuration after running through the fillDefaults **FOR THAT VERSION OF THE OPERATOR**
		appliedConfig string
	}{

		// The default configuration for a 4.1.0 cluster
		{
			name: "4.1.0 openshift-sdn",

			inputConfig: `{"clusterNetwork":[{"cidr":"10.128.0.0/14","hostPrefix":23}],"defaultNetwork":{"type":"OpenShiftSDN"},"serviceNetwork":["172.30.0.0/16"]}`,

			appliedConfig: `{"clusterNetwork":[{"cidr":"10.128.0.0/14","hostPrefix":23}],"serviceNetwork":["172.30.0.0/16"],"defaultNetwork":{"type":"OpenShiftSDN","openshiftSDNConfig":{"mode":"NetworkPolicy","vxlanPort":4789,"mtu":8951}},"disableMultiNetwork":false,"deployKubeProxy":false,"kubeProxyConfig":{"bindAddress":"0.0.0.0","proxyArguments":{"metrics-bind-address":["0.0.0.0"],"metrics-port":["9101"]}}}'`,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			g := NewGomegaWithT(t)
			input, err := parseNetworkSpec(tc.inputConfig)
			g.Expect(err).NotTo(HaveOccurred())

			applied, err := parseNetworkSpec(tc.appliedConfig)
			g.Expect(err).NotTo(HaveOccurred())
			fillDefaults(applied, applied)

			// This is the exact config transformation flow in the operator
			g.Expect(Validate(input)).NotTo(HaveOccurred())
			fillDefaults(input, applied)
			g.Expect(IsChangeSafe(applied, input, &fakeBootstrapResult().Infra)).NotTo(HaveOccurred())
		})
	}
}

func parseNetworkSpec(in string) (*operv1.NetworkSpec, error) {
	f := strings.NewReader(in)
	decoder := k8syaml.NewYAMLOrJSONDecoder(f, 4096)
	spec := operv1.NetworkSpec{}
	err := decoder.Decode(&spec)

	if err != nil {
		return nil, err
	}
	return &spec, nil
}
