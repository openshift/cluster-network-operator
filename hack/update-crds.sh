#!/bin/bash
set -euo pipefail

function install_crd {
  local SRC="$1"
  local DST="$2"
  if ! diff -Naup "$SRC" "$DST"; then
    cp "$SRC" "$DST"
    echo "installed CRD: $SRC => $DST"
  fi
}

# Can't rely on associative arrays for old Bash versions (e.g. OSX)
install_crd \
  "vendor/github.com/openshift/api/operator/v1/0000_70_cluster-network-operator_01_crd.yaml" \
  "manifests/0000_70_cluster-network-operator_05_clusteroperator.yaml"

install_crd \
  "vendor/github.com/openshift/api/networkoperator/v1/001-egressrouter.crd.yaml" \
  "manifests/0000_70_cluster-network-operator_01_egr_crd.yaml"
