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

Feature enablement is managed through two mechanisms:

- **ConfigMap-based** (`004-config.yaml`): Most features (egress IP, multi-network, network segmentation, admin network policy, etc.) are configured in the cluster-wide ConfigMap which is passed to ovnkube via `--config-file`.

- **CLI flags** (`ovnkube-control-plane.yaml`): Features that require ovnkube-control-plane pod restarts on configuration changes (multicast, multi-networkpolicy) are enabled via CLI flags (e.g., `--enable-multicast`, `--enable-multi-networkpolicy`) to ensure the control-plane pods restart automatically when the feature is toggled. Note that ovnkube-node pods already restart when the ConfigMap changes, so only control-plane-specific features require CLI flags.

These features are not gated per node mode.
