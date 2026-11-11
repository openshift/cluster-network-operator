package main

import (
	"github.com/openshift/cluster-network-operator/openshift-tests-ext/test/annotate"
)

func main() {
	annotate.Run(annotate.LabelToTestNameMatchMaps, annotate.LabelToLabelMaps, func(_ string) bool {
		return false
	})
}
