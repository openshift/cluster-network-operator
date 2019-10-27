#!/bin/bash 

# run-locally.sh is some scaffolding around running a local instance of the
# network operator with the installer.
# See HACKING.md

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

# Extract environment from a running cluster
function extract_environment_from_running_cluster() {
    if [[ ! -e "${CLUSTER_DIR}/env.sh" ]]; then
        echo "Copying environment variables from manifest to ${CLUSTER_DIR}/env.sh"
        kubectl get deployment -n openshift-network-operator network-operator -ojsonpath='{range .spec.template.spec.containers[0].env[?(@.value)]}{.name}{"="}{.value}{"\n"}' > "${CLUSTER_DIR}/env.sh"
    fi
}

# Extract environment from the installer's cluster-network-operator manifests
function extract_environment_from_manifests() {
    if [[ manifests/0000_70_cluster-network-operator_03_deployment.yaml -nt "${CLUSTER_DIR}/env.sh" ]]; then
        echo "Copying environment variables from manifest to ${CLUSTER_DIR}/env.sh"
        oc --kubeconfig=hack/null-kubeconfig patch --local=true -f manifests/0000_70_cluster-network-operator_03_deployment.yaml -p '{}' -ojsonpath='{range .spec.template.spec.containers[0].env[?(@.value)]}{.name}{"="}{.value}{"\n"}' > "${CLUSTER_DIR}/env.sh"
    fi
}

# Update cluster-network-operator environment variables
function setup_operator_env() {
    local image_env_key="$1"
    local plugin_image="$2"

    sed -i -e "s/^RELEASE_VERSION=.*/RELEASE_VERSION=${RELEASE_VERSION}/" "${CLUSTER_DIR}/env.sh"

    if [[ -n "${plugin_image}" ]]; then
        sed -i -e "s#^${image_env_key}=.*#${image_env_key}=${plugin_image}#" "${CLUSTER_DIR}/env.sh"
    fi
}

# Patch the CNO out of the cluster-version-operator and scale it down to zero so
# we can run our local CNO instead
function stop_deployed_operator() {
    kubectl patch --type=json -p "$(cat hack/overrides-patch.yaml)" clusterversion version

    if [[ -n "$(kubectl get deployments -n openshift-network-operator 2> /dev/null | grep network-operator)" ]]; then
        echo "Scaling the deployed network operator to 0"
        kubectl scale deployment -n openshift-network-operator network-operator --replicas 0
    fi
}

# wait_for_cluster waits for the cluster to come up and sets some variables
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

    if ! oc get clusterversion version >& /dev/null; then
        echo "Waiting for cluster-version-operator to start..."
    fi
    # The object gets created first and then populated later, so the "oc get" may succeed
    # but return an empty string. (But not for long.)
    while RELEASE_VERSION="$(oc get clusterversion version -o jsonpath='{.status.desired.version}' 2>/dev/null)"; test -z "${RELEASE_VERSION}"; do
        sleep 5
    done
    echo "Cluster version is ${RELEASE_VERSION}"

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

# Copy an existing install-config into CLUSTER_DIR, or create a new one there
function ensure_install_config() {
    if [[ -z "${INSTALL_CONFIG}" ]]; then
        # Create a new install-config.yaml if none was given and none exists in CLUSTER_DIR
        if [[ ! -f "${CLUSTER_DIR}/install-config.yaml" ]]; then
            echo "Creating install config..."
            "${INSTALLER_PATH}" --dir="${CLUSTER_DIR}" create install-config
        fi
    else
        cp "${INSTALL_CONFIG}" "${CLUSTER_DIR}/install-config.yaml"
    fi
    # since the install-config is consumed by the installer, back it up for
    # subsequent re-use
    cp "${CLUSTER_DIR}/install-config.yaml" "${CLUSTER_DIR}/install-config.yaml.bak.$(date '+%s')"
}

# Create installer manifests and substitute the requested network plugin
function create_manifests_with_network_plugin() {
    sed -i -e "s/networkType: [a-zA-Z]*$/networkType: $NETWORK_PLUGIN/" "${CLUSTER_DIR}/install-config.yaml"
    echo "Creating install manifests..."
    "${INSTALLER_PATH}" --dir="${CLUSTER_DIR}" create manifests
}

# Run the installer to create a new cluster
function create_cluster() {
    "${INSTALLER_PATH}" --dir="${CLUSTER_DIR}" create cluster &
    ID=$!
    echo "Started cluster installer PID ${ID}"
    trap "trap - TERM && kill -- -$$" INT TERM EXIT
}

