#!/usr/bin/env bash
set -euo pipefail

# Version v1.17.0 or higher is required
K8S_VERSION=${K8S_VERSION:-v1.17.0}
BUILD_OVN=${BUILD_OVN:-false}
BUILD_CNO=${BUILD_CNO:-false}
BUILD_MULTUS=${BUILD_MULTUS:-false}
CNO_PATH=${CNO_PATH:-$GOPATH/src/github.com/openshift/cluster-network-operator}
OVN_K8S_PATH=${OVN_K8S_PATH:-$GOPATH/src/github.com/ovn-org/ovn-kubernetes}
CLUSTER_CIDR=${CLUSTER_CIDR:-"172.16.0.0/16"}
SERVICE_NETWORK=${SERVICE_NETWORK:-"172.30.0.0/16"}
# Skip the comment lines and retrieve the number of Master nodes from kind.yaml file.
NUM_MASTER_NODES=`grep "^[^#]" kind.yaml | grep -c "role\: control-plane"`

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

# Detect IP to use as API server
API_IP=$(ip -4 addr | grep -oP '(?<=inet\s)\d+(\.\d+){3}' | grep -v "127.0.0.1" | head -n 1)
if [ -z "$API_IP" ]; then
  echo "Error detecting machine IP to use as API server"
  exit 1
fi

sed -i "s/apiServerAddress.*/apiServerAddress: ${API_IP}/" kind.yaml

# Ensure reachability to host via Docker network
if ! sudo iptables -C DOCKER-USER -j ACCEPT > /dev/null 2>&1; then 
  sudo iptables -I DOCKER-USER -j ACCEPT
fi

# Create KIND cluster
kind create cluster --name ovn --kubeconfig ${HOME}/admin.conf --image kindest/node:${K8S_VERSION} --config=./kind.yaml
export KUBECONFIG=${HOME}/admin.conf
mkdir -p /tmp/kind
sudo chmod 777 /tmp/kind
count=0
until kubectl get secrets -o jsonpath='{.items[].data.ca\.crt} &> /dev/null'
do
  if [ $count -gt 10 ]; then
    echo "Failed to get k8s crt/token"
    exit 1
  fi
  count=$((count+1))
  echo "secrets not available on attempt $count"
  sleep 5
done
kubectl get secrets -o jsonpath='{.items[].data.ca\.crt}' > /tmp/kind/ca.crt
kubectl get secrets -o jsonpath='{.items[].data.token}' > /tmp/kind/token

echo -e "\n"

CNO_TEMPLATES=$CNO_PATH/manifests

if [ "$BUILD_CNO" = true ]; then
  echo "Building CNO"
  pushd $CNO_PATH
  sed -i '/host-run-netns/{n;s/readOnly.*/mountPropagation: Bidirectional/}' bindata/network/ovn-kubernetes/ovnkube-node.yaml
  CNO_IMAGE=$(BUILDCMD="docker build" ./hack/build-image.sh | grep 'Successfully tagged' | grep -Eo cluster-network-operator:.*)
  sed -i '/host-run-netns/{n;s/mountPropagation.*/readOnly: true/}' bindata/network/ovn-kubernetes/ovnkube-node.yaml
  if [ -z "$CNO_IMAGE" ]; then
    echo "Error locating built CNO Image"
    exit 1
  fi
  echo "Loading CNO image into KIND"
  kind load docker-image $CNO_IMAGE --name ovn
  popd
fi

if [ "$BUILD_OVN" = true ]; then
  echo "Building OVN-K8S"
  pushd $OVN_K8S_PATH
  pushd go-controller
  make clean
  make
  popd
  pushd dist/images
  sudo cp -f ../../go-controller/_output/go/bin/* .
  cat << EOF | docker build -t origin-ovn-kubernetes:dev -f - .
FROM quay.io/openshift/origin-ovn-kubernetes:4.3
COPY ovnkube ovn-kube-util /usr/bin/
COPY ovn-k8s-cni-overlay /usr/libexec/cni/ovn-k8s-cni-overlay
COPY ovnkube.sh /root/
EOF
  popd
  echo "Loading OVN-K8S docker image into KIND"
  kind load docker-image origin-ovn-kubernetes:dev --name ovn
  popd
fi

NODES=$(docker ps | grep "kindest/node" | awk '{ print $1 }')
for n in $NODES; do
  echo "Modifying node $n"
  echo "Copying kubeconfig"
  docker cp ~/admin.conf $n:/etc/kubernetes/kubeconfig
  docker exec $n chmod 777 /etc/kubernetes/kubeconfig
  echo "Modifying os-release for Multus"
  # required for Multus platform check
  docker exec $n sed -i 's/ID=.*/ID=rhcos/' /etc/os-release
