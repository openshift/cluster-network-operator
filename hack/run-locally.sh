#!/bin/bash 

# run-locally.sh is some scaffolding around running a local instance of the
# network operator with the installer.
# See https://github.com/openshift/cluster-network-operator/wiki/Running-a-local-cluster-network-operator-for-plugin-development#run-hackrun-locallysh-to-start-a-cluster-with-your-custom-image

set -o errexit
set -o nounset

if [[ -n "${HACK_MINIMIZE:-}" ]]; then
    echo "HACK_MINIMIZE set! This is only for development, and the installer will likely not succeed."
    echo "You will still be left with a reasonably functional cluster."
    echo ""
fi

function run_vs_existing_cluster {
    echo "Attaching to already running cluster..."
    wait_for_cluster
    extract_environment_from_running_cluster
    stop_deployed_operator
}

# Install our overrides so the cluster doesn't run the network operator.
function override_install_manifests() {
    # Patch the CVO to not create the network operator
    oc --kubeconfig=hack/null-kubeconfig patch --type=json --local=true -f="${CLUSTER_DIR}/manifests/cvo-overrides.yaml" -p "$(cat hack/overrides-patch.yaml)" -o yaml > "${CLUSTER_DIR}/manifests/.cvo-overrides.yaml.new"
    # Optionally, tell the CVO to skip some unnecessary components
    if [[ -n "${HACK_MINIMIZE:-}" ]]; then
        oc --kubeconfig=hack/null-kubeconfig patch --type=json --local=true -f="${CLUSTER_DIR}/manifests/.cvo-overrides.yaml.new" -p "$(cat hack/overrides-minimize-patch.yaml)" -o yaml > "${CLUSTER_DIR}/manifests/.cvo-overrides.yaml.new.2"
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
    echo "Copying environment variables from manifest to ${CLUSTER_DIR}/env.sh"
    oc get deployment -n openshift-network-operator network-operator -ojsonpath='{range .spec.template.spec.containers[0].env[?(@.value)]}{.name}{"="}{.value}{"\n"}' > "${CLUSTER_DIR}/env.sh"
    if [[ $EXPORT_ENV_ONLY == true ]]; then
        exit 0
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

    # library-go controller needs to write stuff here
    if [[ ! -w "/var/run/secrets" ]]; then
        echo "Need /var/run/secrets to be writable, please execute"
        echo sudo /bin/bash -c "'mkdir -p /var/run/secrets && chown ${USER} /var/run/secrets'"
    fi

    mkdir -p /var/run/secrets/kubernetes.io/serviceaccount/
    echo -n "openshift-network-operator" > /var/run/secrets/kubernetes.io/serviceaccount/namespace
}

# Patch the CNO out of the cluster-version-operator and scale it down to zero so
# we can run our local CNO instead
function stop_deployed_operator() {
    oc patch --type=json -p "$(cat hack/overrides-patch.yaml)" clusterversion version

    if [[ -n "$(oc get deployments -n openshift-network-operator 2> /dev/null | grep network-operator)" ]]; then
        echo "Scaling the deployed network operator to 0"
        oc scale deployment -n openshift-network-operator network-operator --replicas 0
    fi
}

# wait_for_cluster waits for the cluster to come up and sets some variables
function wait_for_cluster() {
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

$0 runs against an existing cluster using the specific '-k' option or 
detects whether the cluster given by the '-c' option does not yet exist or
is already running. If it does not yet exist the cluster will be created using
the options supplied below. If it is already running, any running
cluster-network-operator will be stopped, plugin image overrides applied, and
a local cluster-network-operator started.

The following options are accepted for both new and existing clusters:

 -c DIR           the cluster installation temporary directory (can also be given via CLUSTER_DIR); 
 -k KUBECONFIG    the kubeconfig pointing to the existing cluster (can also be given via KUBECONFIG); 

Either -c or -k (or their corresponding environment variable) is required

 -m IMAGE         custom network plugin container image name

The following options are always accepted but only used for new clusters:

 -e EXPORT_ENV_ONLY  exports cluster environment only, used as a helper which allows you to modify the image you want to target later
 -f CONFIG           the path to an openshift-install-created install-config.yaml file; if not given one will be created
 -i INSTALLER        path to the openshift-install binary; if not given PATH will be searched
 -w                  pause after creating manifests to allow manual overrides

The following environment variables are honored:
 - CLUSTER_DIR: the cluster installation temp directory
 - KUBECONFIG: the kubeconfig pointing to the existing cluster 
 - INSTALLER_PATH: the path to the openshift-install binary for 'new' mode
 - INSTALL_CONFIG: the path to the install-config.yaml file for 'new' mode
"
}

EXPORT_ENV_ONLY=false
PLUGIN_IMAGE="${PLUGIN_IMAGE:-}"
NETWORK_PLUGIN="OVNKubernetes"
IMAGE_ENV_KEY="OVN_IMAGE"
CLUSTER_DIR="${CLUSTER_DIR:-}"
INSTALLER_PATH="${INSTALLER_PATH:-}"
INSTALL_CONFIG="${INSTALL_CONFIG:-}"
WAIT_FOR_MANIFEST_UPDATES="${WAIT_FOR_MANIFEST_UPDATES:-}"
KUBECONFIG="${KUBECONFIG:-}"

while getopts "e?c:f:i:m:k:n:w" opt; do
    case $opt in
        c) CLUSTER_DIR="${OPTARG}"
           mkdir -p "${CLUSTER_DIR}" >& /dev/null;;
        e) EXPORT_ENV_ONLY=true;;
        f) INSTALL_CONFIG="${OPTARG}";;
        i) INSTALLER_PATH="${OPTARG}";;
        m) PLUGIN_IMAGE="${OPTARG}";;
        k) KUBECONFIG="${OPTARG}";;
        w)
            WAIT_FOR_MANIFEST_UPDATES=1
            ;;
        *)
            print_usage
            exit 2
            ;;
    esac
done

if [[ -z "$(which oc 2> /dev/null || exit 0)" ]]; then
    echo "error: could not find 'oc' in PATH" >&2
    exit 1
fi

# re-build the network operator
echo "rebuilding the CNO"
hack/build-go.sh

# Autodetect the state of the cluster to determine which mode to run in
if [[ -n "${CLUSTER_DIR}" ]]; then
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
        export KUBECONFIG="${CLUSTER_DIR}/auth/kubeconfig"
        run_vs_existing_cluster
    else
        echo "could not detect cluster state" >&2
        exit 1
    fi
elif [[ -n "${KUBECONFIG}" ]]; then
    export CLUSTER_DIR=/tmp
    run_vs_existing_cluster
else
    echo "error: KUBECONFIG / CLUSTER_DIR must be set or '-c <cluster-dir>'/'-k <kubeconfig>' must be given"
    echo
    print_usage
    exit 1
fi

setup_operator_env "${IMAGE_ENV_KEY}" "${PLUGIN_IMAGE}"

env $(cat "${CLUSTER_DIR}/env.sh") OSDK_FORCE_RUN_MODE=local ./cluster-network-operator start --kubeconfig "${KUBECONFIG}"
