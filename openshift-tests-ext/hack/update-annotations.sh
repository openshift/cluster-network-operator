#!/usr/bin/env bash
set -o errexit
set -o nounset
set -o pipefail

HERE="$(dirname "$(readlink --canonicalize "${BASH_SOURCE[0]}")")"
ROOT="$(readlink --canonicalize "$HERE/..")"

"${HERE}/check-go-mod-consistency.sh"

pushd "$ROOT" > /dev/null
trap 'popd > /dev/null 2>&1' EXIT

echo "🔄 Regenerating test/generated/zz_generated.annotations.go..." >&2
mkdir -p "${ROOT}/test/generated"
# Ensure deps (including test/e2e replace) are available for go generate.
go mod vendor
go generate -mod=vendor "${ROOT}/test/e2e_test.go"
echo "  ✅ Updated ${ROOT}/test/generated/zz_generated.annotations.go" >&2
