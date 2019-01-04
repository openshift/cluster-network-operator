#!/usr/bin/env bash

set -eu

source "$(dirname "${BASH_SOURCE}")/init.sh"
setup_env

CMDS=${CMDS:-cluster-network-operator cluster-network-renderer}
GOFLAGS=${GOFLAGS:-}
GLDFLAGS=${GLDFLAGS:-}

# Go to the root of the repo
cd "${CNO_ROOT}"

if [ -z ${VERSION+a} ]; then
	echo "Using version from git..."
	VERSION=$(git describe --abbrev=8 --dirty --always)
fi

GLDFLAGS+="-X ${CNO_GO_PKG}/pkg/version.Raw=${VERSION}"

eval $(go env)

for cmd in ${CMDS}; do
	echo "Building ${CNO_GO_PKG}/cmd/${cmd} (${VERSION})"
	CGO_ENABLED=0 GOOS=${GOOS} GOARCH=${GOARCH} go build ${GOFLAGS} -ldflags "${GLDFLAGS}" -o ${CNO_BIN_PATH}/${cmd} ${CNO_GO_PKG}/cmd/${cmd}
done
