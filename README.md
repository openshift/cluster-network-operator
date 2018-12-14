# Cluster Network Operator

The Cluster Network Operator installs and upgrades the networking components on an OpenShift Kubernetes cluster.

It follows the [Controller pattern](https://godoc.org/github.com/kubernetes-sigs/controller-runtime/pkg#hdr-Controller): it reconciles the state of the cluster against a desired configuration. The configuration specified by a CustomResourceDefinition called `networkoperator.openshift.io/NetworkConfig/v1`, which has a corresponding [type](/openshift/cluster-network-operator/blob/master/pkg/apis/networkoperator/v1/networkconfig_types.go).

When the controller has reconciled and all its dependent resources have converged, the cluster should have an installed SDN plugin and a working service network. In OpenShift, the Cluster Network Operator runs very early in the install process -- while the boostrap API server is still running.

# Configuring
The network operator has a complex configuration, but most parameters have a sensible default.

The configuration must be called "default".

A configuration with minimum parameters set:

```yaml
apiVersion: "networkoperator.openshift.io/v1"
kind: "NetworkConfig"
metadata:
  name: "default"
spec:
  serviceNetwork: "172.30.0.0/16"
  clusterNetworks:
    - cidr: "10.128.0.0/14"
      hostSubnetLength: 9
  defaultNetwork:
    type: OpenShiftSDN
```

## Configuring IP address pools
Users must supply at least two address pools - one for pods, and one for services. These are the ClusterNetworks and ServiceNetwork parameter. Some network plugins, such as OpenShiftSDN, support multiple ClusterNetworks. All address blocks must be non-overlapping. You should select address pools large enough to fit your anticipated workload.

Currently, changing the address pools once set is not supported. In the future, some network providers may support expanding the address pools.

Each ClusterNetwork entry has an additional required parameter, `hostSubnetLength`, that specifies the address size to assign to assign to each individual node. Note that this is currently *reverse* from the usual CIDR notation - a hostSubnetLength of 9 means that the node will be assigned a /23.

Example
```yaml
spec:
  serviceNetwork: "172.30.0.0/16"
  clusterNetworks:
    - cidr: "10.128.0.0/14"
      hostSubnetLength: 9
    - cidr: "192.168.0.0/18"
      hostSubnetLength: 9
```

## Configuring the default network provider
Users must select a default network provider. This cannot be changed. Different network providers have additional provider-specific settings.

Currently, the only supported value for network Type is `OpenShiftSDN`.

### Configuring OpenShiftSDN
OpenShiftSDN supports the following configuration options, all of which are optional:
* `mode`: one of "Subnet" "Multitenant", or "NetworkPolicy". Configures the [isolation mode](https://docs.openshift.com/container-platform/3.11/architecture/networking/sdn.html#overview) for OpenShift SDN. The default is "NetworkPolicy".
* `vxlanPort`: The port to use for the VXLAN overlay. The default is 4789
* `MTU`: The MTU to use for the VXLAN overlay. The default is the MTU of the node that the cluster-network-operator is first run on, minus 50 bytes for overhead. If the nodes in your cluster don't all have the same MTU then you will need to set this explicitly.
* `useExternalOpenvswitch`: boolean. If the nodes are already running openvswitch, and OpenShiftSDN should not install its own, set this to true. This only needed for certain advanced installations with DPDK or OpenStack.

Example:
```yaml
spec:
  defaultNetwork:
    type: OpenShiftSDN
    openshiftSDNConfig:
      mode: NetworkPolicy
      vxlanPort: 4789
      mtu: 1450
      useExternalOpenvswitch: false
```

## Configuring kube-proxy
Users may customize the kube-proxy configuration. None of these settings are required:

* `iptablesSyncPeriod`: The interval between periodic iptables refreshes. Default: 30 seconds. Increasing this can reduce the number of iptables invocations.
* `bindAddress`: The address to "bind" to - the address for which traffic will be redirected.
* `proxyArguments`: additional command-line flags to pass to kube-proxy - see the [documentation](https://kubernetes.io/docs/reference/command-line-tools-reference/kube-proxy/).

Also, the top-level flag `deployKubeProxy` tells the network operator to explicitly deploy a kube-proxy process. Generally, you will not need to provide this; the operator will decide appropriately. For example, OpenShiftSDN includes an embedded service proxy, so this flag is automatically false in that case.

Example:

```yaml
spec:
  deployKubeProxy: false
  kubeProxyConfig:
   iptablesSyncPeriod: 30s
   bindAddress: 0.0.0.0
   proxyArguments:
     iptables-min-sync-period: ["30s"]
```

# Using
The operator looks for a CRD of type `NetworkConfig` with the name `default`. Whenever this object is created or changed, the operator will apply those changes to the cluster (if they are valid and safe to do so).

You can view and edit that object with
```
oc edit networkconfig default
```

and any changes will be automatically applied.

## Unsafe changes
Most network changes are unsafe to roll out to a production cluster. Therefore, the network operator will stop reconciling if it detects that an unsafe change has been requested. 

### Safe changes to apply:
It is safe to edit the following fields in `NetworkConfig.Spec`:
* deployKubeProxy
* all of kubeProxyConfig

### Force-applying an unsafe change
Administrators may wish to forcefully apply a disruptive change to a cluster that is not serving production traffic. To do this, first they should make the desired configuration change to the CRD. Then, delete the network operator's understanding of the state of the system:

```
oc -n openshift-cluster-network-operator delete configmap applied-default
```

Be warned: this is an unsafe operation! It may cause the entire cluster to lose connectivity or even be permanently broken. For example, changing the ServiceNetwork will cause existing services to be unreachable, as their ServiceIP won't be reassigned.

# Development

## Basic structure

The network operator consists of a controller loop and a rendering system. The basic flow is:

1. The controller loop detects a configuration change
1. The configuration is preprocessed:
    - validity is checked.
    - unspecified values are defaulted or carried forward from the previous configuration.
    - safety of the proposed change is checked.
1. The configuration is rendered in to a set of kubernetes objects (e.g. DaemonSets).
1. The desired objects are reconciled against the API server, being created or updated as necessary.
1. The applied configuration is stored separately, for later comparison.

### Filling defaults
Because most of the operator's configuration parameters are not changeable, it is important that the applied configuration is stable across upgrades. This has two implications:

**All defaults must be made explicit.**

The network configuration is transformed internally in to a fully-expressed struct. All optional values must have their default set. For example, if the vxlanPort is not specified, the default of 4789 is chosen and applied to the OpenShiftSDNConfig.

Making all defaults explicit makes it possible to prevent unsafe changes when a newer version of the operator changes a default value.

**Some values must be carried forward from the previous configuration.**

Some values are computed at run-time but can never be changed. For example, the MTU of the overlay network is determined from the node's interfaces. Changing this is unsafe, so this must always be carried forward.

Note that the fully-expressed configuration is not persisted back to the apiserver. Instead, it is saved only in the stored applied configuration. An alternative would be to inject these values via a mutating webhook. That requires a running service network, which we don't have until after we've run.

### Validation
Each network provider is responsible for validating their view of the configuration. For example, the OpenShiftSDN provider validates that the vxlanPort is a number between 1 and 65535, that the MTU is sane, etc. No validation is provided via the CRD itself.

## Building

### Test builds
Build binaries manually with
```
./hack/build-go.sh
```

There is a special mode, the renderer, that emulates what the operator _would_ apply, given a fresh cluster. You can execute it manually:

```
_output/linux/amd64/cluster-network-renderer --config sample-config.yaml --out out.yaml
```

## Running manually against a test cluster
If you have a running cluster, you can run the operator manually. Just set the `KUBECONFIG` environment variable.

Note that, like all operators installed by the Cluster Version Operator, it determines all pointers to dependent images via environment variables. You will have to set several environment varables to emulate how the CVO works.

You can determine the current values of these environment variables by inspecting the valid deployed daemonset. Do this before you delete it...
```sh
oc get -n openshift-cluster-network-operator daemonset cluster-network-operator -ojsonpath='{range .spec.template.spec.containers[0].env[?(@.value)]}{.name}{"="}{.value}{" "}' | tee images
```

After stopping the deployed operator (see below), you can run the operator locally with
```sh
env POD_NAME=LOCAL $(cat images) _output/linux/amd64/cluster-network-operator
```

### Stopping the deployed operators
If the installer-deployed operator is up and running thanks to the CVO, you will need to stop the CVO, then stop the operator. If you don't stop the CVO, it will quickly re-create the production network operator daemonset.

To do this, just scale the CVO down to 0 replicas and delete the network-operator daemonset.

```sh
oc scale --replicas 0 -n openshift-cluster-version deployments/cluster-version-operator
oc delete -n openshift-cluster-network-operator daemonset cluster-network-operator 
```

## Building images
By default, podman is used to build images. 

```
./hack/build-image.sh
```

You might need sudo:
```
BUILDCMD="sudo podman build" ./hack/build-image.sh
```

Or you could use a docker that supports multi-stage builds
```
BUILDCMD="docker build" ./hack/build-image.sh
```
