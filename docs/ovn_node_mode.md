## OVN node modes

The `OVN_NODE_MODE` environment variable is injected into the `ovnkube-node` Pod to identify the node's operational mode. It is consumed by the startup script rendered from `bindata/network/ovn-kubernetes/common/008-script-lib.yaml`.

### Behavior by mode

- `full` (default):
  - `gateway_interface="br-ex"`
  - `init_ovnkube_controller="--init-ovnkube-controller ${K8S_NODE}"`

- `dpu-host`:
  - `gateway_interface="derive-from-mgmt-port"` ([ovn-kubernetes#5327](https://github.com/ovn-kubernetes/ovn-kubernetes/pull/5327))
  - `ovnkube_node_mode="--ovnkube-node-mode dpu-host"`
  - `init_ovnkube_controller=""` (disabled)

### Feature configuration

Feature enablement (egress IP, multicast, multi-network, network segmentation, admin network policy, etc.) is managed through the cluster-wide ConfigMap (`004-config.yaml`) which is passed to ovnkube via `--config-file`. These features are not gated per node mode.
