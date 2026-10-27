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
(`openshift-frr-k8s/bgp-vip`) from the `BGPVIPConfig` singleton
(`bgpvipconfigs.machineconfiguration.openshift.io/cluster`, openshift/api#2972)
that the installer generates from `platform.baremetal.bgpVIPConfig` and that
is editable on day 2; the API and Ingress VIPs come from the Infrastructure
status. The operconfig controller watches the CR (dynamic informer, only
when the feature gate is enabled — the CRD ships behind it) so a spec change
re-renders the FRRConfiguration without a restart.

- The CR spec carries the BGP **sessions** (neighbors, optional BFD):
  `failureDetection: BFD` becomes the `vip-bfd` profile, `peerReachability:
  MultiHop` becomes `ebgpMultiHop`, the timers become `metav1.Duration`
  strings; omitted fields stay omitted so FRR's defaults apply.
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
- A peer's `passwordSecret` names a `kubernetes.io/basic-auth` Secret in
  `openshift-config` (`password` key, at most 80 bytes — the TCP MD5 limit);
  CNO resolves it at render time. Errors name the Secret, never its contents.
- The render outcome is reported on the CR's `SessionsConfigured` condition
  through server-side apply (field manager `cluster-network-operator`,
  owning only that condition; `Rendered` belongs to the
  machine-config-operator): `True/AsExpected` once the FRRConfiguration is
  rendered for the observed generation, `False/RenderFailed` with the error
  otherwise. A missing CR renders nothing and writes no condition.

## Known limitations

- `hostOverrides` (per-host peer lists) reach control plane nodes through the
  MCO-rendered per-node peers file — runtimecfg resolves them by short
  hostname at render time. The cluster-wide `FRRConfiguration` intentionally
  carries only `defaultPeers`: expressing per-host sessions as per-node CRs
  would require hostname/label semantics shared across installer, runtimecfg
  and node selectors. CNO logs a warning when overrides are present.
- Peer passwords are rendered **inline** into `FRRConfiguration.spec`, as
  MetalLB does, instead of as `passwordSecret` references: the frr-k8s
  validation webhook treated a reference as an empty password and rejected a
  neighbor shared with an inline-password producer (metallb/frr-k8s#484, fixed
  by metallb/frr-k8s#511). Once that fix ships in the frr-k8s image CNO
  deploys, `renderInlinePassword` in `pkg/network/bgp_vip.go` flips to
  references plus a Secret copy into `openshift-frr-k8s`; that path is
  already implemented and tested.
- `SessionsConfigured` has render-level semantics (the FRRConfiguration was
  produced), not session-level ones; the sessions themselves are observable
  through frr-k8s's `BGPSessionState` objects.

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
