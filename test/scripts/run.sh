#!/usr/bin/env bash
# Run native CNO Ginkgo e2e specs against a live cluster.
# Intended for local authoring; CI uses openshift-tests-ext (OTE).
#
# Usage:
#   export KUBECONFIG=/path/to/kubeconfig   # required unless ~/.kube/config exists
#   ./test/scripts/run.sh                   # all specs
#   ./test/scripts/run.sh env-overrides     # focus substring (or FOCUS=...)
#   LABEL_FILTER='!Disruptive' ./test/scripts/run.sh
#   make -C test test-e2e FOCUS=env-overrides
#
# Does not assume Kind; any reachable OpenShift cluster with OVN-Kubernetes works.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
E2E_DIR="${SCRIPT_DIR}/../e2e"

if [[ -z "${KUBECONFIG:-}" ]]; then
	if [[ -f "${HOME}/.kube/config" ]]; then
		export KUBECONFIG="${HOME}/.kube/config"
	else
		echo "error: set KUBECONFIG to an OpenShift cluster kubeconfig" >&2
		exit 1
	fi
fi

if [[ ! -f "${KUBECONFIG}" ]]; then
	echo "error: KUBECONFIG file not found: ${KUBECONFIG}" >&2
	exit 1
fi

FOCUS="${FOCUS:-${1:-.}}"
LABEL_FILTER="${LABEL_FILTER:-}"
# Disruptive specs restart ovnkube-node one-by-one; keep headroom above Ginkgo timeout.
TEST_TIMEOUT="${TEST_TIMEOUT:-90}"
GO_TEST_TIMEOUT=$((TEST_TIMEOUT + 5))

cd "${E2E_DIR}"

args=(
	-test.timeout "${GO_TEST_TIMEOUT}m"
	-v
	.
	-ginkgo.v
	-ginkgo.focus "${FOCUS}"
	-ginkgo.fail-on-empty
	-ginkgo.timeout "${TEST_TIMEOUT}m"
)

if [[ -n "${LABEL_FILTER}" ]]; then
	args+=(-ginkgo.label-filter "${LABEL_FILTER}")
fi

if [[ -n "${E2E_REPORT_DIR:-}" ]]; then
	mkdir -p "${E2E_REPORT_DIR}"
	args+=(-ginkgo.junit-report "${E2E_REPORT_DIR}/junit_cno_e2e.xml")
fi

# Framework flags (same shape as ovn-kubernetes test/scripts/e2e-cp.sh).
args+=(-provider skeleton -kubeconfig "${KUBECONFIG}")

exec go test "${args[@]}"
