## OVN node modes and per-node feature enforcement

This change introduces `OVN_NODE_MODE` as an environment variable injected into the `ovnkube-node` Pod. The value is consumed by the startup script rendered from `bindata/network/ovn-kubernetes/common/008-script-lib.yaml` to tailor behavior per node mode at runtime.

### Why move flags from the config map into the script?

- The INI-based config (`004-config.yaml`) is rendered cluster-wide. Those values are not reliably overridable on a per-node or per-mode basis.
- In DPU host mode, some features are not supported and must be deterministically disabled on those nodes even if the cluster-wide config enables them.
- Moving the enablement logic to the entrypoint script allows per-node enforcement using `OVN_NODE_MODE`, preventing unsupported features from being turned on by cluster defaults.

### Behavior by mode

- `full` (default):
  - `gateway_interface=br-ex`
  - `init_ovnkube_controller="--init-ovnkube-controller ${K8S_NODE}"`
  - `enable_multicast_flag="--enable-multicast"`
  - `egress_features_enable_flag="--enable-egress-ip=true --enable-egress-firewall=true --enable-egress-qos=true --enable-egress-service=true"`
  - `multi_external_gateway_enable_flag="--enable-multi-external-gateway=true"`

- `dpu-host`:
  - `gateway_interface="derive-from-mgmt-port"`
  - `ovnkube_node_mode="--ovnkube-node-mode dpu-host"`
  - `init_ovnkube_controller=""` (disabled)
  - `enable_multicast_flag=""` (disabled)
  - `egress_features_enable_flag=""` (egress IP and related features disabled)
  - `multi_external_gateway_enable_flag=""` (multi-external gateway disabled)
  - Multi-network, network segmentation, and multi-network policy/admin network policy are gated and not enabled in this mode.

### Manifests

- `ovnkube-node.yaml` (managed and self-hosted) now inject `OVN_NODE_MODE` into the Pod env so the script can apply mode-aware logic.
- `ovnkube-control-plane.yaml` (managed and self-hosted) have feature flags moved from ConfigMap to inline script logic.
- `004-config.yaml` drops hard-coded feature enables that conflict with per-node enforcement.

**Note**: Control-plane components always run in "full" mode since they don't run on DPU hosts and need all features enabled for cluster coordination. Always-enabled features (egress, multicast, multi-external-gateway) are added directly to the command line, while conditional features use script variables.

### Implementation Details

#### Environment Variable Injection

The `OVN_NODE_MODE` environment variable is injected into `ovnkube-node` pods through the DaemonSet specification in both managed and self-hosted variants:

- `bindata/network/ovn-kubernetes/managed/ovnkube-node.yaml`
- `bindata/network/ovn-kubernetes/self-hosted/ovnkube-node.yaml`

The value is typically derived from node labels or annotations that identify the node's hardware type.

#### Script Logic Flow

The startup script (`008-script-lib.yaml`) implements the following conditional logic:

```bash
if [[ "${OVN_NODE_MODE}" != "dpu-host" ]]; then
    # Enable features for full mode
    egress_ip_enable_flag="--enable-egress-ip=true --enable-egress-firewall=true --enable-egress-qos=true --enable-egress-service=true"
    enable_multicast_flag="--enable-multicast"
    # ... other feature flags
else
    # DPU host mode - disable features
    egress_ip_enable_flag=""
    enable_multicast_flag=""
    gateway_interface="derive-from-mgmt-port"
    ovnkube_node_mode="--ovnkube-node-mode dpu-host"
fi
```

#### Feature Flag Mapping

The following table shows how cluster-wide configuration translates to per-node enforcement:

| Feature | ConfigMap (004-config.yaml) | Script Variable | DPU Host Behavior |
|---------|----------------------------|-----------------|-------------------|
| Egress IP | `enable-egress-ip=true` | `egress_features_enable_flag` | Force disabled |
| Multicast | `enable-multicast=true` | `enable_multicast_flag` | Force disabled |
| Multi External Gateway | `enable-multi-external-gateway=true` | `multi_external_gateway_enable_flag` | Force disabled |
| Multi-network | `enable-multi-network=true` | `multi_network_enabled_flag` | Conditionally disabled |
| Admin Network Policy | `enable-admin-network-policy=true` | `admin_network_policy_enabled_flag` | Conditionally disabled |
| Network Segmentation | `enable-network-segmentation=true` | `network_segmentation_enabled_flag` | Conditionally disabled |

### Testing

- Unit tests assert that the rendered script contains the correct assignments for `gateway_interface`, `init_ovnkube_controller`, `enable_multicast_flag`, `egress_features_enable_flag`, and `ovnkube_node_mode` across modes.
- The comprehensive test `TestOVNKubernetesScriptLibCombined` validates all conditional logic paths and feature flag assignments for node scripts.
- The test `TestOVNKubernetesControlPlaneFlags` validates that control-plane scripts have:
  - Always-enabled features added directly to the command line (egress, multicast, multi-external-gateway)
  - Conditional features handled via script variables (multi-network, network policies, etc.)
  - Correct multi-network enablement logic (OVN_MULTI_NETWORK_ENABLE)
- Tests verify both positive cases (features enabled in full mode) and negative cases (features disabled in DPU host mode).

### Migration Notes

When upgrading clusters that previously relied on ConfigMap-based feature control:

1. Existing ConfigMap values in `004-config.yaml` have been removed for features that require per-node control
2. The startup scripts (both node and control-plane) now contain the authoritative feature enablement logic
3. Control-plane components automatically enable all features (always run in "full" mode)
4. DPU host nodes will automatically have incompatible features disabled regardless of previous ConfigMap settings
5. No manual intervention is required - the migration is handled automatically during the upgrade process


