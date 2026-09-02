# AGENTS.md — Cluster Network Operator

Project context for AI coding agents.

## Project Overview

The Cluster Network Operator (CNO) installs, configures, and upgrades the lifecycle of
networking components on an OpenShift cluster. It follows the
[Controller pattern](https://godoc.org/github.com/kubernetes-sigs/controller-runtime/pkg#hdr-Controller):
it watches `Network.config.openshift.io/v1` and `Network.operator.openshift.io/v1`,
reconciling rendered manifests to the latter.

Each managed component has its own `bindata/<component>/` templates and a
`render<Component>` function in `pkg/network/` — `ls bindata/` for the current set rather
than trusting an enumerated list here, since it drifts.

## Repository Layout

```text
cmd/                    # Binaries: cluster-network-operator, cluster-network-check-endpoints, cluster-network-check-target
pkg/controller/         # controller-runtime reconcilers (operconfig, proxyconfig, infrastructureconfig,
                         #   pki, signer, statusmanager, egress_router, ...)
pkg/network/            # Core render logic: one render<Component> function per managed component
pkg/render/             # Generic manifest templating engine (Go text/template + sprig)
pkg/bootstrap/          # BootstrapResult — cluster facts (infra, proxy, ...) gathered before rendering
pkg/apply/              # Applies rendered objects via Server-Side Apply; merge.go has SSA special cases
pkg/operator/           # controllercmd wiring, manager startup
pkg/hypershift/         # HyperShift-specific behavior (local subset of HostedControlPlane API)
pkg/platform/           # Platform (cloud provider) specific logic
pkg/client/             # cnoclient — wraps default + HyperShift-aware clients
pkg/apis/               # Generated API types/clientsets — do not hand-edit, see Codegen below
bindata/                # Go-template manifests, one dir per component; rendered+applied at runtime
manifests/              # CVO-applied manifests for the operator itself (namespace, CRDs, RBAC, deployment)
profile-patches/        # Patches layered on manifests/ per install profile (see Makefile)
docs/                   # architecture.md, operands.md, IPsec/DPU docs — kept in sync with behavior changes
test/e2e/               # Ginkgo e2e suites
hack/                   # Dev scripts: update-codegen.sh, test-go.sh, verify-style.sh, kind.yaml, ...
```

## Build and Test

```bash
make build      # Build cluster-network-operator, cluster-network-check-endpoints, and cluster-network-check-target
make check      # verify (incl. codegen diff) + test-unit + golangci-lint — run this before a PR

# `check`'s pieces, for faster iteration on just one:
make test-unit             # Unit tests only (./pkg/... ./cmd/...)
make golangci-lint          # Lint only
make verify-update-codegen  # Regenerate deepcopy/clientset code and diff-check
```

## Key Conventions

- **Commit/PR titles**: imperative mood, under 72 chars, prefixed by affected component when
  scoped (e.g. `ovn-kubernetes: Fix EgressIP race condition`). Include the Jira ref
  (`OCPBUGS-12345`) in the title or description for bug fixes.
- **Commits are logical units**: one self-contained change per commit; body explains *why*,
  not a narration of the diff. No "address review comments" commits — squash into the
  relevant commit. No merge commits — rebase onto target instead.
- **PR description** must cover Why / What / How-to-verify (which CI lanes/platforms tested
  it — AWS, GCP, Azure, bare-metal, vSphere, SNO, HyperShift). Manual testing alone doesn't
  count. Bug fixes need root cause + fix explanation. Keep PRs under ~7000 lines diff
  (excl. vendor); split large features.
- **Tests are expected alongside changes**: modified `pkg/`/`cmd/` Go code or `bindata/`
  templates need corresponding `*_test.go` changes. User-facing behavior changes/bug fixes
  need `test/e2e/` coverage. If genuinely infeasible, justify in the PR description.
- **Docs**: new features / behavior changes / architecture changes need `docs/` updates
  (new feature → new doc file). DPU/DPF changes need docs on data-path impact, config
  keys, failure modes. IPsec changes update `docs/enabling_ns_ipsec.md`. Operand/component
  boundary changes update `docs/operands.md` / `docs/architecture.md`.
- **RBAC least privilege**: no wildcard verb/resource in ClusterRole/Role without
  justification; mutation verbs (create/update/patch/delete) need explicit justification.
- **Generated files are not hand-edited**: `pkg/apis/**` comes from `hack/update-codegen.sh`.
- **Keep this file and `.coderabbit.yaml` in sync** with reality — both go stale the same way
  (renamed files/flags/CRDs, changed architecture). CodeRabbit's "Stale Project Docs and
  Config" pre-merge check flags AGENTS.md drift and blocks merge on it, but that's a
  best-effort AI check, not a real CI job — don't rely on it catching everything.

## Go Code Quality (applies to code you write here)

- Logging: klog only, never `fmt.Print*`/`log.*`.
- Wrap errors with context (`fmt.Errorf`): `%w` when callers need `errors.Is/As`, `%v` when
  the underlying error is an implementation detail. Don't return bare `err` from functions
  doing meaningful work (thin wrappers/delegation are fine bare).
  Watch for `err :=` in nested blocks shadowing an outer `err`.
  Always use explicit `time.Duration` units (`10*time.Second`, never bare `10`).
- **Dual-stack always**: never handle IPv4 only where IPv6 also applies (e.g. joining
  an IP address and port manually rather than using `net.JoinHostPort`).
- Guard shared map/slice state accessed from multiple goroutines.
- Tests: `t.Fatalf` needs context, not just the bare error. Prefer gomega
  `Eventually`/`Consistently` or poll loops over `time.Sleep`. Use `t.Setenv`, not
  `os.Setenv` (auto-restores).
- Avoid AI-slop patterns: comments that restate the code (`// set a to b` above `a = b`),
  unnecessary intermediate variables, defensive checks for conditions that can't occur in
  context, copy-pasted blocks that should be a helper/table-driven test, and large
  unrelated test additions unconnected to the functional change.

## Architecture Notes

### Config flow: config.openshift.io → operator.openshift.io

Two CRDs drive configuration. `network.config.openshift.io/cluster` is the installer-set
source of truth. `network.operator.openshift.io/cluster` is what CNO actually renders from.
On each reconcile CNO copies `clusterNetwork`, `serviceNetwork`, and `networkType` from
config → operator, **unconditionally overwriting** the operator CR for those fields. Fields
that exist only on the operator CR (`defaultNetwork.ovnKubernetesConfig`, `ManagementState`,
`disableNetworkDiagnostics`) are untouched by this merge — they're the admin's to control,
and setting them on the operator CR directly has no effect for config-owned fields (gets
overwritten next reconcile). `ManagementState`/`disableNetworkDiagnostics` need special SSA
merge protection (`pkg/apply/merge.go`) since they're non-pointer fields whose zero values
would otherwise clobber user-set values under SSA.

`pkg/bootstrap.BootstrapResult` carries ambient cluster facts (infra platform, proxy, etc.)
that rendering needs but that don't come from either CR.

### Render pipeline

`pkg/network.Render()` calls one `render<Component>` function per component — order matters
(e.g. CNCC renders before the CNI plugin because the plugin depends on the CRD CNCC defines).
Each function feeds `bindata/<component>/` Go-template YAML through
`pkg/render.RenderDir(s)` (sprig funcs) into `[]*unstructured.Unstructured`.

### Apply: Server-Side Apply only

`pkg/apply.ApplyObject` uses SSA exclusively (`Force: true`, field manager
`cluster-network-operator[/<subcontroller>]`) for all components. Omitting a field the
manager owns **releases** it — the API server deletes the field or resets it to its
default if no other manager owns it, but leaves it alone if another manager still does.
Don't assume removing a field from a template is a no-op on the live object.

### manifests/ vs bindata/

For standalone/self-hosted OpenShift, `manifests/` is applied by the CVO **before CNO
starts** — namespace, CNO's own deployment, RBAC, CRDs, monitoring. In HyperShift, CNO
itself is deployed by `openshift/hypershift` instead of CVO (see HyperShift section below);
changes here may need a matching change in that repo. Every resource needs correct
`include.release.openshift.io/*`
annotations (`self-managed-high-availability`, `single-node-developer`,
`ibm-cloud-managed` where applicable) or it's skipped on that profile.
`0000_70_cluster-network-operator_03_deployment.yaml` has a companion
`0000_70_cluster-network-operator_03_deployment-ibm-cloud-managed.yaml` — keep them aligned.

`bindata/` is rendered and applied by CNO **at runtime** — the actual networking component
workloads. Critical annotations CNO acts on:
- `release.openshift.io/version: "{{.ReleaseVersion}}"` — required on every
  Deployment/DaemonSet/StatefulSet; drives upgrade-progress tracking.
- `network.operator.openshift.io/cluster-name` — HyperShift: which cluster (management vs
  guest) a resource belongs to; wrong value applies it to the wrong cluster.
- `networkoperator.openshift.io/create-only` — created once, never updated.
- `network.operator.openshift.io/copy-from` — content copied from `ClusterName/Namespace/Name`.
- `networkoperator.openshift.io/non-critical` — doesn't block operator `Available` during install.
- Namespaces running DaemonSets need `openshift.io/node-selector: ""` or pods can become
  unschedulable under a cluster-wide default node selector.
- When adding a field to containers in a DaemonSet/Deployment, check *all* containers
  including init containers — partial coverage is a recurring bug class here.

### OVN-Kubernetes: managed vs self-hosted

`bindata/network/ovn-kubernetes/` splits into `common/` (shared), `managed/` (HyperShift
control plane, runs in management cluster), `self-hosted/` (standalone OCP control plane).
**#1 source of HyperShift drift bugs**: `managed/` and `self-hosted/` have parallel files for
the same components (ovnkube-control-plane, ovnkube-node, config, monitoring) — a change to
one almost always needs the mirrored change in the other (masquerade subnet, interconnect
flags, IPv6 URL formatting are recurring examples). Some settings are legitimately
HyperShift-only (SOCKS proxy, cert paths, `{{.OvnControlPlaneImage}}`).

Rollout order: control-plane first, then nodes only after control-plane rollout completes;
a pre-puller DaemonSet pulls images ahead of node rollout. Bugs in the rollout-progress
checks in `pkg/network/ovn_kubernetes.go` block cluster upgrades.

Pod restart triggers: `ovnkube-script-lib` ConfigMap (`008-script-lib.yaml`) is hashed into
`OVNKubeConfigHash` on the pod template → triggers ovnkube-node restart on change. Separate
`OVNKubeNodeConfigHash` (tracks `outboundSNAT`) and `OVNKubeControlPlaneConfigHash` (tracks
BGP `asNumber`/`topology`) keep node-only vs control-plane-only changes from cross-restarting.
Annotations set via `setOVNObjectAnnotation` (`ip-family-mode`, `cluster-network-cidr`,
`hybrid-overlay-status`) go on both the object and pod template, so they do trigger rollout.
Other ConfigMaps (e.g. `004-config.yaml`) are **not hashed** — changes only take effect on
next natural pod restart; a new option needing immediate effect must join the hash
computation or use another restart mechanism. Audit container command/args for duplicated
or obsolete CLI flags when touching OVN-K containers — a recurring bug source.

`bindata/network/node-identity/` has the same `managed/`/`self-hosted/` split; keep aligned.

### HyperShift

CNO itself is deployed by `openshift/hypershift` (not CVO) in HyperShift mode — changes to
`manifests/` or the CNO deployment may need a corresponding change there too.
`pkg/hypershift` defines a local subset of the HostedControlPlane API to avoid importing the
full `openshift/hypershift` dependency; `HYPERSHIFT`/`HOSTED_CLUSTER_NAME` env vars control
mode detection.
