#!/bin/bash

source "$(dirname "${BASH_SOURCE}")/init.sh"

# Check for `go` binary and set ${GOPATH}.
setup_env

cd ${GOPATH}/src/${CNO_GO_PKG}

if [[ ! $(which go) ]]; then
  echo "go not found on PATH. To install:"
  echo "https://golang.org/dl/"
  exit 1
fi
if [[ ! $(which gosec) ]]; then
  echo "gosec not found on PATH. To install:"
  echo "go get -u github.com/securego/gosec/cmd/gosec"
  exit 1
fi

gosec -severity medium --confidence medium \
    -exclude G304 \
    -quiet ./...
retcode=$?

exit $retcode
