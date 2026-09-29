#!/bin/bash
set -e

HERE=$(dirname "$(readlink --canonicalize "${BASH_SOURCE[0]}")")
ROOT=$(readlink --canonicalize "$HERE/..")
OUTPUT="${ROOT}/bin"
mkdir -vp "$OUTPUT"

pushd "$ROOT"
trap popd EXIT

echo "Adding vendor files to tests extension"
go mod vendor
echo "Building CNO tests extension binary"
CGO_ENABLED=0 go build -v \
    -o "${OUTPUT}/cluster-network-operator-tests-ext" \
    -mod=vendor \
    "$ROOT/ote/cmd/"
