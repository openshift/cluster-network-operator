#!/bin/bash
set -euo pipefail

function verify_crd {
  local SRC="$1"
  local DST="$2"
  if ! diff -Naup "$SRC" "$DST"; then
    echo "invalid CRD: $SRC => $DST"
    exit 1
  fi
}

verify_crd \
   "vendor/github.com/openshift/api/operator/v1/0000_70_cluster-network-operator_01_crd.yaml" \
  "manifests/0000_70_cluster-network-operator_05_clusteroperator.yaml"

verify_crd \
  "vendor/github.com/openshift/api/networkoperator/v1/001-egressrouter.crd.yaml" \
  "manifests/0000_70_cluster-network-operator_01_egr_crd.yaml"
