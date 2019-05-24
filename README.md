# Cluster Network Operator

The Cluster Network Operator installs and upgrades the networking components on an OpenShift Kubernetes cluster.

It follows the [Controller pattern](https://godoc.org/github.com/kubernetes-sigs/controller-runtime/pkg#hdr-Controller): it reconciles the state of the cluster against a desired configuration. The configuration specified by a CustomResourceDefinition called `Network.config.openshift.io/v1`, which has a corresponding [type](https://github.com/openshift/api/blob/master/operator/v1/types_network.go).

Most users will be able to use the top-level OpenShift Config API, which has a [Network type](https://github.com/openshift/api/blob/master/config/v1/types_network.go#L26). The operator will automatically translate the `Network.config.openshift.io` object in to a `Network.operator.openshift.io`.

When the controller has reconciled and all its dependent resources have converged, the cluster should have an installed network plugin and a working service network. In OpenShift, the Cluster Network Operator runs very early in the install process -- while the boostrap API server is still running.

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

Alternatively, ovn-kubernetes can be configured by setting `networkType: OVNKubernetes`.

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

(For ovn-kubernetes, `type: OVNKubernetes`.)

## Configuring IP address pools
Users must supply at least two address pools - one for pods, and one for services. These are the ClusterNetwork and ServiceNetwork parameter. Some network plugins, such as OpenShiftSDN and OVNKubernetes, support multiple ClusterNetworks. All address blocks must be non-overlapping. You should select address pools large enough to fit your anticipated workload. Each pool must be able to hold 1 or more hostPrefix allocations.

For future expansion, multiple `serviceNetwork` entries are allowed by the configuration but not actually supported by any network plugins. Supplying multiple addresses is invalid.

Each `clusterNetwork` entry has an additional required parameter, `hostPrefix`, that specifies the address size to assign to each individual node.  For example,
```yaml
cidr: 10.128.0.0/14
hostPrefix: 23
```
means nodes would get blocks of size `/23`, or 512 addresses.

IP address pools are always read from the Cluster configuration and propagated "downwards" into the Operator configuration. Any changes to the Operator configuration are ignored.

Currently, changing the address pools once set is not supported. In the future, some network providers may support expanding the address pools.


Example:
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

Currently, the only understood values for network Type are `OpenShiftSDN` and `OVNKubernetes`.

Other values are ignored. If you wish to use use a third-party network provider not managed by the operator, set the network type to something meaningful to you. The operator will not install or upgrade a network provider, but all other Network Operator functionality remains.

### Adding chained plugins
You can add raw CNI configuration snippets to be installed as CNI chained plugins. These will be included in the CNI configuration file used by the default network provider. Note that these are *not* multus multiple networks. Rather, they are for plugins that manipulate the container's existing network. For example, if you want to adjust your container's sysctls, you can configure

```yaml
spec:
  defaultNetwork:
    type: OpenShiftSDN
    openshiftSDNConfig: {}
    chainedPlugins:
      - '{"name": "tuning", "sysctl": { "net.core.somaxconn": "500"}}`
```

Not all default networks support chained plugins. Currently, only OpenShiftSDN does.

### Configuring OpenShiftSDN
OpenShiftSDN supports the following configuration options, all of which are optional:
* `mode`: one of "Subnet" "Multitenant", or "NetworkPolicy". Configures the [isolation mode](https://docs.openshift.com/container-platform/3.11/architecture/networking/sdn.html#overview) for OpenShift SDN. The default is "NetworkPolicy".
* `vxlanPort`: The port to use for the VXLAN overlay. The default is 4789
* `MTU`: The MTU to use for the VXLAN overlay. The default is the MTU of the node that the cluster-network-operator is first run on, minus 50 bytes for overhead. If the nodes in your cluster don't all have the same MTU then you will need to set this explicitly.
* `useExternalOpenvswitch`: boolean. If the nodes are already running openvswitch, and OpenShiftSDN should not install its own, set this to true. This only needed for certain advanced installations with DPDK or OpenStack.

These configuration flags are only in the Operator configuration object.

Example:
```yaml

```

### Configuring OVNKubernetes
OVNKubernetes supports the following configuration options, all of which are optional:
* `MTU`: The MTU to use for the geneve overlay. The default is the MTU of the node that the cluster-network-operator is first run on, minus 100 bytes for geneve overhead. If the nodes in your cluster don't all have the same MTU then you may need to set this explicitly.

These configuration flags are only in the Operator configuration object.

Example:
```yaml
spec:
  defaultNetwork:
    type: OVNKubernetes
    ovnKubernetesConfig:
      mtu: 1400
```

## Configuring kube-proxy
Some plugins (like OpenShift SDN) have a built-in kube-proxy, some plugins require a standalone kube-proxy to be deployed, and some (like ovn-kubnernetes) don't use kube-proxy at all.

The deployKubeProxy flag can be used to indicate whether CNO should deploy a standalone kube-proxy, but for supported network types, this will default to the correct value automatically.

The configuration here can be used for third-party plugins with a separate kube-proxy process as well.

For plugins that use kube-proxy (whether built-in or standalone), you can configure the proxy via kubeProxyConfig


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
See HACKING.md
