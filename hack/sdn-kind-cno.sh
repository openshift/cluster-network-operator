#!/usr/bin/env bash
set -euo pipefail

# Version v1.19 or higher is required
K8S_VERSION=${K8S_VERSION:-v1.20.2}
BUILD_SDN=${BUILD_SDN:-false}
BUILD_CNO=${BUILD_CNO:-false}
BUILD_MULTUS=${BUILD_MULTUS:-false}
CNO_PATH=${CNO_PATH:-$GOPATH/src/github.com/openshift/cluster-network-operator}
SDN_PATH=${SDN_PATH:-$GOPATH/src/github.com/openshift/sdn}
KIND_CONFIG=${KIND_CONFIG:-$HOME/kind-sdn-config.yaml}
export KUBECONFIG=${HOME}/kube-sdn.conf
NUM_MASTER_NODES=1
SDN_KIND_VERBOSITY=${SDN_KIND_VERBOSITY:-5}

# Default networks (same as in KIND)
IP_FAMILY=ipv4
CLUSTER_CIDR=${CLUSTER_CIDR:-"10.244.0.0/16"}
SERVICE_NETWORK=${SERVICE_NETWORK:-"10.96.0.0/12"}
HOST_PREFIX=24

# Check for docker
if ! command -v docker; then
  echo "docker binary missing in PATH. Docker is required for this deployment"
  exit 1
fi

# Check for kubectl
if ! command -v kubectl; then
  echo "kubectl binary missing in PATH. Please build/install kubectl."
  exit 1
fi

# Ensure reachability to host via Docker network
if ! sudo iptables -C DOCKER-USER -j ACCEPT > /dev/null 2>&1; then 
  sudo iptables -I DOCKER-USER -j ACCEPT
fi

# create the config file
  cat <<EOF > ${KIND_CONFIG}
# config for 1 control plane node and 2 workers (necessary for conformance)
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  ipFamily: ipv4
  disableDefaultCNI: true
  podSubnet: ${CLUSTER_CIDR}
  serviceSubnet: ${SERVICE_NETWORK}
featureGates:
  APIPriorityAndFairness: true
runtimeConfig:
  flowcontrol.apiserver.k8s.io/v1alpha1: true
nodes:
- role: control-plane
- role: worker
- role: worker
EOF


# Create KIND cluster
kind create cluster --name sdn --image kindest/node:${K8S_VERSION} --config=${KIND_CONFIG} -v ${SDN_KIND_VERBOSITY}

echo -e "\n"

CNO_TEMPLATES=$CNO_PATH/manifests

if [ "$BUILD_CNO" = true ]; then
  echo "Building CNO"
  pushd $CNO_PATH
  CNO_IMAGE=$(BUILDCMD="docker build" ./hack/build-image.sh | grep 'Successfully tagged' | grep -Eo cluster-network-operator:.*)
  if [ -z "$CNO_IMAGE" ]; then
    echo "Error locating built CNO Image"
    exit 1
  fi
  echo "Loading CNO image into KIND"
  kind load docker-image $CNO_IMAGE --name sdn -v ${SDN_KIND_VERBOSITY}
  popd
fi

if [ "$BUILD_SDN" = true ]; then
  echo "Building SDN"
  pushd $SDN_PATH
  make clean
  popd
  echo "Loading SDN docker image into KIND"
  kind load docker-image sdn-test --name sdn -v ${SDN_KIND_VERBOSITY}
fi

OVS_PASSWD=$(docker run -t --rm --entrypoint 'grep' quay.io/openshift/origin-ovn-kubernetes:latest openvswitch /etc/passwd)
NODES=$(kind get nodes --name sdn)
for n in $NODES; do
  echo "Modifying node $n"
  echo "Modifying os-release for Multus"
  # required for Multus platform check
  docker exec $n sed -i 's/ID=.*/ID=rhcos/' /etc/os-release
  echo "Adding ovs user"
  docker exec $n bash -c "echo $OVS_PASSWD >> /etc/passwd"
  echo "Linking containerd socket to crio"
  docker exec $n mkdir -p /var/run/crio/
  docker exec $n ln -s /run/containerd/containerd.sock /var/run/crio/crio.sock
done

# openshift-network-operator need read access to the kubeconfig
# TODO: support multiple master nodes
 docker exec sdn-control-plane cp /etc/kubernetes/admin.conf /etc/kubernetes/kubeconfig
 docker exec sdn-control-plane chmod 666 /etc/kubernetes/kubeconfig

# Create Proxy resource
kubectl create -f https://raw.githubusercontent.com/openshift/api/master/config/v1/0000_03_config-operator_01_proxy.crd.yaml

# Create Network resource
kubectl create -f https://raw.githubusercontent.com/openshift/api/master/config/v1/0000_10_config-operator_01_network.crd.yaml

