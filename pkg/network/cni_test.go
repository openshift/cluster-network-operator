package network

import (
	"testing"

	operv1 "github.com/openshift/api/operator/v1"

	. "github.com/onsi/gomega"
)

func TestMakeCNIConfig(t *testing.T) {
	g := NewGomegaWithT(t)
	conf := operv1.NetworkSpec{}

	out := makeCNIConfig(&conf, "test1", "0.1.2", `{"type": "only"}`)
	g.Expect(out).To(MatchJSON(`
{
	"cniVersion": "0.1.2",
	"name": "test1",
	"plugins": [{"type": "only"}]
}`))

	conf.DefaultNetwork.ChainedPlugins = []operv1.ChainedPluginEntry{
		{RawCNIConfig: `{"type": "foo"}`},
		{RawCNIConfig: `{"type": "bar", "a": "b", "c":{"a": "b", "c": "d"}}`},
	}

	out = makeCNIConfig(&conf, "test2", "1.2.3", `{"type": "primary"}`)
	g.Expect(out).To(MatchJSON(`
{
	"cniVersion": "1.2.3",
	"name": "test2",
	"plugins": [
	  {"type": "primary"},
		{"type": "foo"},
		{"type": "bar",
			"a": "b",
			"c": {
				"a": "b",
				"c": "d"
			}
		}]
}`))

}

func TestValidateChainedPlugins(t *testing.T) {
	g := NewGomegaWithT(t)

	conf := &operv1.NetworkSpec{}
	g.Expect(validateChainedPlugins(conf)).To(BeEmpty())

	conf.DefaultNetwork.ChainedPlugins = []operv1.ChainedPluginEntry{
		{RawCNIConfig: `{"type": "foo"}`},
		{RawCNIConfig: `{"type": "bar"}`},
	}
	conf.DefaultNetwork.Type = "unknown"
	g.Expect(validateChainedPlugins(conf)).To(ContainElement(MatchError(
		ContainSubstring("network type unknown does not support chained plugins"))))

	conf.DefaultNetwork.Type = "OpenShiftSDN"
	g.Expect(validateChainedPlugins(conf)).To(BeEmpty())

	conf.DefaultNetwork.ChainedPlugins = []operv1.ChainedPluginEntry{
		{RawCNIConfig: `asdfasdf`},
	}
	g.Expect(validateChainedPlugins(conf)).To(ContainElement(MatchError(
		ContainSubstring("invalid json in Spec.DefaultNetwork.ChainedPlugin[0].RawCNIConfig"))))

	conf.DefaultNetwork.ChainedPlugins = []operv1.ChainedPluginEntry{
		{RawCNIConfig: `{"type": "foo"}`},
		{RawCNIConfig: `{"name": "bar"}`},
	}
	g.Expect(validateChainedPlugins(conf)).To(ContainElement(MatchError(
		ContainSubstring("invalid CNI plugin entry in Spec.DefaultNetwork.ChainedPlugin[1].RawCNIConfig: must have 'type' key"))))

}
