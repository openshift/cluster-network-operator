#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_ROOT="$(dirname "${BASH_SOURCE[0]}")/.."

if  ! ( command -v controller-gen > /dev/null ); then
  # Need to take an unreleased version of controller-tools
  # because of controller-tools bug 302
  echo "controller-gen not found, installing sigs.k8s.io/controller-tools@83f6193..."
  olddir="${PWD}"
  builddir="$(mktemp -d)"
  cd "${builddir}"
  GO111MODULE=on go get -u sigs.k8s.io/controller-tools/cmd/controller-gen@83f6193
  cd "${olddir}"
  if [[ "${builddir}" == /tmp/* ]]; then #paranoia
      rm -rf "${builddir}"
  fi
fi

bash "${SCRIPT_ROOT}/vendor/k8s.io/code-generator/generate-groups.sh" deepcopy \
  github.com/openshift/openshift-network-operator/pkg/generated github.com/openshift/cluster-network-operator/pkg/apis \
  "network:v1" \
  --go-header-file "${SCRIPT_ROOT}/hack/custom-boilerplate.go.txt"


echo "Generating CRDs"
mkdir -p _output/crds
controller-gen crd paths=./pkg/apis/... output:crd:dir=_output/crds
cp _output/crds/network.operator.openshift.io_operatorpkis.yaml manifests/0000_70_cluster-network-operator_01_pki_crd.yaml
