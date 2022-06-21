# CNO Architecture

- [CNO Architecture](#cno-architecture)
  - [CNO as SLO](#cno-as-slo)
    - [Install & Upgrade Orderings](#install--upgrade-orderings)
    - [Install payload](#install-payload)
    - [ClusterOperator Status](#clusteroperator-status)
  - [Controllers](#controllers)
  - [Cluster Config Controller](#cluster-config-controller)
  - [Operator Config Controller (Network Controller)](#operator-config-controller-network-controller)
    - [Applied configuration](#applied-configuration)
  - [Egress Router](#egress-router)
  - [Ingress Config](#ingress-config)
  - [Operator PKI](#operator-pki)
  - [Signer controller](#signer-controller)
  - [Proxy Config](#proxy-config)
  - [Configmap CA Injector](#configmap-ca-injector)
  - [Connectivity Check Controller](#connectivity-check-controller)
- [Deriving status](#deriving-status)
    - [Changes needed](#changes-needed)

This document is an overview of CNO's architecture. It is not an authoritative, detailed reference.

The CNO is collection of _controllers_ that, collectively, configure the network of an OpenShift cluster.

Each controller watches a single type of Kubernetes object, called a Kind or GVK (for Group + Version + Kind). It then creates, updates, or deletes some "downstream" objects as appropriate. Most of the interesting logic is in the individual controllers, which are described below.

[Controllers](https://kubernetes.io/docs/concepts/architecture/controller/) are a central concept in Kubernetes, and CNO follows that logical model.


```
     ┌─────────────────┐
     │  ┌──────────┐   │
     │  │          │   │
     │  │ MyKind   │   │
     │  │          │   │
     │  └──────────┘   │
     │   APIServer     │
     └─┬───────────────┘
       │             ▲
 Watch MyKind.Spec   │
       │      Update .Status
       ▼             │
    ┌────────────────┴──┐
    │                   │
    │    Controller     │
    │                   │
    └─┬─────────────────┘
      │              ▲
Create / Update      │
      │        Watch .Status
      ▼              │
    ┌────────────────┴──┐
    │                   │
    │  "Child" objects  │
    │                   │
    └───────────────────┘
```


## CNO as SLO

CNO is a so-called second-level-operator (SLO), which means it is installed by the
Cluster Version Operator (CVO). The CVO has 
[some documentation](https://github.com/openshift/cluster-version-operator/blob/master/docs/dev/operators.md)
about how to interact with it. Most importantly, the CVO will create everything 
in CNO's `/manifests` folder.

All SLOs have to present a unified API: they watch configuration in the `config.openshift.io` API group, and must report their status in a special object called a [ClusterOperator](https://github.com/openshift/api/blob/master/config/v1/types_cluster_operator.go).

### Install & Upgrade Orderings

The CVO has a notion of 
[run levels](https://github.com/openshift/enhancements/blob/master/dev-guide/cluster-version-operator/dev/operators.md#how-do-i-get-added-as-a-special-run-level),
which dictate the order in which components are **upgraded**. Presently, the CNO 
(and thus its operands) are runlevel 07, which is comparatively early. At 
install-time, however, all components are installed at once.

It is important to note that the MCO updates later than the CNO. This means that
any dependent changes in MachineConfigurations (that is to say, [files on disk](https://github.com/openshift/machine-config-operator/tree/master/templates))
will roll out **after** the network. Thus, we need to support running newer
networking components on older machine images.

Before networking is started, nodes are tainted with `node.kubernetes.io/not-ready` 
and `node.kubernetes.io/network-unavailable`. The CNO and any critical operands must
tolerate these taints (as well as `node-role.kubernetes.io/master`, since there are
no worker nodes). As networking comes up, those taints are removed (and the rest
of the cluster's components can start).

During the install process, the CNO will deploy some operands that will initially fail,
as they depend on a component that has not yet been able to run without networking.

### Install payload

The CVO tracks the "real" paths to built images, as those are dependent on build details, CI status, and offline clusters. It passes these to the CNO at runtime via environment variables.

The key takeaway is this: **only reference images provided by the CVO**. You may not reference any other images, even if they're located at `quay.io/openshift`.

### ClusterOperator Status

All SLOs must publish their status to a resource of type ClusterOperator. This is how operators report failing conditions to administrators, as well as blocking upgrades.

There is code to manage generating this status - controllers just need to determine if they are degraded or not.


## Controllers

The individual controllers are all (mostly) independent. If they do communicate, it is generally through the apiserver. That means that each controller could theoretically be a separate process.

Controllers all live in `./pkg/controller/`

## Cluster Config Controller

**Input:** `Network.config.openshift.io/v1`, `Name=cluster` ([typedef](https://github.com/openshift/api/blob/master/config/v1/types_network.go))

The Cluster Config controller reads the high-level configuration object, and applies it "downward" to the detailed network configuration (the poorly-named _Operator Configuration_). Any conflicting fields in the operator configuration will be overwritten.

Conceptually, the Cluster configuration is the networking configuration that universally applies to all clusters. For example, 
it is also consumed by third-party network operators that replace the Network Controller functionality of the CNO.

## Operator Config Controller (Network Controller)

**Input:** `Network.operator.openshift.io/v1` with `Name=cluster` ([typedef](https://github.com/openshift/api/blob/master/operator/v1/types_network.go))

**Output:** Most core networking components.

For more detailed documentation, see [operands.md](https://github.com/openshift/cluster-network-operator/blob/master/docs/operands.md).

This is the "main" controller, in that it is responsible for rendering the core networking components (Openshift-SDN / OVN-Kubernetes / Kuryr, Multus, etc). It is broken down into stages:

1. **Validate** - Check the supplied configuration.
2. **Fill** - Determine any unsupplied default values.
3. **Check** - Compare against previously-applied configuration, to see if any unsafe changes are proposed
4. **Bootstrap** - gather existing cluster state, and create any non-Kubernetes resources (i.e. OpenStack objects)
5. **Render** - process template files in `/bindata` and generate Kubernetes objects
6. **Apply** - Create or update objects in the APIServer. Delete any un-rendered objects.

### Applied configuration

The Network operator needs to make sure that the input configuration doesn't change unsafely, since we don't support rolling out most changes. To do that, it writes a ConfigMap with the applied changes. It then compares the existing configuration with the desired configuration, and sets a status of `Degraded` if it is asked to do something unsafe.

The persisted configuration must **make all defaults explicit**. This protects against inadvertent code changes that could destabilize an existing cluster.

## Egress Router

**Input:** `EgressRouter.network.operator.openshift.io`
**Output:** EgressRouters (a Deployment and a NetworkAttachmentDefinition)

See the [enhancement proposal](https://github.com/openshift/enhancements/blob/master/enhancements/network/egress-router.md)

The EgressRouter is a feature that spins up a container with a MacVLAN secondary interface. Other containers can then NAT their traffic through that interface. The routing is handled via OVN-Kubernetes, but there needs to be a container to hold the interface. This controller watches for EgressRouter CRs and creates / deletes pods as required.

## Ingress Config

**Input:** `IngressController.operator.openshift.io`
**Output:** A label

See the [enhancement proposal](https://github.com/openshift/enhancements/blob/master/enhancements/network/allow-from-router-networkpolicy.md)

The Ingress Config controller ensures that end-users can grant access to Routers, even when they are located in host-network pods. If the IngressController reports host-network pods, then the controller will add the label `policy-group.network.openshift.io/ingress=""` to the special host-network holding namespace.

## Operator PKI

**Input:** `PKI.network.operator.openshift.io`
**Output:** Signed keypairs, distributed via Secrets and ConfigMaps

This is a small controller that manages a PKI : it creates a CA and a single certificate signed by that CA. It is used for OVN PKI - it is not intended to be created by end-users. It is used by the Network controller for OVN-Kubernetes, as well as the Signer controller for OVN-Kubernetes ipsec.

Note, CNO and core networking components cannot use the `service-ca-operator`, as that operator requires a functioning pod network.

## Signer controller

**Input:** `CertificateSigningRequest`
**Output:** `CertificateSigningRequest .Status`

The Signer controller signs CertificateSigningRequests with a Signer of `network.openshift.io/signer`.  These CSRs are generated by a DaemonSet on each node that manages IPSec.

The PKI is created by the Operator PKI controller.

## Proxy Config

**Input:** `Proxy.config.openshift.io`, all ConfigMaps in the `openshift-config` Namespace
**Output:** `Proxy.config.openshift.io .Status`, ConfigMap `openshift-config-managed/trusted-ca-bundle`

See the [enhancement proposal](https://github.com/openshift/enhancements/blob/master/enhancements/proxy/global-cluster-egress-proxy.md).

The ProxyConfig controller has two functions:

- Derive the Proxy Status from the Spec, merging in other variables from the cluster configuration. This includes properties such as NO_PROXY.
- Generate a CA bundle with all CAs merged (which is consumed by the CA injector). CAs are read from ConfigMaps as well as system trust.

## Configmap CA Injector

**Input:** Configmap `openshift-config-managed/trusted-ca-bundle` **and** all with the label `config.openshift.io/inject-trusted-cabundle = true`

**Output:** Configmaps

This controller is used for distributing certificates across the cluster. It watches ConfigMaps with a specific label. Any CM with that label will have the CA bundle injected.

## Connectivity Check Controller

TODO

# Deriving status

All controllers should report a status back. The controllers in the CNO are no different.

The CNO derives status in two ways:

- Controllers can report a Degraded / non-Degraded state
- A special Status Controller watches Pods, Deployments, and Daemonsets and derives status from them. This is only used for pods created by the Network controller. It is from this that we determine the "Available" and "Progressing" statuses. There is [more detailed documentation](https://github.com/openshift/cluster-network-operator/blob/master/docs/operands.md).

Status is posted to both the `Network.operator.openshift.io` object, as well as the network `ClusterOperator.config.openshift.io` object, and the two statuses are currently identical.

### Changes needed

The Status-generating infrastructure in the CNO was written before it had multiple control loops. Correct behavior would be to separate status per-controller, and only publish network-controller status to the `Network.operator` object. This would reflect the logical structure more cleanly.