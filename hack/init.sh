#!/bin/bash

eval $(go env | grep -e "GOHOSTOS" -e "GOHOSTARCH")

export GOOS=${GOOS:-${GOHOSTOS}}
export GOARCH=${GOACH:-${GOHOSTARCH}}
OUT_DIR=${OUT_DIR:-_output}

function setup_env() {
  local init_source="$( dirname "${BASH_SOURCE}" )/.."
  CNO_ROOT="$( absolute_path "${init_source}" )"
  export CNO_ROOT
  CNO_GO_PKG="github.com/openshift/cluster-network-operator"
  export CNO_GO_PKG

  if [[ -z "${GOPATH+a}" ]]; then
    unset GOBIN
    # create a local GOPATH in _output
    GOPATH="${CNO_ROOT}/${OUT_DIR}/go"
    local go_pkg_dir="${GOPATH}/src/${CNO_GO_PKG}"

    mkdir -p "$(dirname "${go_pkg_dir}")"
    rm -f "${go_pkg_dir}"
    ln -s "${CNO_ROOT}" "${go_pkg_dir}"

    export GOPATH
  fi

  if [[ -z "${CNO_BIN_PATH+a}" ]]; then
    export CNO_BIN_PATH="${CNO_ROOT}/${OUT_DIR}/${GOOS}/${GOARCH}"
  fi
  mkdir -p "${CNO_BIN_PATH}"
}
readonly -f setup_env

# absolute_path returns the absolute path to the directory provided
function absolute_path() {
        local relative_path="$1"
        local absolute_path

        pushd "${relative_path}" >/dev/null
        relative_path="$( pwd )"
        if [[ -h "${relative_path}" ]]; then
                absolute_path="$( readlink "${relative_path}" )"
        else
                absolute_path="${relative_path}"
        fi
        popd >/dev/null

	echo ${absolute_path}
}
readonly -f absolute_path
