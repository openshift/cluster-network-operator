#!/usr/bin/env bash
# Run enabled OTE specs by name or substring.
#
# Usage:
#   KUBECONFIG=... ./hack/run-e2e.sh 'env-overrides'
#   KUBECONFIG=... NAMES='env-overrides' ./hack/run-e2e.sh
#
# Substrings are expanded via `list tests` because run-test requires exact names.
# Each match runs as its own `run-test -n …` process: passing multiple names in
# one invocation makes the extension re-exec with the original Ginkgo text
# (without [sig-network]/[Suite:…] rewriting), which then fails lookup.
# Stdin is closed per invocation because run-test treats a non-TTY stdin as
# extra names.

set -o errexit
set -o nounset
set -o pipefail

HERE="$(dirname "$(readlink --canonicalize "${BASH_SOURCE[0]}")")"
ROOT="$(readlink --canonicalize "$HERE/..")"
BINARY="${ROOT}/bin/cluster-network-operator-tests-ext"

NAMES="${1:-${NAMES:-}}"

if [[ -z "${KUBECONFIG:-}" ]]; then
  echo "error: set KUBECONFIG" >&2
  exit 1
fi
if [[ -z "${NAMES}" ]]; then
  echo "error: pass a test name/substring as argv[1] or set NAMES=..." >&2
  exit 1
fi
if [[ ! -x "${BINARY}" ]]; then
  echo "error: missing ${BINARY}; run 'make -C openshift-tests-ext build' first" >&2
  exit 1
fi

names_file="$(mktemp)"
trap 'rm -f "${names_file}"' EXIT

"${BINARY}" list tests \
  | jq -r --arg q "${NAMES}" '.[] | select(.name | contains($q)) | .name' \
  > "${names_file}"

if [[ ! -s "${names_file}" ]]; then
  echo "error: no tests matched NAMES='${NAMES}'" >&2
  exit 1
fi

echo "Running $(wc -l < "${names_file}") test(s) matching '${NAMES}':" >&2
sed 's/^/  /' "${names_file}" >&2

while IFS= read -r name; do
  echo "=== ${name} ===" >&2
  "${BINARY}" run-test -n "${name}" </dev/null
done < "${names_file}"