done

# Create Proxy resource
kubectl create -f https://raw.githubusercontent.com/openshift/api/e7fa4b871a25985ef0cc36c2fbd9f2cb4445dc9c/config/v1/0000_03_config-operator_01_proxy.crd.yaml

# Create Network resource
kubectl create -f https://raw.githubusercontent.com/openshift/api/e7fa4b871a25985ef0cc36c2fbd9f2cb4445dc9c/config/v1/0000_10_config-operator_01_network.crd.yaml

# Create cluster operator
kubectl create -f https://raw.githubusercontent.com/openshift/machine-api-operator/050a65a2bdabcc2c2f17036de967c6bcee6d6a48/config/0000_00_cluster-version-operator_01_clusteroperator.crd.yaml

if [ "$BUILD_OVN" = true ] || [ "$BUILD_CNO" = true ]; then
  pushd $CNO_TEMPLATES
  DEPLOYMENT_TEMPLATE=$(ls 0000*deployment*)
  if [ -z "$DEPLOYMENT_TEMPLATE" ]; then
    echo "error locating deployment template in $CNO_TEMPLATES"
    exit 1
  fi
  cp $DEPLOYMENT_TEMPLATE deployment.yaml.bk
  if [ "$BUILD_OVN" = true ]; then
    sed -i 's/".*origin-ovn-kubernetes:.*/"origin-ovn-kubernetes:dev"/' $DEPLOYMENT_TEMPLATE
  fi
  if [ "$BUILD_CNO" = true ]; then
    sed -i "s#quay.io/openshift/origin-cluster-network-operator:.*#$CNO_IMAGE#" $DEPLOYMENT_TEMPLATE
  fi
fi

echo "Creating CNO operator"
for f in $(ls $CNO_TEMPLATES| grep 0000| grep -v credentials); do
  kubectl create -f ${CNO_TEMPLATES}/$f
done

if [ "$BUILD_OVN" = true ] || [ "$BUILD_CNO" = true ]; then
  mv deployment.yaml.bk $DEPLOYMENT_TEMPLATE
  popd
fi

count=1
until kubectl get pod -n openshift-network-operator -o jsonpath='{.items[0].metadata.name}' --field-selector status.phase=Running &> /dev/null ;do
  if [ $count -gt 15 ]; then
    echo "Network operator not running"
    exit 1
  fi
  echo "Network operator pod not available yet on attempt $count"
  count=$((count+1))
  sleep 10

done

CNO_POD=$(kubectl get pod -n openshift-network-operator -o jsonpath='{.items[0].metadata.name}' --field-selector status.phase=Running)
if [ -z "$CNO_POD" ]; then
    echo "Cannot find running CNO pod"
    exit 1
fi

if [ "$BUILD_CNO" != true ]; then
  echo "WARNING: patching CNO operator pod for OVN-K8S, deployment will no longer function if this pod is restarted"
  kubectl -n openshift-network-operator  exec $CNO_POD sed -i '/host-run-netns/{n;s/readOnly.*/mountPropagation: Bidirectional/}' /bindata/network/ovn-kubernetes/ovnkube-node.yaml > /tmp/ovnkube-node.yaml
  kubectl cp /tmp/ovnkube-node.yaml openshift-network-operator/${CNO_POD}:/bindata/network/ovn-kubernetes/
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

echo "Creating OVN CNO config"
cat << EOF | kubectl create -f -
apiVersion: config.openshift.io/v1
kind: Network
metadata:
  name: cluster
spec:
  clusterNetwork:
  - cidr: ${CLUSTER_CIDR}
    hostPrefix: 24
  networkType: OVNKubernetes
  serviceNetwork:
  - ${SERVICE_NETWORK}
EOF

count=1
until kubectl get pod -n openshift-ovn-kubernetes -o jsonpath="{.items[0].status.phase}" 2> /dev/null | grep Running &> /dev/null;do
  if [ $count -gt 15 ]; then
    echo "OVN-k8s pods are not running"
    exit 1
  fi
  echo "OVN pod not available yet on attempt $count"
  count=$((count+1))
  sleep 10
done
sleep 10


for n in $NODES; do
  echo "Sym-linking cni dirs for node $n"
  docker exec $n rm -rf /opt/cni/bin
  docker exec $n ln -s /var/lib/cni/bin /opt/cni/
  docker exec $n rm -rf /etc/cni/net.d
  docker exec $n mkdir -p /etc/cni/
  docker exec $n ln -s /etc/kubernetes/cni/net.d /etc/cni/
done

echo "Deployment Complete!"
