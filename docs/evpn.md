# EVPN Feature Gate Removal

## Overview

As of this release, EVPN (Ethernet VPN) support has graduated from TechPreview to General Availability (GA). The EVPN feature gate has been removed, making EVPN functionality always available for cluster user-defined networks.

## What Changed

### Before (Feature-Gated)

- EVPN was controlled by the `EVPN` feature gate
- CRD fields were conditionally present based on feature gate state
- VTEP CRD and RBAC were only deployed when the feature gate was enabled
- Required explicit enablement through feature gate configuration

### After (GA)

- EVPN is always available
- No feature gate configuration required
- CRD fields are always present in the ClusterUserDefinedNetwork schema
- VTEP CRD is always deployed
- `--enable-evpn` flag is always passed to ovnkube-control-plane

## Changes to ClusterUserDefinedNetwork CRD

The following fields are now always present in the `ClusterUserDefinedNetwork` CRD:

### EVPN Configuration Fields

- **`spec.network.evpn`** - EVPN configuration for Layer 2 and Layer 3 networks
  - `spec.network.evpn.vtep` - VTEP CR name (required when using EVPN)
  - `spec.network.evpn.ipVRF` - IP-VRF configuration for Layer 3 EVPN
    - `spec.network.evpn.ipVRF.vni` - Virtual Network Identifier (required, 1-16777215)
    - `spec.network.evpn.ipVRF.routeTarget` - Route target (optional, auto-generated if omitted)
  - `spec.network.evpn.macVRF` - MAC-VRF configuration for Layer 2 EVPN
    - `spec.network.evpn.macVRF.vni` - Virtual Network Identifier (required, 1-16777215)
    - `spec.network.evpn.macVRF.routeTarget` - Route target (optional, auto-generated if omitted)

### Transport Field

- **`spec.network.transport`** - Transport type, now includes `EVPN` as a valid value
  - Allowed values: `NoOverlay`, `EVPN`

## VTEP CRD

The `vteps.k8s.ovn.org` CustomResourceDefinition is now always deployed. This CRD represents VXLAN Tunnel Endpoints used for EVPN transport.

## RBAC Changes

RBAC permissions for VTEP resources are now always present:

- **Node roles**: list, get, watch permissions for vteps resources
- **Control plane roles**: list, get, watch, update, patch permissions for vteps resources and vteps/status

## Upgrade Considerations

### Upgrading from Feature-Gated EVPN

When upgrading from a version where EVPN was feature-gated:

1. **CRD Schema Expansion**: The ClusterUserDefinedNetwork CRD schema will expand to include EVPN fields. Existing resources without EVPN configuration are unaffected.

2. **VTEP CRD Deployment**: The VTEP CRD will be deployed during upgrade if not already present.

3. **RBAC Changes**: VTEP permissions will be added to cluster roles.

4. **Runtime Behavior**: The `--enable-evpn` flag will be added to ovnkube-control-plane. This does not affect existing networks unless they explicitly configure EVPN transport.

5. **Control Plane Impact**: Adding the `--enable-evpn` flag changes the ovnkube-control-plane pod template, triggering a pod restart. For self-hosted deployments with RollingUpdate strategy (maxUnavailable: 1, maxSurge: 0), a single-replica control plane may experience a brief availability gap during pod replacement. This represents a potential control-plane unavailability window, though existing data-plane traffic continues uninterrupted.

### Backward Compatibility

- Existing ClusterUserDefinedNetwork resources without EVPN configuration continue to work unchanged
- EVPN fields are optional and only used when `transport: EVPN` is specified
- Default transport remains the standard OVN overlay (Geneve/VXLAN as configured by ovn-encap-type)
- Networks that don't specify `transport: EVPN` are unaffected

## Validation Rules

The CRD includes validation rules to ensure correct EVPN configuration:

- EVPN transport is only allowed for Layer2 or Layer3 primary networks
- When `transport: EVPN` is set, the `evpn` field is required
- For Layer2 topology with EVPN, `evpn.macVRF` is required
- For Layer3 topology with EVPN, `evpn.ipVRF` is required
- macVRF and ipVRF cannot use the same VNI (Virtual Network Identifier)

## Using EVPN

EVPN is opt-in per user-defined network. To use EVPN transport:

1. Set `spec.network.transport` to `EVPN` in your ClusterUserDefinedNetwork
2. Configure the `spec.network.evpn` field with appropriate VRF settings
3. Ensure Route Advertisements is enabled and properly configured

For detailed EVPN configuration instructions, including prerequisites and examples, refer to the upstream documentation.

## Additional Resources

- [OVN-Kubernetes EVPN Documentation](https://ovn-kubernetes.io/master/features/bgp-integration/evpn/)
- [User-Defined Networks](https://docs.openshift.com/container-platform/latest/networking/ovn_kubernetes_network_provider/user-defined-networks.html)
