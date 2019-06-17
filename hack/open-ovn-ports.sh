#!/bin/bash

set -o errexit
set -o nounset

function get_group_id() {
    name="$1"

    id=$(aws --output json ec2 describe-security-groups --filters="Name=tag:Name,Values=${name}" | \
	     jq -r .SecurityGroups[0].GroupId)
    if [[ "${id}" == "null" ]]; then
	echo "error: security group '${name}' does not (yet?) exist" 1>&2
	exit 1
    fi
    echo "${id}"
}

function open_port() {
    src_group="$1"
    dest_group="$2"
    protocol="$3"
    port="$4"

    aws ec2 authorize-security-group-ingress --group-id "${dest_group}" \
	--source-group "${src_group}" --protocol "${protocol}" --port "${port}" \
	|| true # if we got this far, the only likely error is "acl already exists"
}

if [[ -z "${CLUSTER_DIR:-}" ]]; then
    echo "error: CLUSTER_DIR must be set" 1>&2
    exit 1
fi

if [[ ! -f "${CLUSTER_DIR}/metadata.json" ]]; then
    echo "error: Could not find ${CLUSTER_DIR}/metadata.json" 1>&2
    exit 1
fi

if ! jq -e .aws  "${CLUSTER_DIR}/metadata.json" >/dev/null; then
    echo "error: not using AWS. Don't know how to open OVN ports" 1>&2
    exit 1
fi

id=$(jq -r .infraID "${CLUSTER_DIR}/metadata.json")
masters=$(get_group_id "${id}-master-sg")
workers=$(get_group_id "${id}-worker-sg")

# Open northd and southd ports from masters and workers to masters. (The CLI doesn't
# directly support port ranges so it's easiest to just open two single ports.)
open_port "${masters}" "${masters}" tcp 6641
open_port "${masters}" "${masters}" tcp 6642
open_port "${workers}" "${masters}" tcp 6641
open_port "${workers}" "${masters}" tcp 6642

# Open GENEVE port from masters and workers to masters and workers
open_port "${masters}" "${masters}" udp 6081
open_port "${masters}" "${workers}" udp 6081
open_port "${workers}" "${masters}" udp 6081
open_port "${workers}" "${workers}" udp 6081
