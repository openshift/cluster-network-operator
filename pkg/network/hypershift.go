package network

import "os"

var (
	enabled   = os.Getenv("HYPERSHIFT")
	namespace = os.Getenv("HOSTED_CLUSTER_NAMESPACE")
)

type HyperShiftConfig struct {
	Enabled   bool
	Namespace string
}

func NewHyperShiftConfig() HyperShiftConfig {
	return HyperShiftConfig{
		Enabled:   hyperShiftEnabled(),
		Namespace: namespace,
	}
}

func hyperShiftEnabled() bool {
	return enabled == "true"
}
