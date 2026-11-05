package test

// This list contains separate lists of informing and blocking tests. Tests not
// on either of these lists do not run. To graduate a test from informing to
// blocking:
//  1. Remove the test from InformingTests and add it to BlockingTests
//  2. Rebuild: make -C openshift-tests-ext build
//  3. Verify: ./openshift-tests-ext/bin/cluster-network-operator-tests-ext list tests \
//       | jq -r '.[] | select(.name | contains("substring")) | {name, lifecycle}'
//
// To regenerate InformingTests with newly discovered specs (new tests land in
// InformingTests; existing BlockingTests membership is preserved):
//  make -C openshift-tests-ext update-test-lists
//
// Used by: openshift-tests-ext/cmd/cluster-network-operator-tests-ext/main.go

// InformingTests lists tests that generally pass but are not considered stable
// and should not block CI jobs if they fail.
var InformingTests = []string{
	`[sig-network][cluster-network-operator-ext] CNO [JIRA:Networking][OTP] 51727-ovsdb-server and northd should not core dump on node restart [Suite:cluster-network-operator/conformance/disruptive]`,
	`[sig-network][cluster-network-operator-ext] CNO [JIRA:Networking][OTP] 72028-Join switch IP and management port IP for newly added node should be synced correctly into NBDB, pod on new node can communicate with old pod on old node [Suite:cluster-network-operator/conformance/disruptive]`,
	`[sig-network][cluster-network-operator-ext] CNO [JIRA:Networking][OTP] 72817- internalJoinSubnet and internalTransitSwitchSubnet should be configurable post install as a Day 2 operation [Suite:cluster-network-operator/conformance/disruptive]`,
	`[sig-network][cluster-network-operator-ext] CNO env-overrides _global key applies OVN_KUBE_LOG_LEVEL on all ovnkube-node pods [Suite:cluster-network-operator/conformance/disruptive]`,
	`[sig-network][cluster-network-operator-ext] CNO env-overrides node-specific env-overrides take precedence over _global [Suite:cluster-network-operator/conformance/disruptive]`,
	`[sig-network][cluster-network-operator-ext] CNO env-overrides without _global, node-specific override still applies and other nodes keep default [Suite:cluster-network-operator/conformance/disruptive]`,
}

// BlockingTests lists tests that are considered stable and should block CI jobs
// if they fail.
var BlockingTests = []string{}
