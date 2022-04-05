package network

import (
	"encoding/json"
	"log"

	operv1 "github.com/openshift/api/operator/v1"
)

const ipamTypeDHCP = "dhcp"
const ipamTypeWhereabouts = "whereabouts"

// detectIPAMTypeRaw determines if a target type of IPAM is being used
// this facilitates using auxillary features associated with that IPAM (such as DHCP CNI daemon, or ip-reconciler for Whereabouts)
func detectIPAMTypeRaw(targetType string, addNet *operv1.AdditionalNetworkDefinition) bool {
	// Parse the RawCNIConfig
	var rawConfig map[string]interface{}
	var err error

	confBytes := []byte(addNet.RawCNIConfig)
	err = json.Unmarshal(confBytes, &rawConfig)
	if err != nil {
		log.Printf("WARNING: Cannot detect multus network IPAM type, failed to Unmarshal RawCNIConfig: %v", confBytes)
		return false
	}

	// Cycle through the IPAM keys, and determine if the type is dhcp
	if rawConfig["ipam"] != nil {
		ipam, okipamcast := rawConfig["ipam"].(map[string]interface{})
		if !okipamcast {
			log.Printf("WARNING: IPAM element has data of type %T but wanted map[string]interface{}", rawConfig["ipam"])
			return false
		}

		for key, value := range ipam {
			if key == "type" {
				typeval, oktypecast := value.(string)
				if !oktypecast {
					log.Printf("WARNING: IPAM type element has data of type %T but wanted string", value)
					return false
				}

				if typeval == targetType {
					return true
				}
			}
		}
	}
	return false
}

// useDHCPSimpleMacvlan determines if the the DHCP CNI plugin running as a daemon should be rendered in case of SimpleMacvlan.
func useDHCPSimpleMacvlan(conf *operv1.SimpleMacvlanConfig) bool {
	// if IPAMConfig is not configured, DHCP is used (as default IPAM is DHCP)
	if conf == nil || conf.IPAMConfig == nil {
		return true
	}

	// if IPAMConfig.Type is DHCP, DHCP is of course used
	if conf.IPAMConfig.Type == operv1.IPAMTypeDHCP {
		return true
	}
	return false
}

// detectAuxiliaryIPAM detects if an auxiliary ipam is used.
func detectAuxiliaryIPAM(conf *operv1.NetworkSpec) (bool, bool) {
	renderdhcp := false
	renderwhereabouts := false

	// This isn't useful without Multinetwork.
	if *conf.DisableMultiNetwork {
		return renderdhcp, renderwhereabouts
	}

	// Look and see if we have an AdditionalNetworks
	if conf.AdditionalNetworks != nil {
		for _, addnet := range conf.AdditionalNetworks {
			switch addnet.Type {
			case operv1.NetworkTypeRaw:
				renderdhcp = renderdhcp || detectIPAMTypeRaw(ipamTypeDHCP, &addnet)
				renderwhereabouts = renderwhereabouts || detectIPAMTypeRaw(ipamTypeWhereabouts, &addnet)
			case operv1.NetworkTypeSimpleMacvlan:
				// SimpleMacvlan only supports static and DHCP. So we don't detect whereabouts.
				renderdhcp = renderdhcp || useDHCPSimpleMacvlan(addnet.SimpleMacvlanConfig)
			}

			if renderdhcp && renderwhereabouts {
				break
			}
		}
	}

	return renderdhcp, renderwhereabouts
}
