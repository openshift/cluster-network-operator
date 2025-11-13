# DPU Host Mode

## Overview

DPU (Data Processing Unit) host mode is a cluster-wide feature that automatically disables certain OVN-Kubernetes networking features when DPU hardware is detected in the cluster. This ensures consistent network behavior across all nodes, as DPU hardware has specific limitations that require certain features to be disabled.

## How It Works

### 1. Detection

The cluster network operator automatically detects DPU nodes by looking for nodes with the DPU mode label:
- **Label**: `network.operator.openshift.io/dpu`
- **Detection Point**: `pkg/network/ovn_kubernetes.go` in the `bootstrapOVNConfig()` function

```go
// Detect if DPU nodes are present in cluster
ovnConfigResult.DpuHostModeEnabled = len(ovnConfigResult.DpuModeNodes) > 0
```

When **any** DPU node is found in the cluster, DPU host mode is enabled **cluster-wide**.

### 2. Feature Disabling

When DPU host mode is enabled (`DpuHostModeEnabled = true`), the following features are automatically disabled across the **entire cluster**:

#### Egress Features (Template-controlled)
These features are controlled by the `DPU_HOST_MODE_ENABLED` flag in ConfigMap templates:
- ❌ Egress IP
- ❌ Egress Firewall
- ❌ Egress QoS
- ❌ Egress Service

#### Network Features (Go-controlled)
These features are disabled directly in Go code when DPU mode is detected:
- ❌ Multi-network support (`OVN_MULTI_NETWORK_ENABLE`)
- ❌ Network segmentation (`OVN_NETWORK_SEGMENTATION_ENABLE`)
- ❌ Multi-network policies (`OVN_MULTI_NETWORK_POLICY_ENABLE`)
- ❌ Admin network policies (`OVN_ADMIN_NETWORK_POLICY_ENABLE`)
- ❌ Multicast support (`OVN_MULTICAST_ENABLE`)

### 3. Implementation Architecture

The implementation uses a hybrid approach with two control mechanisms:

#### Go Code Control (Primary)
Location: `pkg/network/ovn_kubernetes.go` (lines 394-401)

```go
// Disable all DPU-incompatible features when DPU host mode enabled
if dpuHostModeEnabled {
    // Disable feature gates that are incompatible with DPU
    data.Data["OVN_ADMIN_NETWORK_POLICY_ENABLE"] = false
    data.Data["OVN_NETWORK_SEGMENTATION_ENABLE"] = false
    data.Data["OVN_MULTI_NETWORK_ENABLE"] = false
    data.Data["OVN_MULTI_NETWORK_POLICY_ENABLE"] = false
    data.Data["OVN_MULTICAST_ENABLE"] = false
}
```

These flags are set to `false` when DPU nodes are present, overriding any feature gate settings.

#### Template Control (Egress Features)
Location: `bindata/network/ovn-kubernetes/{managed,self-hosted}/004-config.yaml`

```ini
[ovnkubernetesfeature]
{{- if not .DPU_HOST_MODE_ENABLED }}
enable-egress-ip=true
enable-egress-firewall=true
enable-egress-qos=true
enable-egress-service=true
{{- end }}
```

Egress features are only rendered in the ConfigMap when `DPU_HOST_MODE_ENABLED` is false.

## Node Configuration

### DPU Host Nodes
Nodes running in DPU host mode have special configuration:
- **OVN Node Mode**: Set to `dpu-host`
- **Gateway Interface**: Uses `derive-from-mgmt-port` instead of `br-ex`
- **Management Port**: Configured via `OVNKUBE_NODE_MGMT_PORT_NETDEV`

### Regular Nodes
When DPU nodes are present, regular (full mode) nodes still run in `full` mode but with the same features disabled to maintain cluster consistency.

## Cluster-Wide Behavior

| Scenario | Feature State | Node Modes | Consistency |
|----------|---------------|------------|-------------|
| **No DPU nodes** | All features enabled | All nodes: `full` | ✅ Perfect |
| **DPU nodes present** | DPU-incompatible features disabled | DPU: `dpu-host`<br>Others: `full` | ✅ Perfect |

**Key Point**: Feature disabling is cluster-wide, not per-node. This ensures consistent behavior regardless of which node is handling traffic.

## Configuration Files Affected

### ConfigMaps
- `bindata/network/ovn-kubernetes/managed/004-config.yaml`
- `bindata/network/ovn-kubernetes/self-hosted/004-config.yaml`

### Scripts
- `bindata/network/ovn-kubernetes/common/008-script-lib.yaml`

### Control Plane
- `bindata/network/ovn-kubernetes/managed/ovnkube-control-plane.yaml`
- `bindata/network/ovn-kubernetes/self-hosted/ovnkube-control-plane.yaml`

### Node DaemonSets
- `bindata/network/ovn-kubernetes/managed/ovnkube-node.yaml`
- `bindata/network/ovn-kubernetes/self-hosted/ovnkube-node.yaml`

## Testing

Comprehensive tests for DPU host mode are located in:
- `pkg/network/ovn_kubernetes_test.go`
- Test function: `TestOVNKubernetesDpuHostMode`

The tests verify:
- ✅ DPU node detection
- ✅ Feature disabling when DPU nodes are present
- ✅ Normal operation when no DPU nodes exist
- ✅ Multiple DPU node scenarios
- ✅ Gateway interface configuration
- ✅ ConfigMap rendering
- ✅ Script template rendering

Run tests with:
```bash
go test -v -run TestOVNKubernetesDpuHostMode ./pkg/network/
```

## Troubleshooting

### Check if DPU Mode is Enabled

Check the cluster-network-operator logs for:
```
DPU host mode enabled - DPU-incompatible features will be disabled cluster-wide (N DPU nodes found)
```

### Verify DPU Node Labels

List nodes with DPU label:
```bash
kubectl get nodes -l network.operator.openshift.io/dpu
```

### Check Feature State

Examine the `ovnkube-config` ConfigMap:
```bash
kubectl get configmap ovnkube-config -n openshift-ovn-kubernetes -o yaml
```

Look for the absence of disabled features (they won't appear in the ConfigMap when disabled).

## Design Decisions

### Why Cluster-Wide?
DPU hardware limitations require certain features to be disabled. Enabling these features on some nodes but not others would create:
- Inconsistent behavior depending on which node handles traffic
- Potential connectivity issues when traffic moves between nodes
- Complex troubleshooting scenarios

Therefore, when **any** DPU node is present, features are disabled **cluster-wide** to ensure predictable behavior.

### Why Hybrid Approach?
- **Go Control**: Provides type safety, testability, and centralized logic for feature gates
- **Template Control**: Maintains backward compatibility for egress features that have always been template-controlled

This hybrid approach balances clean architecture with minimal changes to existing, stable code paths.

## Future Enhancements

Potential future improvements:
1. Migrate egress features to pure Go control for consistency
2. Add webhook validation to prevent users from enabling incompatible features
3. Add status reporting to show which features are disabled and why
4. Support per-node feature enablement for non-DPU nodes (advanced use case)

## References

- [Architecture Documentation](architecture.md)
- [Operands Documentation](operands.md)
- OpenShift OVN-Kubernetes operator source code


