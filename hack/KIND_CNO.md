# Kubernetes in Docker (KIND) CNO Deployment

The [KIND](https://github.com/kubernetes-sigs/kind) deployment is used for reproducing an OpenShift networking
environment with upstream K8S. The value proposition is really for developers who want to reproduce an issue or test a
fix in an environment that can be brought up locally and within a few minutes.

## How does it work?

At a high level, the deployment will create a docker container per K8S node. This docker container will host its own
containerd instance, and pods, kubelet, etc will all run inside of these docker containers. The ``kind.yaml`` file
declares how many nodes will be deployed. Currently only a single node is supported. There are plans to support HA as
well as more nodes. ``ovn-kind-cno.sh`` is the script that will handle deploying K8S, CNO, and consequently
Multus/OVN-K8S.

## Requirements

The deployment should work locally on a laptop for a single node. To deploy, it is required to have golang
(1.11 or later), kubectl, and docker installed. The KIND deployment requires at least Kubernetes v1.17 (installed by
default) so building corresponding kubectl client is best.

It is also required to install KIND before deploying. As of this writing the latest KIND release is v0.7.0, and
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

````
$ BUILD_OVN=true ovn-kind-cno.sh
````

This will build an OVN K8S docker image to use with the deployment from a local git workspace. By default the script will use
a path relative to your GOPATH. To override this, specify `CNO_PATH` or `OVN_K8S_PATH` when executing the above.

Post deployment you can simply use kubectl to interact with your cluster. Additionally you may wish to exec into the
docker node and poke around. Inside the docker container you can see all the running pods via:

````
root@ovn-control-plane:/# ctr  --namespace k8s.io containers list
````

Kubelet logs can also be found via journalctl.

In order to clean up your environment and remove your KIND cluster, simply execute:

````
$ kind delete cluster --name ovn
````

## Todo

* Add support for building custom Multus
* Fix multus-admission-controller not coming up



