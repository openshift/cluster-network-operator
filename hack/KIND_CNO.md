# Kubernetes in Docker (KIND) CNO Deployment

The [KIND](https://github.com/kubernetes-sigs/kind) deployment is used for reproducing an OpenShift networking
environment with upstream K8S. The value proposition is really for developers who want to reproduce an issue or test a
fix in an environment that can be brought up locally and within a few minutes.

## How does it work?

At a high level, the deployment will create a docker container per K8S node. This docker container will host its own
containerd instance, and pods, kubelet, etc will all run inside of these docker containers.

``ovn-kind-cno.sh`` is the script that will handle deploying K8S, CNO, and consequently Multus/OVN-K8S.
By default it will use the following configuration for kind, and IPv4 multinode cluster:

```yaml
# config for 1 control plane node and 2 workers (necessary for conformance)
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  ipFamily: ${IP_FAMILY:-ipv4}
  disableDefaultCNI: true
  podSubnet: ${CLUSTER_CIDR:-10.244.0.0/16}
  serviceSubnet: ${SERVICE_CIDR:-10.96.0.0/12}
nodes:
- role: control-plane
- role: worker
- role: worker
```

It accepts environment variables to customize the environment, with the following defaults:

```sh
K8S_VERSION=${K8S_VERSION:-v1.18.2}
BUILD_OVN=${BUILD_OVN:-false}
BUILD_CNO=${BUILD_CNO:-false}
BUILD_MULTUS=${BUILD_MULTUS:-false}
CNO_PATH=${CNO_PATH:-$GOPATH/src/github.com/openshift/cluster-network-operator}
OVN_K8S_PATH=${OVN_K8S_PATH:-$GOPATH/src/github.com/ovn-org/ovn-kubernetes}
KIND_CONFIG=${KIND_CONFIG:-$HOME/kind-ovn-config.yaml}
export KUBECONFIG=${HOME}/kube-ovn.conf
NUM_MASTER_NODES=1
OVN_KIND_VERBOSITY=${OVN_KIND_VERBOSITY:-5}
```

## Requirements

The deployment should work locally on a laptop for a single node. To deploy, it is required to have golang
(1.11 or later), kubectl, and docker installed. The KIND deployment requires at least Kubernetes v1.17 (installed by
default) so building corresponding kubectl client is best.

It is also required to install KIND before deploying. As of this writing the latest KIND release is v0.8.1, and
installation instructions can be found at https://github.com/kubernetes-sigs/kind#installation-and-usage.

## Usage

Simply run ``ovn-kind-cno.sh`` to deploy. Additionally, a user may wish to build CNO or OVN-K8S locally. To build CNO
simply execute:

````
$ BUILD_CNO=true ovn-kind-cno.sh
```` 

It is recommended to build CNO. In order to deploy changes are needed to the OVN yaml files, which are included within
the CNO docker image. If CNO is not built, these files are patched live within the running CNO pod. If that pod is
restarted post deployment, the changes will be lost, and OVN will stop functioning correctly.

To build OVN K8S:

```
$ BUILD_OVN=true ovn-kind-cno.sh
```

To build an IPv6 ONLY cluster:

```
$ IP_FAMILY=ipv6 ovn-kind-cno.sh
```

This will build an OVN K8S docker image to use with the deployment from a local git workspace. By default the script will use
a path relative to your GOPATH. To override this, specify `CNO_PATH` or `OVN_K8S_PATH` when executing the above.

### Post deployment

Post deployment you can simply use kubectl to interact with your cluster. Additionally you may wish to exec into the
docker node and poke around. Inside the docker container you can see all the running pods via:

```sh
docker exec -it ovn-control-plane crictl ps
CONTAINER           IMAGE               CREATED             STATE               NAME                      ATTEMPT             POD ID
fe1fe79be1a73       e4b1ab4cbf659       2 hours ago         Running             kube-multus               0                   91dad15b65f72
3a69b9c3b6456       e9b443d218dc7       2 hours ago         Running             ovnkube-master            0                   6e24b8c012e49
394730b38dc46       e9b443d218dc7       2 hours ago         Running             sbdb                      0                   6e24b8c012e49
504d7fbb09129       e9b443d218dc7       2 hours ago         Running             nbdb                      0                   6e24b8c012e49
29a497add0ee8       e9b443d218dc7       2 hours ago         Running             ovnkube-node              0                   d2ef48bd1b871
c9232a2cabdb1       e9b443d218dc7       2 hours ago         Running             northd                    0                   6e24b8c012e49
d1e1de2d2b763       e9b443d218dc7       2 hours ago         Running             ovn-controller            0                   d2ef48bd1b871
884a0ea94c0ff       e9b443d218dc7       2 hours ago         Running             ovs-daemons               0                   7fbfcf5dbd924
eac57b49af7ab       4ec1801e760a8       2 hours ago         Running             network-operator          0                   f7821d3420787
29c039335d1b8       dd61f68ee6f73       3 hours ago         Running             kube-proxy                0                   1554057b10ecd
e6fca3ad23f05       303ce5db0e90d       3 hours ago         Running             etcd                      0                   f6a03d6d1ce11
43d6cfc99f9f0       06f726e5bab40       3 hours ago         Running             kube-apiserver            0                   42834ae09e8ce
898e5e0a7aa64       4b2a99ce99208       3 hours ago         Running             kube-controller-manager   0                   a5bf81537cd99
3d761203df63c       5e7eb76f91581       3 hours ago         Running             kube-scheduler            0                   34ef9a3da0f2d
```

Kubelet logs can also be found via journalctl.

```sh
-- Logs begin at Sun 2020-06-07 09:48:19 UTC, end at Sun 2020-06-07 12:28:37 UTC. --
Jun 07 09:48:21 ovn-control-plane systemd[1]: Started kubelet: The Kubernetes Node Agent.
Jun 07 09:48:24 ovn-control-plane kubelet[151]: Flag --fail-swap-on has been deprecated, This parameter should be set via the config file specified by the Kubelet's --config flag. See https://kubernetes.io/docs
/tasks/administer-cluster/kubelet-config-file/ for more information.
Jun 07 09:48:24 ovn-control-plane kubelet[151]: F0607 09:48:24.108531     151 server.go:199] failed to load Kubelet config file /var/lib/kubelet/config.yaml, error failed to read kubelet config file "/var/lib/kubelet/config.yaml", error: open /var/lib/kubelet/config.yaml: no such file or directory
```

### Using the cluster

You can use now the cluster, deploy applications and expose them.

Let's create a Deployment:

```sh
kubectl create deployment hello-node --image=k8s.gcr.io/echoserver:1.4
deployment.apps/hello-node created
```

and expose it using a NodePort Service.

```sh
kubectl expose deployment hello-node --type NodePort --port=8080
service/hello-node exposed
```

you can check the port used by the NodePort service:

```sh
 kubectl get service hello-node -o wide
NAME         TYPE       CLUSTER-IP      EXTERNAL-IP   PORT(S)          AGE   SELECTOR
hello-node   NodePort   10.111.137.68   <none>        8080:32591/TCP   52s   app=hello-node
```

you should be able to access the service in any of the cluster nodes IP, that you can obtain using:

```sh
kubectl get nodes -o wide
NAME                STATUS   ROLES    AGE     VERSION   INTERNAL-IP   EXTERNAL-IP   OS-IMAGE           KERNEL-VERSION     CONTAINER-RUNTIME
ovn-control-plane   Ready    master   4h29m   v1.18.2   172.18.0.3    <none>        Ubuntu 20.04 LTS   5.4.0-33-generic   containerd://1.3.3-14-g449e9269
ovn-worker          Ready    <none>   4h28m   v1.18.2   172.18.0.2    <none>        Ubuntu 20.04 LTS   5.4.0-33-generic   containerd://1.3.3-14-g449e9269
ovn-worker2         Ready    <none>   4h28m   v1.18.2   172.18.0.4    <none>        Ubuntu 20.04 LTS   5.4.0-33-generic   containerd://1.3.3-14-g449e9269
```

if everything is ok, and using this examples IP 172.18.0.2 and Port 32591, we should obtain an answer like this:

```sh
curl http://172.18.0.2:32591
CLIENT VALUES:
client_address=100.64.2.1
command=GET
real path=/
query=nil
request_version=1.1
request_uri=http://172.18.0.2:8080/

SERVER VALUES:
server_version=nginx: 1.10.0 - lua: 10001

HEADERS RECEIVED:
accept=*/*
host=172.18.0.2:32591
user-agent=curl/7.68.0
BODY:
```

### Running kubernetes e2e test

Once you have your cluster working, you can use it to debug Kubernetes e2e tests.

In order to do that you just have to checkout the desired Kubernetes branch and
compile the e2e.test binary:

```
cd $GOPATH/src/k8s.io/kubernetes
bazel build //test/e2e:e2e.test
```

Execute your tests using the KIND cluster kubeconfig file:

```
bazel-bin/test/e2e/e2e.test -kubeconfig $HOME/kube-ovn.conf -ginkgo.focus="\[sig-network\].*Conformance" -num-nodes 2
```

### Cleaning up

In order to clean up your environment and remove your KIND cluster, simply execute:

```sh
$ kind delete cluster --name ovn
```

## Todo

* Add support for building custom Multus

