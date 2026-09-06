package network

import (
	"testing"

	. "github.com/onsi/gomega"
	operv1 "github.com/openshift/api/operator/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestRenderIPTablesAlerterQuotesYAMLStringFields(t *testing.T) {
	g := NewGomegaWithT(t)

	bootstrapResult := fakeBootstrapResult()
	bootstrapResult.IPTablesAlerter.Enabled = true

	objs, err := renderIPTablesAlerter(&operv1.NetworkSpec{}, bootstrapResult, manifestDir)
	g.Expect(err).NotTo(HaveOccurred())

	var scriptCM *uns.Unstructured
	for _, obj := range objs {
		if obj.GetKind() == "ConfigMap" && obj.GetName() == "iptables-alerter-script" {
			scriptCM = obj
			break
		}
	}
	g.Expect(scriptCM).NotTo(BeNil())

	script, found, err := uns.NestedString(scriptCM.Object, "data", "iptables-alerter.sh")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())

	// Unquoted YAML 1.1 booleans such as yes/no/true/false/on/off unmarshal as
	// bool and crash kubectl when metadata.namespace/name must be strings.
	g.Expect(script).To(ContainSubstring(`namespace: "${pod_namespace}"`))
	g.Expect(script).To(ContainSubstring(`name: "${pod_name}"`))
	g.Expect(script).To(ContainSubstring(`uid: "${pod_uid}"`))
	g.Expect(script).To(ContainSubstring(`pod-uid: "${pod_uid}"`))
	g.Expect(script).To(ContainSubstring(`reportingInstance: "${ALERTER_POD_NAME}"`))
}
