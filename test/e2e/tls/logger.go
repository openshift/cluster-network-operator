package tls

import (
	"k8s.io/klog/v2"
)

// LogStep outputs a major test step for visibility in test reports
func LogStep(description string) {
	klog.Infof("TEST STEP: %s", description)
}
