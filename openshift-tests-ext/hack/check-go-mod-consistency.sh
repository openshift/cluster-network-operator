#!/bin/bash
# Verify go / k8s module versions stay in sync between the e2e module and the
# OTE module. When bumping kube deps for tests, bump both go.mod files.
set -euo pipefail

HERE=$(dirname "$(readlink --canonicalize "${BASH_SOURCE[0]}")")
REPO_ROOT=$(readlink --canonicalize "$HERE/../..")
GO_MOD_E2E="${REPO_ROOT}/test/e2e/go.mod"
GO_MOD_OTE="${REPO_ROOT}/openshift-tests-ext/go.mod"

echo "Checking go.mod consistency between test/e2e and openshift-tests-ext..."

GO_E2E=$(grep -E '^go ' "${GO_MOD_E2E}" | awk '{print $2}')
GO_OTE=$(grep -E '^go ' "${GO_MOD_OTE}" | awk '{print $2}')
if [[ "${GO_E2E}" != "${GO_OTE}" ]]; then
	echo "Go versions differ: test/e2e=${GO_E2E}, openshift-tests-ext=${GO_OTE}" >&2
	exit 1
fi

# Require-line versions only (ignore replace staging pins).
for dep in k8s.io/api k8s.io/apimachinery k8s.io/client-go k8s.io/kubernetes; do
	VER_E2E=$(grep -E "^\s+${dep} " "${GO_MOD_E2E}" | head -1 | awk '{print $2}')
	VER_OTE=$(grep -E "^\s+${dep} " "${GO_MOD_OTE}" | head -1 | awk '{print $2}')
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
