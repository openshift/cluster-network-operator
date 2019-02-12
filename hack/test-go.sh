#!/bin/bash

source "$(dirname "${BASH_SOURCE}")/init.sh"

# Check for `go` binary and set ${GOPATH}.
setup_env

cd ${GOPATH}/src/${CNO_GO_PKG}

if [ -z "$PKGS" ]; then
  # by default, test everything that's not in vendor
  PKGS="$(go list -f '{{if len .TestGoFiles}} {{.ImportPath}} {{end}}' ./...)"
fi

CGO_ENABLED=0 go test "${goflags[@]:+${goflags[@]}}" ${PKGS}
retcode=$?

exit $retcode
