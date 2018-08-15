#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

vendor/k8s.io/code-generator/generate-groups.sh \
deepcopy \
github.com/openshift/openshift-network-operator/pkg/generated \
github.com/openshift/openshift-network-operator/pkg/apis \
networkoperator:v1 \
--go-header-file "./tmp/codegen/boilerplate.go.txt"