function print_usage() {
    >&2 echo "Usage: $0 [-c <cluster-dir>] [options]

$0 detects whether the cluster given by the '-c' option does not yet exist or
is already running. If it does not yet exist the cluster will be created using
the options supplied below. If it is already running, any running
cluster-network-operator will be stopped, plugin image overrides applied, and
a local cluster-network-operator started.

The following options are accepted for both new and existing clusters:

 -c DIR           the cluster installation temporary directory (can also be given via CLUSTER_DIR); required
 -m IMAGE         custom network plugin container image name

The following options are always accepted but only used for new clusters:

 -f CONFIG        the path to an openshift-install-created install-config.yaml file; if not given one will be created
 -i INSTALLER     path to the openshift-install binary; if not given PATH will be searched
 -n PLUGIN        the name of the network plugin to deploy; one of [sdn|OpenShiftSDN|ovn|OVNKubernetes]
 -w               pause after creating manifests to allow manual overrides

The following environment variables are honored:
 - CLUSTER_DIR: the cluster installation temp directory
 - INSTALLER_PATH: the path to the openshift-install binary for 'new' mode
 - INSTALL_CONFIG: the path to the install-config.yaml file for 'new' mode
"
}

PLUGIN_IMAGE="${PLUGIN_IMAGE:-}"
NETWORK_PLUGIN="OpenShiftSDN"
IMAGE_ENV_KEY="SDN_IMAGE"
CLUSTER_DIR="${CLUSTER_DIR:-}"
INSTALLER_PATH="${INSTALLER_PATH:-}"
INSTALL_CONFIG="${INSTALL_CONFIG:-}"
WAIT_FOR_MANIFEST_UPDATES="${WAIT_FOR_MANIFEST_UPDATES:-}"

while getopts "c:f:i:m:n:w" opt; do
    case $opt in
        c) CLUSTER_DIR="${OPTARG}";;
        f) INSTALL_CONFIG="${OPTARG}";;
        i) INSTALLER_PATH="${OPTARG}";;
        m) PLUGIN_IMAGE="${OPTARG}";;
        n)
            case ${OPTARG} in
                ovn|OVNKubernetes)
                    NETWORK_PLUGIN="OVNKubernetes"
                    IMAGE_ENV_KEY="OVN_IMAGE"
                    ;;
                sdn|OpenShiftSDN)
                    ;;
                *)
                    echo "Unknown network plugin ${OPTARG}" >&2
                    exit 1
                ;;
            esac
            ;;
        w)
            WAIT_FOR_MANIFEST_UPDATES=1
            ;;
        *)
            print_usage
            exit 2
            ;;
    esac
done

if [[ -z "${CLUSTER_DIR:-}" ]]; then
    echo "error: CLUSTER_DIR must be set or '-c <cluster-dir>' must be given"
    echo
    print_usage
    exit 1
fi
mkdir -p "${CLUSTER_DIR}" >& /dev/null

if [[ -z "$(which oc 2> /dev/null || exit 0)" ]]; then
    echo "could not find 'oc' in PATH" >&2
    exit 1
fi

# Autodetect the state of the cluster to determine which mode to run in
if [[ -z "$(ls -A ${CLUSTER_DIR} 2> /dev/null | grep -v install-config.yaml | grep -v .openshift_install | grep -v env.sh)" ]]; then
    echo "Creating new cluster..."

    # Find openshift-install if not explicitly given
    if [[ -z "${INSTALLER_PATH}" ]]; then
        INSTALLER_PATH="$(which openshift-install 2> /dev/null || exit 0)"
        if [[ -z "${INSTALLER_PATH}" ]]; then
            echo "could not find openshift-install in PATH for building a new cluster" >&2
            exit 1
        fi
    fi

    rm -f "${CLUSTER_DIR}/env.sh"
    ensure_install_config
    create_manifests_with_network_plugin
    override_install_manifests

    # Wait for user to update manifests if requested
    if [[ -n "${WAIT_FOR_MANIFEST_UPDATES}" ]]; then
        read -n 1 -p "Pausing for manual manifest updates; press any key to continue..."
    fi

    extract_environment_from_manifests
    create_cluster
    wait_for_cluster
elif [[ -n "$(ls -A ${CLUSTER_DIR}/terraform.* 2> /dev/null)" ]]; then
    echo "Attaching to already running cluster..."

    wait_for_cluster
    stop_deployed_operator
    extract_environment_from_running_cluster
else
    echo "could not detect cluster state" >&2
    exit 1
fi

setup_operator_env "${IMAGE_ENV_KEY}" "${PLUGIN_IMAGE}"

env $(cat "${CLUSTER_DIR}/env.sh") _output/linux/amd64/cluster-network-operator
