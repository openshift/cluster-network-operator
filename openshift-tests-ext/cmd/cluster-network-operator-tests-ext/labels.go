package main

import (
	"sort"
	"strings"

	"github.com/openshift-eng/openshift-tests-extension/pkg/util/sets"
)

// getTestExtensionLabels returns labels applied to every spec in this extension.
func getTestExtensionLabels() []string {
	return []string{"sig-network", extensionLabel}
}

// generatePrependedLabelsStr renders Labels as a sorted `[label]` prefix for the
// test name (same approach as ovn-kubernetes OTE). Serial/Disruptive are omitted
// here because AppendedAnnotations places [Serial] (and [Suite:…]) at the end.
func generatePrependedLabelsStr(labels sets.Set[string]) string {
	labelList := labels.UnsortedList()
	sort.Strings(labelList)

	var b strings.Builder
	for _, label := range labelList {
		switch label {
		case "Serial", "Disruptive":
			continue
		}
		b.WriteByte('[')
		b.WriteString(label)
		b.WriteByte(']')
	}
	return b.String()
}
