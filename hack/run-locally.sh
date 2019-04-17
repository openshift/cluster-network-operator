#!/bin/bash 

# run-locally.sh is some scaffolding around running a local instance of the
# network operator with the installer.
# See INSTALLER-HACKING.md

set -o errexit
set -o nounset

if [[ -n "${HACK_MINIMIZE:-}" ]]; then
    echo "HACK_MINIMIZE set! This is only for development, and the installer will likely not succeed."
    echo "You will still be left with a reasonably functional cluster."
    echo ""
fi

# Install our overrides so the cluster doesn't run the network operator.
function override_install_manifests() {
    # Patch the CVO to not create the network operator
    kubectl --kubeconfig=hack/null-kubeconfig patch --type=json --local=true -f="${CLUSTER_DIR}/manifests/cvo-overrides.yaml" -p "$(cat hack/overrides-patch.yaml)" -o yaml > "${CLUSTER_DIR}/manifests/.cvo-overrides.yaml.new"
    # Optionally, tell the CVO to skip some unnecessary components
    if [[ -n "${HACK_MINIMIZE:-}" ]]; then
        kubectl --kubeconfig=hack/null-kubeconfig patch --type=json --local=true -f="${CLUSTER_DIR}/manifests/.cvo-overrides.yaml.new" -p "$(cat hack/overrides-minimize-patch.yaml)" -o yaml > "${CLUSTER_DIR}/manifests/.cvo-overrides.yaml.new.2"
        mv "${CLUSTER_DIR}/manifests/.cvo-overrides.yaml.new.2" "${CLUSTER_DIR}/manifests/.cvo-overrides.yaml.new"
    fi

    if ! cmp -s "${CLUSTER_DIR}/manifests/.cvo-overrides.yaml.new" "${CLUSTER_DIR}/manifests/cvo-overrides.yaml"; then
        echo "Applying overrides to ${CLUSTER_DIR}/manifests/cvo-overrides.yaml"
        mv "${CLUSTER_DIR}/manifests/.cvo-overrides.yaml.new" "${CLUSTER_DIR}/manifests/cvo-overrides.yaml"
    else
        rm -f "${CLUSTER_DIR}/manifests/.cvo-overrides.yaml.new"
    fi
}

# Install our overrides in to an existing cluster
function stop_deployed_operator() {
    echo "Telling the CVO to ignore the network operator"
    kubectl patch --type=json -p "$(cat hack/overrides-patch.yaml)" clusterversion version

    echo "Scaling the deployed network operator to 0"
    kubectl scale deployment -n openshift-network-operator network-operator --replicas 0
}

# Extract environment variables for the CNO DaemonSet
function setup_operator_env() {
    if [[ manifests/0000_07_cluster-network-operator_03_daemonset.yaml -nt "${CLUSTER_DIR}/env.sh" ]]; then
        echo "Copying environment variables from manifest to ${CLUSTER_DIR}/env.sh"
        oc --kubeconfig=hack/null-kubeconfig convert --local=true -f manifests/0000_07_cluster-network-operator_03_daemonset.yaml -ojsonpath='{range .spec.template.spec.containers[0].env[?(@.value)]}{.name}{"="}{.value}{"\n"}' > "${CLUSTER_DIR}/env.sh"
    fi
}

# Extract environment variables from an already-deployed cluster
function setup_operator_env_running() {
    if [[ ! -e "${CLUSTER_DIR}/env.sh" ]]; then
        echo "Copying environment variables from manifest to ${CLUSTER_DIR}/env.sh"
        kubectl get deployment -n openshift-network-operator network-operator -ojsonpath='{range .spec.template.spec.containers[0].env[?(@.value)]}{.name}{"="}{.value}{"\n"}' > "${CLUSTER_DIR}/env.sh"
    fi
}

# wait_for_cluster waits for the cluster to come up
function wait_for_cluster() {
    export KUBECONFIG="${CLUSTER_DIR}/auth/kubeconfig"

    if [[ ! -e "${KUBECONFIG}" ]]; then
        echo "Waiting for installer to create a kubeconfig..."
        while [[ ! -e "${KUBECONFIG}" ]]; do
            sleep 5
        done
        echo "Found ${KUBECONFIG}"
    fi

    # A few environment variables we set in the Daemonset
    export POD_NAME=LOCAL
    export KUBERNETES_SERVICE_PORT=6443
    export KUBERNETES_SERVICE_HOST=$(oc config view -o jsonpath='{.clusters[0].cluster.server}' | awk -F'[/:]' '{print $4}')

    if ! getent ahosts ${KUBERNETES_SERVICE_HOST} >& /dev/null; then
        echo "Waiting for installer to create apiserver..."
        while ! getent ahosts ${KUBERNETES_SERVICE_HOST} >& /dev/null; do
            sleep 5
        done
        echo "Found ${KUBERNETES_SERVICE_HOST}"
    fi

    if ! oc get namespace openshift-network-operator >& /dev/null; then
        echo "Waiting for installer to create openshift-network-operator namespace..."
        while ! oc get namespace openshift-network-operator >& /dev/null; do
            sleep 5
        done
        echo "Found openshift-network-operator namespace"
    fi

    if ! oc get Network.config.openshift.io cluster >& /dev/null; then
        echo "Waiting for installer to create network operator configuration..."
        while ! oc get Network.config.openshift.io cluster >& /dev/null; do
            sleep 5
        done
        echo "Found network operator configuration"
        echo ""
        echo "Ready to start operator. (If this fails, try running '$0' again.)"
    fi
}

if [[ -z "${CLUSTER_DIR:-}" ]]; then
    echo "error: CLUSTER_DIR must be set"
    echo "For more info, see INSTALLER-HACKING.md"
    exit 1
fi

# If the cluster doesn't exist yet, prepare the manifests.
if [[ -z "${ATTACH_RUNNING:-}" ]]; then
    if [[ ! -e "${CLUSTER_DIR}/env.sh" && ! -e "${CLUSTER_DIR}/manifests/cvo-overrides.yaml" ]]; then
        echo "error: cannot find installer state; please run"
        echo "  openshift-install --dir=${CLUSTER_DIR} create manifests"
        echo "For more info, see INSTALLER-HACKING.md"
        exit 1
    fi

    if [[ -e "${CLUSTER_DIR}/manifests/cvo-overrides.yaml" ]]; then
        override_install_manifests
    fi

    setup_operator_env
fi

wait_for_cluster

if [[ ! -z "${ATTACH_RUNNING:-}" ]]; then
    setup_operator_env_running
    stop_deployed_operator
fi

env $(cat "${CLUSTER_DIR}/env.sh") _output/linux/amd64/cluster-network-operator
