package network

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pkg/errors"

	operv1 "github.com/openshift/api/operator/v1"
)

const CNINetworkName = "openshift"

// Whatever network is the default network should use this as the target CNI
// configuration file.
const CNIConfigPath = "/etc/cni/net.d/80-openshift.conflist"

// validateChainedPlugins ensures that all supplied chained plugins are some
// kind of reasonable cni configuration
func validateChainedPlugins(conf *operv1.NetworkSpec) []error {
	out := []error{}

	if len(conf.DefaultNetwork.ChainedPlugins) > 0 && conf.DefaultNetwork.Type != operv1.NetworkTypeOpenShiftSDN {
		out = append(out, errors.Errorf("network type %s does not support chained plugins", conf.DefaultNetwork.Type))
	}

	for i, entry := range conf.DefaultNetwork.ChainedPlugins {
		if entry.RawCNIConfig == "" {
			out = append(out, errors.Errorf("invalid CNI plugin entry Spec.DefaultNetwork.ChainedPlugin[%d]: rawCNIConfig must be specified", i))
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(entry.RawCNIConfig), &m); err != nil {
			out = append(out, errors.Wrapf(err, "invalid json in Spec.DefaultNetwork.ChainedPlugin[%d].RawCNIConfig", i))
			continue
		}

		if _, ok := m["type"]; !ok {
			out = append(out, errors.Errorf("invalid CNI plugin entry in Spec.DefaultNetwork.ChainedPlugin[%d].RawCNIConfig: must have 'type' key", i))
		}
	}

	return out
}

// makeCNIConfig merges the primary plugin configuration (as JSON) with any chained
// plugin's configurations.
func makeCNIConfig(conf *operv1.NetworkSpec, netName, cniVersion, primaryPluginConfig string) string {
	pluginConfigs := []string{primaryPluginConfig}
	for _, entry := range conf.DefaultNetwork.ChainedPlugins {
		pluginConfigs = append(pluginConfigs, entry.RawCNIConfig)
	}

	configFile := fmt.Sprintf(`
{
	"name": "%s",
	"cniVersion": "%s",
	"plugins": [
		%s
	]
}`,
		netName, cniVersion, strings.Join(pluginConfigs, ",\n\t"))

	return configFile
}