# Create cluster operator
kubectl create -f https://raw.githubusercontent.com/openshift/machine-api-operator/master/config/0000_00_cluster-version-operator_01_clusteroperator.crd.yaml

# Create ingress resource
kubectl create -f https://raw.githubusercontent.com/openshift/api/master/operator/v1/0000_50_ingress-operator_00-ingresscontroller.crd.yaml

# Create Egress Router
kubectl create -f https://raw.githubusercontent.com/openshift/api/master/networkoperator/v1/001-egressrouter.crd.yaml

if [ "$BUILD_SDN" = true ] || [ "$BUILD_CNO" = true ]; then
  pushd $CNO_TEMPLATES
  DEPLOYMENT_TEMPLATE=$(ls 0000*deployment*)
  if [ -z "$DEPLOYMENT_TEMPLATE" ]; then
    echo "error locating deployment template in $CNO_TEMPLATES"
    exit 1
  fi
  cp $DEPLOYMENT_TEMPLATE deployment.yaml.bk
  if [ "$BUILD_SDN" = true ]; then
    sed -i 's#quay.io/openshift/origin-sdn:latest#sdn-test#' $DEPLOYMENT_TEMPLATE
  fi
  if [ "$BUILD_CNO" = true ]; then
    sed -i "s#quay.io/openshift/origin-cluster-network-operator:.*#$CNO_IMAGE#" $DEPLOYMENT_TEMPLATE
  fi
fi

echo "Creating \"cluster-config-v1\" configMap with $NUM_MASTER_NODES master nodes"
cat <<EOF | kubectl create -f - 
apiVersion: v1
kind: ConfigMap
metadata:
  name: cluster-config-v1
  namespace: kube-system
data:
  install-config: |
    apiVersion: v1
    controlPlane:
      replicas: ${NUM_MASTER_NODES}
EOF

echo "Creating SDN CNO config"
cat << EOF | kubectl create -f -
apiVersion: config.openshift.io/v1
kind: Network
metadata:
  name: cluster
spec:
  clusterNetwork:
  - cidr: ${CLUSTER_CIDR}
    hostPrefix: ${HOST_PREFIX}
  networkType: OpenShiftSDN
  serviceNetwork:
  - ${SERVICE_NETWORK}
EOF

echo "Creating CNO operator"
for f in $(ls $CNO_TEMPLATES| grep 0000| grep -v credentials); do
  kubectl create -f ${CNO_TEMPLATES}/$f
done

# OVS is expected to run in systemd but that not an option in kindest/noede
# we need to deploy it in a pod.
echo "Creating OVS daemonset"
kubectl create namespace ovs-kind
kubectl create -f $CNO_PATH/hack/ovs-kind.yaml

if [ "$BUILD_SDN" = true ] || [ "$BUILD_CNO" = true ]; then
  mv deployment.yaml.bk $DEPLOYMENT_TEMPLATE
  popd
fi

if ! kubectl wait -n openshift-network-operator --for condition=available deployment network-operator --timeout=120s; then
  echo "Network operator not running"
  exit 1
fi



echo "Deleting default kube-proxy"
kubectl delete -n kube-system ds kube-proxy

if [ "$BUILD_CNO" != true ]; then
  echo "WARNING: patching CNO operator pod for OVN-K8S, deployment will no longer function if this pod is restarted"
  CNO_POD=$(kubectl get pod -n openshift-network-operator -o jsonpath='{.items[0].metadata.name}' --field-selector status.phase=Running)
fi


for n in $NODES; do
  echo "Sym-linking cni dirs for node $n"
  docker exec $n rm -rf /opt/cni/bin
  docker exec $n ln -s /var/lib/cni/bin /opt/cni/
  docker exec $n rm -rf /etc/cni/net.d
  docker exec $n mkdir -p /etc/cni/
  docker exec $n ln -s /etc/kubernetes/cni/net.d /etc/cni/
done

# wait until resources are created.
sleep 150


# TODO: configure a certificate for multus admission controller


# FIXME: temporary hack The multus admission controller cert isn't created
# correctly. Because right now multus is a nice to have, overwrite multus
# configuration with openshift-sdn directly
if ! kubectl wait -n openshift-multus --for=condition=ready pods -l app=multus --timeout=300s ; then
  echo "multus pods are not running"
  exit 1
fi
echo "WARNING: patching multus configuration. Deployment will no longer work if multus pods and kubelet are restarted"
for n in $NODES; do
  docker exec $n mv /etc/cni/net.d/00-multus.conf /tmp/00-multus.conf
  docker exec $n cp /var/run/multus/cni/net.d/80-openshift-network.conf /etc/cni/net.d/80-openshift-network.conf
  docker exec $n systemctl restart containerd
  docker exec $n systemctl restart kubelet
done

if ! kubectl wait -n openshift-sdn --for=condition=ready pods -l app=sdn --timeout=300s ; then
  echo "SDN pods are not running"
  exit 1
fi

echo "Deployment Complete!"
