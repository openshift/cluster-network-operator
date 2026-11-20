#!/bin/bash
# Verify go / k8s module versions stay in sync between the e2e module and the
# OTE module. When bumping kube deps for tests, bump both go.mod files.
set -euo pipefail

HERE=$(dirname "$(readlink --canonicalize "${BASH_SOURCE[0]}")")
REPO_ROOT=$(readlink --canonicalize "$HERE/../..")
GO_MOD_E2E="${REPO_ROOT}/test/e2e/go.mod"
GO_MOD_OTE="${REPO_ROOT}/openshift-tests-ext/go.mod"

mod_go_version() {
	grep -E '^go ' "$1" | awk '{print $2}'
}

# Prefer an explicit replace directive when present (common for k8s staging).
mod_dep_version() {
	local file=$1
	local path=$2
	local ver
	ver=$(grep -E "^\s+${path} => " "$file" | head -1 | awk '{print $NF}' || true)
	if [[ -z "${ver}" ]]; then
		ver=$(grep -E "^\s+${path} " "$file" | head -1 | awk '{print $2}' || true)
	fi
	echo "${ver}"
}

echo "Checking go.mod consistency between test/e2e and openshift-tests-ext..."

GO_E2E=$(mod_go_version "${GO_MOD_E2E}")
GO_OTE=$(mod_go_version "${GO_MOD_OTE}")
if [[ "${GO_E2E}" != "${GO_OTE}" ]]; then
	echo "Go versions differ: test/e2e=${GO_E2E}, openshift-tests-ext=${GO_OTE}" >&2
	exit 1
fi

for dep in k8s.io/api k8s.io/apimachinery k8s.io/client-go k8s.io/kubernetes; do
	VER_E2E=$(mod_dep_version "${GO_MOD_E2E}" "${dep}")
	VER_OTE=$(mod_dep_version "${GO_MOD_OTE}" "${dep}")
	if [[ -z "${VER_E2E}" || -z "${VER_OTE}" ]]; then
		echo "Missing ${dep} in one of the modules: test/e2e=${VER_E2E:-<none>}, openshift-tests-ext=${VER_OTE:-<none>}" >&2
		exit 1
	fi
	if [[ "${VER_E2E}" != "${VER_OTE}" ]]; then
		echo "${dep} versions differ: test/e2e=${VER_E2E}, openshift-tests-ext=${VER_OTE}" >&2
		exit 1
	fi
done

echo "Finished checking go.mod consistency."
