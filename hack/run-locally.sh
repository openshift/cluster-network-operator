#!/bin/bash 

# run-locally.sh is some scaffolding around running a local instance of the
# network operator with the installer.
# See INSTALLER-HACKING.md

set -o errexit
set -o nounset

# Install our overrides so the cluster doesn't run the network operator.
function override() {
    if [[ ! -e ${CLUSTER_DIR}/manifests/cvo-overrides.yaml ]]; then
        echo "cannot find cvo-overrides.yaml; please run"
        echo "openshift-install --dir=${CLUSTER_DIR} create manifests"
        exit 1
    fi

    # Patch the CVO to not create the network operator
    echo "Applying overrides to ${CLUSTER_DIR}/manifests/cvo-overrides.yaml"

    kubectl --kubeconfig=hack/null-kubeconfig patch --type=json --local=true -f=${CLUSTER_DIR}/manifests/cvo-overrides.yaml -p "$(cat hack/overrides-patch.yaml)" -o yaml > ${CLUSTER_DIR}/manifests/.cvo-overrides.yaml.new

    mv ${CLUSTER_DIR}/manifests/.cvo-overrides.yaml.new ${CLUSTER_DIR}/manifests/cvo-overrides.yaml
}

# Extract the image references from the release image.
function build_env() {
    echo "Writing image references to ${CLUSTER_DIR}/env.sh"
    oc --kubeconfig=hack/null-kubeconfig convert --local=true -f manifests/0000_07_cluster-network-operator_03_daemonset.yaml -ojsonpath='{range .spec.template.spec.containers[0].env[?(@.value)]}{.name}{"="}{.value}{"\n"}' > ${CLUSTER_DIR}/env.sh
}

# start_operator waits for the cluster to come up, then launches
# the operator locally.
function start_operator() {
    export KUBECONFIG=${CLUSTER_DIR}/auth/kubeconfig

    echo "Waiting for $KUBECONFIG"
    while true; do 
        if [[ ! -e ${KUBECONFIG} ]]; then
            sleep 10
        else
            break
        fi
    done

    # A few environment variables we set in the Daemonset
    export POD_NAME=LOCAL
    export KUBERNETES_SERVICE_PORT=6443
    export KUBERNETES_SERVICE_HOST=$(oc config view -o jsonpath='{.clusters[0].cluster.server}' | awk -F'[/:]' '{print $4}')

    echo "Waiting for the apiserver to come up and for the network configuration to be rceated."
    echo "You can ignore the error message 'error: the server doesn't have a resource type \"Network\"'"

    while true; do 
        if oc get Network.config.openshift.io cluster; then
            echo "Network config exists, continuing"
            break
        else
            echo "No network config or apiserver not up yet, retrying"
            sleep 15
            continue
        fi
    done
    echo "Starting operator"
    env $(cat ${CLUSTER_DIR}/env.sh) _output/linux/amd64/cluster-network-operator
}

function usage() {
    >&2 echo "Usage: $0 (prepare|start)

Scaffoding for running a local build of the network operator with the installer.
For more info, see INSTALLER-HACKING.md

Commands:
- prepare: adds overrides to prevent the default network operator from starting, extracts image references
- start: Starts the local network operator

Required environment variables:
CLUSTER_DIR - The state directory used by the installer.
"
}

if [[ -z "${CLUSTER_DIR:-}" ]]; then
    echo error: CLUSTER_DIR is required
    echo
    usage
    exit 1
fi

case "${1:-""}" in
    prepare)
        override;
        build_env;
        ;;
    start)
        if [[ ! -e ${CLUSTER_DIR}/env.sh ]]; then
            echo "env.sh missing, run prepare first"
            exit 1
        fi

        start_operator ${CLUSTER_DIR};
        ;;
    *)
        usage
        exit 1
        ;;
esac
