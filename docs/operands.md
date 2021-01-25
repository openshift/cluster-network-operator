# Cluster Network Operator Operands

## General Notes

The data for rendering CNO operands is in `bindata/`. As with
`manifests/`, the objects within a given operand directory are created
in lexicographic order. Objects that must be created first, in a
specific order, should have numbered prefixes, while the remaining
objects can be unnumbered. Most operand directories start with a
`000-ns.yaml` object to create the operand namespace before anything
else is created. (Some reuse the namespace created by another operand,
and depend on the rendering code to render the two operands in the
right order.)

Some operands require creating objects of Custom Resource types that
are defined by other OCP operators. Since the CRDs for these types may
not have been created yet when CNO starts, it may not be possible to
create those objects right away. You can annotate the objects with

    networkoperator.openshift.io/ignore-errors: ""

to indicate that CNO should ignore errors when creating them. (It will
then retry creating them every few minutes, until eventually it
succeeds.)

After rendering all of the objects specified by the network
configuration, the `StatusManager` will begin monitoring any
`DaemonSet`s and `Deployment`s included among the rendered objects,
and update the status of the "network" `ClusterOperator` based on the
combined status of those objects. Specifically:

  - If any operand is currently rolling out pods (or trying and
    failing to roll out pods), the operator will be `Progressing`:

        - type: Progressing
          status: "True"
          reason: Deploying
          message: |-
            DaemonSet "openshift-multus/multus" is not available (awaiting 3 nodes)
            DaemonSet "openshift-multus/network-metrics-daemon" is waiting for other operators to become ready
            DaemonSet "openshift-multus/multus-admission-controller" is waiting for other operators to become ready
            DaemonSet "openshift-sdn/sdn-controller" is not available (awaiting 3 nodes)
            DaemonSet "openshift-sdn/ovs" is not available (awaiting 3 nodes)
            DaemonSet "openshift-sdn/sdn" is not available (awaiting 3 nodes)
            DaemonSet "openshift-network-diagnostics/network-check-target" is not available (awaiting 3 nodes)
            Deployment "openshift-network-diagnostics/network-check-source" is waiting for other operators to become ready
          lastTransitionTime: "2020-12-11T14:24:32Z"

  - If an operand fails to roll out for too long, the operator will
    be `Degraded`:

        - type: Degraded
          status: "True"
          reason: RolloutHung
          message: |-
            DaemonSet "openshift-multus/multus" is not making progress - last change 2020-12-11T14:14:32Z"
          lastTransitionTime: "2020-12-11T14:24:32Z"

  - Once all operands report that they are fully availble, the
    operator will become `Ready`.

CNO starts up early during the install process, but some of its
operands cannot run successfully until much later in the install
process:

  - Pods that do not run on masters will not be able to start until
    the worker nodes are created.

  - Pods that use the
    `service.beta.openshift.io/serving-cert-secret-name` Service
    annotation or the `service.beta.openshift.io/inject-cabundle=true`
    ConfigMap annotation will not be able to start until after the
    Service CA Operator starts (which depends on the pod network being
    up).

To prevent such pods from confusing the CNO's status, you should
annotate any DaemonSet or Deployment that will start up "late" in the
install process with:

    networkoperator.openshift.io/non-critical: ""

This will prevent them from triggering the "`RolloutHung`" status (and
will also change the way that they appear in the "`Deploying`" status,
to make it clear that they're "`Progressing`" rather than "`Ready`"
because they depend on other things that aren't ready yet).

## Network Plugins

CNO renders (at most) one of `bindata/network/openshift-sdn`,
`bindata/network/ovn-kubernetes`, or `bindata/network/kuryr`,
depending on the `spec.defaultNetwork.type` of the
`network.operator.openshift.io` configuration object (which in turn is
copied there from the `.spec.networkType` of the
`network.config.openshift.io` configuration). If the specified network
type is not one of "`OpenShiftSDN`", "`OVNKubernetes`", or "`Kuryr`",
the CNO will not render any network plugin.

Note that the CRDs for the `network.openshift.io` types
(`ClusterNetwork`, `HostSubnet`, `NetNamespace`, and
`EgressNetworkPolicy`) are defined in
`bindata/network/openshift-sdn/001-crd.yaml` and so are only created
when using OpenShift SDN.

## Multus

Multus is deployed as long as `.spec.disableMultiNetwork` is not set.
The `multus` DaemonSet copies all of the (non-network-plugin-specific)
CNI plugin binaries onto each node. Currently this is:

  - multus
  - cni-plugins
  - egress-router-cni
  - route-override
  - whereabouts

(SR-IOV is not configured from CNO; it has its own operator.)

The daemonset does not do anything after init time, but needs to keep
running because there is no concept of an "`initContainer`-only" pod.

Multus-admission-controller is a simple admission controller that
checks the Multus-related annotations on Pods, to provide better error
messages when they are wrong. (The cluster will operate fine without
multus-admission-controller; it is not needed for security or functionality. It
just makes the error handling nicer, by causing certain failures to
happen at pod creation time rather than being reported asynchronously
after pod creation.)

The network-metrics-daemon gathers metrics about Multus-created
network interfaces, to provide to Prometheus.

## Kube-proxy

If `.spec.deployKubeProxy` is `true`, CNO will deploy a standalone
`kube-proxy` DaemonSet. (This defaults to `false` for the three
"built-in" network plugins, and `true` for any other plugin.) Some of
the `kube-proxy` options can be configured via
`.spec.kubeProxyConfig`. In particular,
`.spec.kubeProxyConfig.proxyArgs` is a map from kube-proxy
command-line option name (without the initial dashes) to an array of
values.

## Network diagnostics

Network diagnostics consists of a single `network-check-source` pod
and a DaemonSet `network-check-target` that deploys a pod to every
node. The source pod regularly tries to connect to each target pod,
plus additional other targets such as the kube-apiserver and
openshift-apiserver pods, and reports when they are unreachable.
