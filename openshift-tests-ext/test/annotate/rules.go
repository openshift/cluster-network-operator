package annotate

import (
	// Ensure all native CNO e2e specs are loaded into the Ginkgo tree for annotation.
	_ "github.com/openshift/cluster-network-operator/test/e2e"
)

var (
	// LabelToLabelMaps maps an annotation token onto Ginkgo Labels that should
	// trigger appending that token (same idea as ovn-kubernetes rules.go).
	LabelToLabelMaps = map[string][]string{
		"[Disabled:Unimplemented]": {},
	}

	// LabelToTestNameMatchMaps maps an annotation token onto name substrings /
	// regexes. Suite selection is also driven by Ginkgo Labels (see LabelToLabelMaps).
	LabelToTestNameMatchMaps = map[string][]string{
		"[Serial]": {},
		"[Disruptive]": {
			"[JIRA:Networking][OTP]",
			"TLS Profile Compliance",
			"env-overrides",
		},
	}

	// ExcludedTests skips suite annotation when the built name already matches.
	ExcludedTests = []string{
		`\[Disabled:`,
		`\[Skipped\]`,
		`\[Slow\]`,
		`\[Flaky\]`,
		`\[Local\]`,
	}
)
