#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

HERE="$(dirname "$(readlink --canonicalize "${BASH_SOURCE[0]}")")"
ROOT="$(readlink --canonicalize "$HERE/..")"

# Unset KUBECONFIG to ensure complete test discovery
# (test binary filters based on cluster features when kubeconfig is present)
unset KUBECONFIG

echo "🔍 Validating test lists..." >&2

pushd "$ROOT" > /dev/null
trap 'popd > /dev/null 2>&1' EXIT

# Build the test extension binary with discover-all so unlisted Ginkgo specs appear.
echo "  Building test binary..." >&2
if ! CNO_OTE_DISCOVER_ALL=1 "$HERE/build-tests-ext.sh" > /dev/null 2>&1; then
  echo "  error: failed to build test binary" >&2
  exit 1
fi

echo "  Discovering actual tests..." >&2
if ! ACTUAL_TESTS=$(CNO_OTE_DISCOVER_ALL=1 ./bin/cluster-network-operator-tests-ext list tests 2>/dev/null | jq -r '.[].name' | sort); then
  echo "  error: could not discover tests" >&2
  exit 1
fi

if [[ -z "$ACTUAL_TESTS" ]]; then
  echo "  error: could not discover tests" >&2
  exit 1
fi

echo "  Extracting expected tests from tests.go..." >&2
if ! EXPECTED_TESTS=$(go run ./cmd/list-test-names/main.go | sort); then
  echo "  error: failed to extract expected tests from tests.go" >&2
  exit 1
fi

# Find orphaned tests (in tests.go but don't exist anymore)
ORPHANED=$(comm -13 <(echo "$ACTUAL_TESTS") <(echo "$EXPECTED_TESTS") || true)

# Find new tests (exist but not in tests.go)
NEW_TESTS=$(comm -23 <(echo "$ACTUAL_TESTS") <(echo "$EXPECTED_TESTS") || true)

if [[ -n "$ORPHANED" ]]; then
  echo "ORPHANED_TESTS_START"
  echo "$ORPHANED"
  echo "ORPHANED_TESTS_END"
fi

if [[ -n "$NEW_TESTS" ]]; then
  echo "NEW_TESTS_START"
  echo "$NEW_TESTS"
  echo "NEW_TESTS_END"
fi

if [[ -n "$ORPHANED" ]] || [[ -n "$NEW_TESTS" ]]; then
  echo "" >&2
  echo "  ⚠️  Test list validation failed" >&2
  [[ -n "$ORPHANED" ]] && echo "  Found $(echo "$ORPHANED" | wc -l) orphaned test(s)" >&2
  [[ -n "$NEW_TESTS" ]] && echo "  Found $(echo "$NEW_TESTS" | wc -l) new test(s)" >&2
  echo "  Run: make -C openshift-tests-ext update-test-lists" >&2
  exit 1
fi

echo "  ✅ Test lists are in sync" >&2
exit 0
