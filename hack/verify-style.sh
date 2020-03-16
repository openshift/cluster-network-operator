#!/usr/bin/env bash
#
# This script invokes tools that should be run prior to pushing
# a repo, such as linters. This is designed to prevent running
# CI on code that will have to be changed.

set -uo pipefail

if [[ ! $(which go) ]]; then
  echo "go not found on PATH. To install:"
  echo "https://golang.org/dl/"
  exit 1
fi
if [[ ! $(which dep) ]]; then
  echo "dep not found on PATH. To install:"
  echo "curl https://raw.githubusercontent.com/golang/dep/master/install.sh | sh"
  exit 1
fi
if [[ ! $(which golangci-lint) ]]; then
  echo "golangci-lint not found on PATH. To install:"
  echo "curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(go env GOPATH)/bin $(cat .golangciversion)"
  exit 1
fi
rc=0
trap 'rc=$?' ERR

# Go to the root of the repo
cd "$(git rev-parse --show-cdup)"

GOFILES=$(find . -path ./vendor -prune -o -name '*.go' | grep -v vendor | grep -v pkg/operator/assets)
GOPKGS=$(go list ./... | grep -v '/vendor/' | grep -v '/generated/' | grep -v pkg/operator/assets)

echo "Running gofmt..."
fmt_files=$(gofmt -l -s $GOFILES)
if [[ -n ${fmt_files} ]]; then
	echo "gofmt failed. fix with"
	echo gofmt -w -s $fmt_files
    rc=1
fi

echo "Running golangci-lint..."
golangci-lint run

echo "Running dep check..."
dep check

echo "Done!"
exit ${rc}
