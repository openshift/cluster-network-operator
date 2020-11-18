#!/bin/bash

source "$(dirname "${BASH_SOURCE}")/init.sh"

# Check for `go` binary and set ${GOPATH}.
setup_env

cd ${GOPATH}/src/${CNO_GO_PKG}

PKGS=${PKGS:-./cmd/... ./pkg/...}

CGO_ENABLED=0 go test "${goflags[@]:+${goflags[@]}}" ${PKGS}
retcode=$?

exit $retcode
