# Cluster Network Operator

The Cluster Network Operator installs and upgrades the networking components on an OpenShift Kubernetes cluster.

It follows the [Controller pattern](https://godoc.org/github.com/kubernetes-sigs/controller-runtime/pkg#hdr-Controller): it reconciles the state of the cluster against a desired configuration. The configuration specified by a CustomResourceDefinition called `Network.config.openshift.io/v1`, which has a corresponding [type](https://github.com/openshift/api/blob/master/operator/v1/types_network.go).

Most users will be able to use the top-level OpenShift Config API, which has a [Network type](https://github.com/openshift/api/blob/master/config/v1/types_network.go#L26). The operator will automatically translate the `Network.config.openshift.io` object in to a `Network.operator.openshift.io`.

When the controller has reconciled and all its dependent resources have converged, the cluster should have an installed SDN plugin and a working service network. In OpenShift, the Cluster Network Operator runs very early in the install process -- while the boostrap API server is still running.

# Configuring
The network operator gets its configuration from two objects: the Cluster and the Operator configuration. Most users only need to create the Cluster configuration - the operator will generate its configuration automatically. If you need finer-grained configuration of your network, you will need to create both configurations.

Any changes to the Cluster configuration are propagated down in to the Operator configuration. In the event of conflicts, the Operator configuration will be updated to match the Cluster configuration.

For example, if you want to use the default VXLAN port for OpenShiftSDN, then you don't need to do anything. However, if you need to customize that port, you will need to create both objects and set the port in the Operator config.


#### Configuration objects
*Cluster config*
- *Type Name*: `Network.config.openshift.io`
- *Instance Name*: `cluster`
- *View Command*: `oc get Network.config.openshift.io cluster -oyaml`

*Operator config*
- *Type Name*: `Network.operator.openshift.io`
- *Instance Name*: `cluster`
- *View Command*: `oc get Network.operator.openshift.io cluster -oyaml`

#### Example configurations

*Cluster Config*
```yaml
apiVersion: config.openshift.io/v1
kind: Network
metadata:
  name: cluster
spec:
  clusterNetwork:
  - cidr: 10.128.0.0/14
    hostPrefix: 23
  networkType: OpenShiftSDN
  serviceNetwork:
  - 172.30.0.0/16
```

*Corresponding Operator Config*
This configuration is the auto-generated translation of the above Cluster configuration.
```yaml
apiVersion: operator.openshift.io/v1
kind: Network
metadata:
  name: cluster
spec:
  additionalNetworks: null
  clusterNetwork:
  - cidr: 10.128.0.0/14
    hostPrefix: 23
  defaultNetwork:
    type: OpenShiftSDN
  serviceNetwork:
  - 172.30.0.0/16
```

## Configuring IP address pools
Users must supply at least two address pools - one for pods, and one for services. These are the ClusterNetwork and ServiceNetwork parameter. Some network plugins, such as OpenShiftSDN, support multiple ClusterNetworks. All address blocks must be non-overlapping. You should select address pools large enough to fit your anticipated workload.

For future expansion, multiple `serviceNetwork` entries are allowed by the configuration but not actually supported by any network plugins. Supplying multiple addresses is invalid.

Each `clusterNetwork` entry has an additional required parameter, `hostPrefix`, that specifies the address size to assign to each individual node.  For example,
```yaml
cidr: 10.128.0.0/14
hostPrefix: 23
```
means nodes would get blocks of size `/23`, or 512 addresses.

IP address pools are always read from the Cluster configuration and propagated "downwards" in to the Operator configuration. Any changes to the Operator configuration will be ignored.

Currently, changing the address pools once set is not supported. In the future, some network providers may support expanding the address pools.


Example
```yaml
spec:
  serviceNetwork:
  - "172.30.0.0/16"
  clusterNetwork:
    - cidr: "10.128.0.0/14"
      hostPrefix: 23
    - cidr: "192.168.0.0/18"
      hostPrefix: 23
```

## Configuring the default network provider
Users must select a default network provider. This cannot be changed. Different network providers have additional provider-specific settings.

The network type is always read from the Cluster configuration.

Currently, the only understood value for network Type is `OpenShiftSDN`.

Other values are ignored. If you wish to use use a third-party network provider not managed by the operator, set the network type to something meaningful to you. The operator will not install or upgrade a network provider, but all other Network Operator functionality remains.


### Configuring OpenShiftSDN
OpenShiftSDN supports the following configuration options, all of which are optional:
* `mode`: one of "Subnet" "Multitenant", or "NetworkPolicy". Configures the [isolation mode](https://docs.openshift.com/container-platform/3.11/architecture/networking/sdn.html#overview) for OpenShift SDN. The default is "NetworkPolicy".
* `vxlanPort`: The port to use for the VXLAN overlay. The default is 4789
* `MTU`: The MTU to use for the VXLAN overlay. The default is the MTU of the node that the cluster-network-operator is first run on, minus 50 bytes for overhead. If the nodes in your cluster don't all have the same MTU then you will need to set this explicitly.
* `useExternalOpenvswitch`: boolean. If the nodes are already running openvswitch, and OpenShiftSDN should not install its own, set this to true. This only needed for certain advanced installations with DPDK or OpenStack.

These configuration flags are only in the Operator configuration object.

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

The top-level flag `deployKubeProxy` tells the network operator to explicitly deploy a kube-proxy process. Generally, you will not need to provide this; the operator will decide appropriately. For example, OpenShiftSDN includes an embedded service proxy, so this flag is automatically false in that case.

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
The operator is expected to run as a pod (via a Deployment) inside a kubernetes cluster. It will retrieve the configuration above and reconcile the desired configuration. A suitable manifest for running the operator is located in `manifests/`.

## Unsafe changes
Most network changes are unsafe to roll out to a production cluster. Therefore, the network operator will stop reconciling if it detects that an unsafe change has been requested. 

### Safe changes to apply:
It is safe to edit the following fields in the Operator configuration:
* deployKubeProxy
* all of kubeProxyConfig

### Force-applying an unsafe change
Administrators may wish to forcefully apply a disruptive change to a cluster that is not serving production traffic. To do this, first they should make the desired configuration change to the CRD. Then, delete the network operator's understanding of the state of the system:

```
oc -n openshift-network-operator delete configmap applied-cluster
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

If you want to run the operator as part of an installer run, see INSTALLER-HACKING.md.

If you have a running cluster, you can run the operator locally against that cluster. Just set the `KUBECONFIG` environment variable.

In addition to `KUBECONFIG`, you will also need to set several other variables:

- `NODE_IMAGE` and `HYPERSHIFT_IMAGE` - These are normally set in the operator's environment by the Cluster Version Operator, pointing to the correct versions of the dependent images.
- `KUBERNETES_SERVICE_HOST` and `KUBERNETES_SERVICE_PORT` - The Cluster Network Operator needs to provide the SDN controller pods with the host network address of the Kubernetes apiserver (since the `172.30.0.1` address may not be functioning when the SDN controller starts, and it is currently not possible for the SDN controller to get this information from its node in the way that the SDN node pods do).

You can determine the correct values of these environment variables by inspecting the (working) cluster before you replace the operator:
```sh
oc get -n openshift-network-operator deployment network-operator -ojsonpath='{range .spec.template.spec.containers[0].env[?(@.value)]}{.name}{"="}{.value}{"\n"}' | tee env.sh
oc exec -n openshift-sdn $(oc get pods -n openshift-sdn -l app=sdn-controller -ojsonpath='{.items[0].metadata.name}') -- printenv | grep '^KUBERNETES_SERVICE_[A-Z]*=' | tee -a env.sh
```

After stopping the deployed operator (see below), you can run the operator locally with
```sh
env POD_NAME=LOCAL $(cat env.sh) _output/linux/amd64/cluster-network-operator
```

### Stopping the deployed operators
If the installer-deployed operator is up and running thanks to the CVO, you will need to stop the CVO, then stop the operator. If you don't stop the CVO, it will quickly re-create the production network operator daemonset.

To do this, just scale the CVO down to 0 replicas and delete the network-operator daemonset.

```sh
oc scale --replicas 0 -n openshift-cluster-version deployments/cluster-version-operator
oc delete -n openshift-network-operator deployment network-operator
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
