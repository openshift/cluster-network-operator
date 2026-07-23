# BGP-based VIP management

Feature gate: `BGPBasedVIPManagement` (DevPreviewNoUpgrade). BareMetal
platform only, and only when the Infrastructure CR reports
`status.platformStatus.baremetal.vipManagement: BGP`. Without all three,
everything below is inert and frr-k8s behaves exactly as before.
When all three hold, the FRR additional routing capability provider
(`network.operator.openshift.io/cluster`:
`spec.additionalRoutingCapabilities.providers: [FRR]`) is additionally
required - it ships the FRRConfiguration CRD; without it, rendering fails
explicitly rather than staying inert.

Enhancement: openshift/enhancements#1982.

## What CNO does

When active, CNO renders a single cluster-wide `FRRConfiguration`
(`openshift-frr-k8s/bgp-vip`) from the installer-generated `bgp-vip-config`
ConfigMap:

- The CR spec carries the BGP **sessions** (neighbors, optional BFD).
- VIP **advertisement** is in `rawConfig`: `redistribute table-direct 198`
  filtered to exactly the API/ingress VIP prefixes, plus per-neighbor egress
  permits. kube-vip (rendered by MCO) installs a VIP route into kernel table
  198 only while that node's backend health check passes, so each node
  advertises a VIP only while it can serve it; withdrawal is automatic.
  Advertisement cannot use the CRD's `prefixes`/`toAdvertise` surface: it
  renders unconditional `network` statements and cannot express redistributed
  routes (native support proposed in metallb/frr-k8s#469).
- Configured `communities` are attached to the VIP routes where they enter
  the BGP table (the redistribution route-maps), so every peer receives them.

## Known DevPreview limitations

- `hostOverrides` (per-host peer lists) reach control plane nodes through the
  MCO-rendered per-node peers file — runtimecfg resolves them by short
  hostname at render time. The cluster-wide `FRRConfiguration` intentionally
  carries only `defaultPeers`: expressing per-host sessions as per-node CRs
  would require hostname/label semantics shared across installer, runtimecfg
  and node selectors, which the planned TechPreview structured API addresses.
  CNO logs a warning when overrides are present.
- BGP peer passwords travel from the installer through the `bgp-vip-config`
  ConfigMap into `FRRConfiguration.spec` in plaintext. Moving to
  `passwordSecret` (`kubernetes.io/basic-auth`) requires the installer to
  generate Secrets and is planned alongside the TechPreview structured API.

## Placement

Control plane nodes run an MCO-rendered frr-k8s **static pod** (needed at
bootstrap, before any workload can schedule). The frr-k8s DaemonSet therefore
avoids masters by role under BGP VIP management; on compact/SNO topologies it
correctly matches zero nodes. Workers keep the DaemonSet and advertise the
ingress VIP when they host healthy routers.

## RBAC

The static pod authenticates with the node kubeconfig (the MCO
node-bootstrapper ServiceAccount). `003-static-pod-rbac.yaml` grants the
static pod the read permissions the frr-k8s controller's informers require,
and write access to `FRRNodeState`/`BGPSessionState`. Rendered only under
BGP VIP management.
Per-node write scoping is not expressible in RBAC; a ValidatingAdmissionPolicy
is planned follow-up.
