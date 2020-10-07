package network

import (
	"encoding/json"
	operv1 "github.com/openshift/api/operator/v1"
	"log"
)

// useDHCPRaw determines if the the DHCP CNI plugin running as a daemon should be rendered.
func useDHCPRaw(addnet *operv1.AdditionalNetworkDefinition) bool {
	// Parse the RawCNIConfig
	var rawConfig map[string]interface{}
	var err error

	confBytes := []byte(addnet.RawCNIConfig)
	err = json.Unmarshal(confBytes, &rawConfig)
	if err != nil {
		log.Printf("WARNING: Not rendering DHCP daemonset, failed to Unmarshal RawCNIConfig: %v", confBytes)
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

				if typeval == "dhcp" {
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

// useDHCP determines if the the DHCP CNI plugin running as a daemon should be rendered in case of Raw.
func useDHCP(conf *operv1.NetworkSpec) bool {
	renderdhcp := false

	// This isn't useful without Multinetwork.
	if *conf.DisableMultiNetwork {
		return renderdhcp
	}

	// Look and see if we have an AdditionalNetworks
	if conf.AdditionalNetworks != nil {
		for _, addnet := range conf.AdditionalNetworks {
			switch addnet.Type {
			case operv1.NetworkTypeRaw:
				renderdhcp = renderdhcp || useDHCPRaw(&addnet)
			case operv1.NetworkTypeSimpleMacvlan:
				renderdhcp = renderdhcp || useDHCPSimpleMacvlan(addnet.SimpleMacvlanConfig)
			}

			if renderdhcp == true {
				break
			}
		}
	}

	return renderdhcp
}
