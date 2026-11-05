#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

HERE="$(dirname "$(readlink --canonicalize "${BASH_SOURCE[0]}")")"
ROOT="$(readlink --canonicalize "$HERE/..")"
TESTS_GO="${ROOT}/test/tests.go"

# Unset KUBECONFIG to ensure complete test discovery
unset KUBECONFIG

echo "🔄 Updating InformingTests / BlockingTests in test/tests.go..." >&2

pushd "$ROOT" > /dev/null
trap 'popd > /dev/null 2>&1' EXIT

echo "  Building test binary (discover-all)..." >&2
CNO_OTE_DISCOVER_ALL=1 "$HERE/build-tests-ext.sh"

echo "  Discovering tests..." >&2
# Capture the full pipeline first: mapfile does not propagate process-substitution
# exit status, so a mid-stream list-tests failure must not rewrite tests.go.
DISCOVERY_OUT="$(mktemp)"
trap 'rm -f "$DISCOVERY_OUT"; popd > /dev/null 2>&1' EXIT
if ! CNO_OTE_DISCOVER_ALL=1 ./bin/cluster-network-operator-tests-ext list tests >"$DISCOVERY_OUT" 2>/dev/null; then
  echo "error: list tests failed; leaving test/tests.go unchanged" >&2
  exit 1
fi
if ! DISCOVERED_NAMES=$(jq -r '.[].name' <"$DISCOVERY_OUT" | sort -u); then
  echo "error: failed to parse list tests output; leaving test/tests.go unchanged" >&2
  exit 1
fi
if [[ -z "$DISCOVERED_NAMES" ]]; then
  echo "error: no tests discovered from list tests; leaving test/tests.go unchanged" >&2
  exit 1
fi
mapfile -t ACTUAL_TESTS <<<"$DISCOVERED_NAMES"

# Preserve BlockingTests membership for names that still exist.
# Supports both `[]string{}` and multi-line `[]string{ ... }` forms, and both
# backtick and double-quoted Go string literals. Fail closed if parsing is unreliable
# so entries are never silently moved to InformingTests.
BLOCKING_PARSE_OUT="$(mktemp)"
trap 'rm -f "$DISCOVERY_OUT" "$BLOCKING_PARSE_OUT"; popd > /dev/null 2>&1' EXIT
if ! python3 - >"$BLOCKING_PARSE_OUT" <<'PY'
import codecs
import re
import sys
from pathlib import Path

text = Path("test/tests.go").read_text()
m = re.search(r"var BlockingTests = \[\]string\{(.*?)\}", text, re.S)
if not m:
    print("error: could not find BlockingTests in test/tests.go", file=sys.stderr)
    sys.exit(1)

body = m.group(1)
stripped = body.strip()
if not stripped:
    sys.exit(0)

# Strip Go comments before collecting string literals so quoted text inside
# comments (e.g. // "spec name") is never treated as a BlockingTests entry.
# Walk the body so // and /* */ inside string literals are preserved.
code_parts = []
i = 0
while i < len(body):
    if body.startswith("//", i):
        nl = body.find("\n", i)
        i = len(body) if nl < 0 else nl
        continue
    if body.startswith("/*", i):
        end = body.find("*/", i + 2)
        if end < 0:
            print(
                "error: unclosed block comment in BlockingTests; "
                "leaving test/tests.go unchanged",
                file=sys.stderr,
            )
            sys.exit(1)
        i = end + 2
        continue
    if body[i] == "`":
        end = body.find("`", i + 1)
        if end < 0:
            print(
                "error: unclosed raw string in BlockingTests; "
                "leaving test/tests.go unchanged",
                file=sys.stderr,
            )
            sys.exit(1)
        code_parts.append(body[i : end + 1])
        i = end + 1
        continue
    if body[i] == '"':
        j = i + 1
        while j < len(body):
            if body[j] == "\\":
                j += 2
                continue
            if body[j] == '"':
                break
            j += 1
        else:
            print(
                "error: unclosed double-quoted string in BlockingTests; "
                "leaving test/tests.go unchanged",
                file=sys.stderr,
            )
            sys.exit(1)
        code_parts.append(body[i : j + 1])
        i = j + 1
        continue
    code_parts.append(body[i])
    i += 1

code = "".join(code_parts)
if not code.strip():
    # Body was only comments/whitespace.
    sys.exit(0)

names = []
for match in re.finditer(r"`([^`]*)`|\"((?:\\.|[^\"\\])*)\"", code):
    if match.group(1) is not None:
        name = match.group(1)
    else:
        name = codecs.decode(match.group(2), "unicode_escape")
    name = name.strip()
    if name:
        names.append(name)

remainder = re.sub(r"`[^`]*`|\"(?:\\.|[^\"\\])*\"", "", code)
remainder = re.sub(r"[\s,]+", "", remainder)
if remainder:
    print(
        f"error: could not reliably parse BlockingTests (leftover {remainder!r}); "
        "leaving test/tests.go unchanged",
        file=sys.stderr,
    )
    sys.exit(1)

if not names:
    print(
        "error: BlockingTests is non-empty but no entries were parsed; "
        "leaving test/tests.go unchanged",
        file=sys.stderr,
    )
    sys.exit(1)

for name in names:
    print(name)
PY
then
  echo "error: failed to parse BlockingTests from test/tests.go; leaving file unchanged" >&2
  exit 1
fi
mapfile -t OLD_BLOCKING <"$BLOCKING_PARSE_OUT"

declare -A ACTUAL_SET=()
for t in "${ACTUAL_TESTS[@]}"; do
  ACTUAL_SET["$t"]=1
done

BLOCKING=()
for t in "${OLD_BLOCKING[@]+"${OLD_BLOCKING[@]}"}"; do
  [[ -z "$t" ]] && continue
  if [[ -n "${ACTUAL_SET[$t]:-}" ]]; then
    BLOCKING+=("$t")
  else
    echo "  dropping orphaned BlockingTest: $t" >&2
  fi
done

declare -A BLOCKING_SET=()
for t in "${BLOCKING[@]+"${BLOCKING[@]}"}"; do
  BLOCKING_SET["$t"]=1
done

INFORMING=()
for t in "${ACTUAL_TESTS[@]}"; do
  if [[ -z "${BLOCKING_SET[$t]:-}" ]]; then
    INFORMING+=("$t")
  fi
done

# Emit gofmt-compliant slice literals: empty -> []string{}, else multi-line.
write_string_slice() {
  local var_name="$1"
  shift
  local -a items=()
  local item
  for item in "$@"; do
    [[ -z "$item" ]] && continue
    items+=("$item")
  done

  if [[ ${#items[@]} -eq 0 ]]; then
    printf 'var %s = []string{}\n' "$var_name"
    return
  fi

  printf 'var %s = []string{\n' "$var_name"
  for item in "${items[@]}"; do
    printf '\t`%s`,\n' "$item"
  done
  printf '}\n'
}

{
  cat <<'EOF'
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
EOF

  write_string_slice "InformingTests" "${INFORMING[@]+"${INFORMING[@]}"}"

  cat <<'EOF'

// BlockingTests lists tests that are considered stable and should block CI jobs
// if they fail.
EOF

  write_string_slice "BlockingTests" "${BLOCKING[@]+"${BLOCKING[@]}"}"
} > "${TESTS_GO}.tmp"

gofmt -w "${TESTS_GO}.tmp"
mv "${TESTS_GO}.tmp" "$TESTS_GO"

echo "  Wrote ${#INFORMING[@]} InformingTests and ${#BLOCKING[@]} BlockingTests" >&2
echo "  ✅ Updated $TESTS_GO" >&2
