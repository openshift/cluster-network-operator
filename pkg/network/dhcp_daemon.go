package network

import (
	"encoding/json"
	operv1 "github.com/openshift/api/operator/v1"
	"log"
)

// UseDHCP determines if the the DHCP CNI plugin running as a daemon should be rendered.
func UseDHCP(conf *operv1.NetworkSpec) bool {
	renderdhcp := false

	// This isn't useful without Multinetwork.
	if *conf.DisableMultiNetwork {
		return renderdhcp
	}

	// Look and see if we have an AdditionalNetworks
	if conf.AdditionalNetworks != nil {
		for _, addnet := range conf.AdditionalNetworks {
			// Parse the RawCNIConfig
			var rawConfig map[string]interface{}
			var err error

			confBytes := []byte(addnet.RawCNIConfig)
			err = json.Unmarshal(confBytes, &rawConfig)
			if err != nil {
				log.Printf("WARNING: Not rendering DHCP daemonset, failed to Unmarshal RawCNIConfig: %v", confBytes)
				return renderdhcp
			}

			// Cycle through the IPAM keys, and determine if the type is dhcp
			if rawConfig["ipam"] != nil {
				ipam, okipamcast := rawConfig["ipam"].(map[string]interface{})
				if !okipamcast {
					log.Printf("WARNING: IPAM element has data of type %T but wanted map[string]interface{}", rawConfig["ipam"])
					continue
				}

				for key, value := range ipam {
					if key == "type" {
						typeval, oktypecast := value.(string)
						if !oktypecast {
							log.Printf("WARNING: IPAM type element has data of type %T but wanted string", value)
							break
						}

						if typeval == "dhcp" {
							renderdhcp = true
							break
						}
					}
				}
			}

			if renderdhcp == true {
				break
			}
		}
	}

	return renderdhcp
}
