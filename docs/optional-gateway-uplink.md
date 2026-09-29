# Optional OVN-Kubernetes gateway uplink

The `OVNKubernetesUplinkMode` feature gate enables the
`spec.defaultNetwork.ovnKubernetesConfig.gatewayConfig.uplinkMode` field on the
Network API. The field is supported only when `routingViaHost` is `true`.

`Required`, and an omitted value, preserve the existing behavior: every node
must have a usable gateway uplink. `Optional` allows OVN-Kubernetes gateway
initialization to continue without a physical uplink. It does not create host
routes or interfaces and does not guarantee external connectivity.

Before applying `Optional`, CNO waits for every Linux node that can run
`ovnkube-node`, including cordoned nodes, to advertise
`k8s.ovn.org/uplink-mode-capability: v1`. DPU-mode nodes and Windows nodes are
excluded because they do not run the affected gateway initialization path. The
capability barrier prevents an older `ovnkube-node` binary from receiving an
unsupported `--allow-no-uplink` argument during an upgrade or interrupted
rollout.

Changing the mode updates the rendered `ovnkube-node` configuration and causes
a normal DaemonSet rollout. Moving from `Optional` to `Required` while a node
has no uplink makes gateway initialization fail on that node; restore the
uplink or return the setting to `Optional` to recover.

