#!/usr/bin/env bash
set -euo pipefail

# Version v1.17.0 or higher is required
K8S_VERSION=${K8S_VERSION:-v1.18.2}
BUILD_OVN=${BUILD_OVN:-false}
BUILD_CNO=${BUILD_CNO:-false}
BUILD_MULTUS=${BUILD_MULTUS:-false}
CNO_PATH=${CNO_PATH:-$GOPATH/src/github.com/openshift/cluster-network-operator}
OVN_K8S_PATH=${OVN_K8S_PATH:-$GOPATH/src/github.com/ovn-org/ovn-kubernetes}
KIND_CONFIG=${KIND_CONFIG:-$HOME/kind-ovn-config.yaml}
export KUBECONFIG=${HOME}/kube-ovn.conf
NUM_MASTER_NODES=${NUM_MASTER_NODES:-1}
OVN_KIND_VERBOSITY=${OVN_KIND_VERBOSITY:-5}

# Default networks (same as in KIND)
if [ "${IP_FAMILY:-ipv4}" = "ipv6" ]; then
  CLUSTER_CIDR=${CLUSTER_CIDR:-"fd00:10:244::/48"}
  SERVICE_NETWORK=${SERVICE_NETWORK:-"fd00:10:96::/112"}
  HOST_PREFIX=64
else
  CLUSTER_CIDR=${CLUSTER_CIDR:-"10.244.0.0/16"}
  SERVICE_NETWORK=${SERVICE_NETWORK:-"10.96.0.0/12"}
  HOST_PREFIX=24
fi

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

if [ "$NUM_MASTER_NODES" -eq 1 ]; then
  nodes="- role: worker
- role: worker"
elif [ "$NUM_MASTER_NODES" -eq 3 ]; then
  nodes="- role: control-plane
- role: control-plane"
else
  echo "Incorrect number of master nodes: ${NUM_MASTER_NODES}. Please use 1 or 3"
  exit 1
fi

 # create the config file
  cat <<EOF > ${KIND_CONFIG}
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  ipFamily: ${IP_FAMILY:-ipv4}
  disableDefaultCNI: true
  podSubnet: ${CLUSTER_CIDR:-10.244.0.0/16}
  serviceSubnet: ${SERVICE_NETWORK:-10.96.0.0/12}
nodes:
- role: control-plane
${nodes}
EOF


# Create KIND cluster
kind create cluster --name ovn --image kindest/node:${K8S_VERSION} --config=${KIND_CONFIG} -v ${OVN_KIND_VERBOSITY}

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
  kind load docker-image $CNO_IMAGE --name ovn -v ${OVN_KIND_VERBOSITY}
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
  sudo cp $CNO_PATH/bindata/network/ovn-kubernetes/ovnkube-cno-master.sh .
  sudo cp $CNO_PATH/bindata/network/ovn-kubernetes/ovnkube-cno-node.sh .
 
  cat << EOF | docker build -t origin-ovn-kubernetes:dev -f - .
FROM quay.io/openshift/origin-ovn-kubernetes:4.6
COPY ovnkube ovn-kube-util /usr/bin/
COPY ovn-k8s-cni-overlay /usr/libexec/cni/ovn-k8s-cni-overlay
COPY ovnkube.sh /root/
COPY ovnkube-cno-master.sh /root/
RUN chmod +x /root/ovnkube-cno-master.sh
COPY ovnkube-cno-node.sh /root/
RUN chmod +x /root/ovnkube-cno-node.sh
COPY --from=centos:7 /sbin/arping  /usr/bin/.
EOF
  popd
  echo "Loading OVN-K8S docker image into KIND"
  kind load docker-image origin-ovn-kubernetes:dev --name ovn -v ${OVN_KIND_VERBOSITY}
  popd
fi

NODES=$(docker ps | grep "kindest/node" | awk '{ print $1 }')
for n in $NODES; do
  echo "Modifying node $n"
  echo "Modifying os-release for Multus"
  # required for Multus platform check
  docker exec $n sed -i 's/ID=.*/ID=rhcos/' /etc/os-release
done

# openshift-network-operator need read access to the kubeconfig
# TODO: support multiple master nodes
docker exec ovn-control-plane cp /etc/kubernetes/admin.conf /etc/kubernetes/kubeconfig
docker exec ovn-control-plane chmod 666 /etc/kubernetes/kubeconfig

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

# CNO needs to get scheduled on first node, because thats where KIND will put /etc/kubernetes/kubeconfig
until kubectl get pod -n openshift-network-operator -o wide | grep "ovn-control-plane ";do
  kubectl delete pod -n openshift-network-operator --all --grace-period=0 --force
  sleep 1
done

if ! kubectl wait -n openshift-network-operator --for condition=available deployment network-operator --timeout=120s; then
  echo "Network operator not running"
  exit 1
fi

if [ "$BUILD_CNO" != true ]; then
  echo "WARNING: patching CNO operator pod for OVN-K8S, deployment will no longer function if this pod is restarted"
  CNO_POD=$(kubectl get pod -n openshift-network-operator -o jsonpath='{.items[0].metadata.name}' --field-selector status.phase=Running)
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
    hostPrefix: ${HOST_PREFIX}
  networkType: OVNKubernetes
  serviceNetwork:
  - ${SERVICE_NETWORK}
EOF

for n in $NODES; do
  echo "Sym-linking cni dirs for node $n"
  docker exec $n rm -rf /opt/cni/bin
  docker exec $n ln -s /var/lib/cni/bin /opt/cni/
  docker exec $n rm -rf /etc/cni/net.d
  docker exec $n mkdir -p /etc/cni/
  docker exec $n ln -s /etc/kubernetes/cni/net.d /etc/cni/
done

# wait until resources are created
sleep 30

# Wait for pods to come up and be ready. Note metrics pods do not currently work.
if ! kubectl wait -n openshift-ovn-kubernetes --for=condition=ready --selector=app!=ovnkube-node-metrics,app!=ovnkube-master-metrics pods --all --timeout=300s ; then 
  echo "OVN-k8s pods are not Ready"
  exit 1
fi

# Configuring secret for multus-admission-webhook
# https://raw.githubusercontent.com/openshift/multus-admission-controller/master/hack/webhook-create-signed-cert.sh
$CNO_PATH/hack/webhook-create-signed-cert.sh --service multus-admission-controller --namespace openshift-multus --secret multus-admission-controller-secret

# Wait for pods to come up and be ready. Note metrics and admission controller pods do not currently work.
if ! kubectl wait -n openshift-multus --for=condition=ready pods --selector=app!=network-metrics-daemon,app!=multus-admission-controller --all --timeout=300s ; then
  echo "multus pods are not running"
  exit 1
fi

echo "Deployment Complete! Use 'kind export kubeconfig --name ovn' to access your cluster"
