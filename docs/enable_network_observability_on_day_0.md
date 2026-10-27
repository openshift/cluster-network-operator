# Enabling Network Observability on Day 0

CNO can install the [Network Observability Operator](https://docs.openshift.com/container-platform/latest/observability/network_observability/network-observability-overview.html)
(NOO) and create a default `FlowCollector` as part of cluster bring-up, so a cluster
can ship with network observability already running. This document describes that
behavior, how to control it, and why it's **not** turned on automatically when an
existing cluster is upgraded.

The logic lives in the observability controller
(`pkg/controller/observability/observability_controller.go`), which watches the
`network.config.openshift.io/cluster` and `network.operator.openshift.io/cluster`
singletons.

## Prerequisites

Two things must be true before CNO attempts installation:

1. **Feature gate.** The `NetworkObservabilityInstall` feature gate must be enabled.
   The `networkObservability` field on `network.config.openshift.io` is itself gated
   behind this same feature gate (see
   `vendor/github.com/openshift/api/config/v1/types_network.go`), so the field only
   exists on releases where the gate is on.
2. **Cluster availability.** CNO waits for `ClusterVersion` to report `Available=true`
   before installing. On day 0, it allows up to 90 minutes for this.

## Configuration

Set the installation policy on the config CR:

```yaml
apiVersion: config.openshift.io/v1
kind: Network
metadata:
  name: cluster
spec:
  networkObservability:
    installationPolicy: InstallAndEnable   # or NoAction
```

`spec.networkObservability.installationPolicy` accepts:

| Value | Behavior |
| --- | --- |
| `InstallAndEnable` | Install NOO and create the `FlowCollector` **once**, regardless of topology or upgrade state. This is the explicit opt-in and overrides every default-skip below. After the install completes CNO hands off (see below); it is not continuous enforcement. |
| `NoAction` | Do not start a new install. This does not cancel an install already in progress. |
| unset (default) | Install on a **fresh**, non-SNO cluster (opt-out model). Skipped for SNO clusters and for clusters that were **upgraded** into the feature. |

## What CNO installs

When an install proceeds, CNO applies, in order:

1. The OLM resources in `bindata/observability/07-observability-operator.yaml`:
   a `netobserv-operator` `Namespace`, an `OperatorGroup`, and a `Subscription`
   (`installPlanApproval: Automatic`, channel `stable`, from `redhat-operators`).
   The namespace CNO creates is tagged with the
   `network.operator.openshift.io/created-by-cno` annotation.
2. Once the operator has installed the `FlowCollector` CRD, the default
   `FlowCollector` named `cluster` from `bindata/observability/08-flowcollector.yaml`.

After a successful install, CNO is **hands off**. The condition latches terminal
(`Installed`) and CNO stops managing Network Observability. This is a one-time action.
It applies to `InstallAndEnable` exactly as it does to the default opt-out install.
CNO installs once and then steps aside. Deleting the operator or deleting or changing
the `FlowCollector` afterwards will **not** cause CNO to restore them, and leaving
`installationPolicy: InstallAndEnable` set does not re-trigger an install. To make
CNO act again, see [Resetting the install state](#resetting-the-install-state).

## Day 0 vs. upgrade

A feature behind a gate normally arrives when you upgrade into the release that
graduates it. Since Network Observability consumes cluster resources, silently
enabling it during an upgrade could regress a cluster that was already running fine.
A brand-new cluster has no such prior working state to regress, so installing at
bring-up is the expected behavior.

Therefore, under the default policy, **CNO does not enable Network Observability
on an upgrade.** It records the re-evaluatable `UpgradedCluster` reason instead.
To enable it on such a cluster, set `installationPolicy: InstallAndEnable`.

### How an upgrade is detected

`isClusterUpgraded` reads `ClusterVersion.status.history`, which CVO records
newest-first. The last entry is the initial install and the first is the current
release; if they differ, the cluster was upgraded:

```
upgraded  ⇔  history[len-1].image != history[0].image
```

It compares `image`, not `version`. The release `version` is optional and may be empty
or repeated across distinct releases, whereas `image` is always populated and uniquely
identifies the release, so an upgrade between two builds that share a version string is
still detected. This is a stable fact (unlike the transient `Progressing` condition)
and needs no hardcoded "feature introduced in version X" constant. A fresh install has
a single history entry (or repeated same-image entries), so it reads as not-upgraded
and installs normally.

One consequence: a cluster *born* with the feature that later takes a z-stream patch
also reads as "upgraded." This is harmless, because such a cluster already resolved
its condition at day 0 (`Installed`, `PreExisting`, or a re-evaluatable reason), and
the terminal/in-progress short-circuit at the top of `shouldInstallAndSetCondition`
runs before the upgrade check is ever reached.

## Status condition

CNO reports the outcome on the operator Network CR
(`network.operator.openshift.io/cluster`) status via the
`NetworkObservabilityInstalledByCNO` condition. The `Reason` tells you what happened
and, where relevant, why not. Reasons are grouped by lifecycle.

**Re-evaluatable** (`Status: False`; re-checked every reconcile, can still install):

| Reason | Message |
| --- | --- |
| `FeatureGateDisabled` | NetworkObservabilityInstall feature gate is not enabled |
| `NoAction` | Installation policy is set to NoAction so it will not install Network Observability. |
| `SNO` | Single control-plane cluster. Network Observability will not be installed, even if control-plane nodes are added later. |
| `UpgradedCluster` | Cluster was upgraded; Network Observability is not enabled on upgrade. Set installationPolicy to InstallAndEnable to install it. |

**In-progress** (`Status: False`; the install is underway):

| Reason | Message |
| --- | --- |
| `WaitingForCluster` | Waiting for the cluster to be available |
| `InstallingOperator` | Installing Network Observability Operator... |
| `CreatingFlowCollector` | Creating FlowCollector instance... |

**Transient** (`Status: False`; a step failed and is being retried; these keep the
20-minute install clock running, and the apply-failure messages carry the underlying
cause):

| Reason | Message |
| --- | --- |
| `OperatorApplyFailed` | Failed to install Network Observability Operator: `<err>`. Will retry in `<interval>`. |
| `FlowCollectorApplyFailed` | Failed to create FlowCollector: `<err>`. Will retry in `<interval>`. |
| `RollbackFailed` | Failed to roll back OLM resources. Retrying. |

**Terminal** (CNO stops acting; `Installed` is `Status: True`, the rest are `Status: False`):

| Reason | Message |
| --- | --- |
| `Installed` | Network Observability has been installed. Will not be managed by CNO. |
| `PreExisting` | FlowCollector CRD already exists. Will not be managed by CNO. |
| `ClusterNotAvailable` | Cluster did not become available within 90 minutes. Will not retry. |
| `OperatorInstallFailed` | Failed to install Network Observability Operator within 20 minutes. Rolled back. Will not retry. |
| `FlowCollectorCreateFailed` | Failed to create FlowCollector within 20 minutes. Rolled back. Will not retry. |

## Timeouts and rollback

- CNO waits up to **90 minutes** for the cluster to become available
  (`clusterAvailableTimeout`). If it never does, the condition latches
  `ClusterNotAvailable`.
- Once the cluster is available, CNO has **20 minutes** to finish the install
  (`installTimeout`), measured from the later of the install attempt and cluster
  availability so an already-up cluster does not consume the budget.
- If the install does not complete in time, CNO **rolls back** the OLM resources it
  created (`Subscription`, then `OperatorGroup`, then the operator CSV, and the
  namespace only if CNO created it; the `FlowCollector` CRD is intentionally left in
  place) and latches a terminal failure named for the phase that did not finish
  (`OperatorInstallFailed` or `FlowCollectorCreateFailed`). Rollback matters because
  OLM is a standing controller that would otherwise keep driving the install after CNO
  has given up. If rollback itself fails, CNO reports `RollbackFailed` and retries.

## Resetting the install state

Because CNO installs once and then hands off, there is no supported in-band way to ask
it to "do it again." The only way back to the original, pre-install state is a manual
**factory reset**: undo the install and clear CNO's latched decision so the controller
re-evaluates from scratch on its next reconcile, exactly as if this were day 0.

> **Warning:** This edits the operator CR's status subresource directly, which is not a
> routine operation. Treat it as a deliberate, destructive reset, not a config toggle.
> Do it only when you intend to tear down and re-drive the whole decision.

> **Only for a CNO-owned install (reason `Installed`).** Step 1 deletes NOO and the
> `FlowCollector` CRD, which cascade-deletes every `FlowCollector` CR. Do **not** run it
> against a `PreExisting` install. There CNO did not create those resources, so another
> controller owns them and deleting them would break that owner's installation. Removing
> them is only safe after coordinating with the current owner. If you merely want CNO to
> re-evaluate a `PreExisting` cluster, clearing the condition (step 2) alone is enough,
> though with the CRD still present CNO will just record `PreExisting` again.

1. **Uninstall what CNO installed.** Delete the `FlowCollector` CR, uninstall NOO
   (its `Subscription`, `OperatorGroup`, and CSV in the `netobserv-operator`
   namespace), and delete the `FlowCollector` CRD. Optionally delete the
   `netobserv-operator` namespace. This step matters. If the `FlowCollector` CRD is
   left in place, clearing the condition in step 2 makes CNO record `PreExisting`
   rather than reinstall (something other than CNO appears to own the CRD).

2. **Clear CNO's latched decision.** Remove the `NetworkObservabilityInstalledByCNO`
   condition from the operator Network status:

   ```console
   $ oc edit network.operator.openshift.io/cluster --subresource=status
   # delete the NetworkObservabilityInstalledByCNO entry under status.conditions
   ```

On the next reconcile, CNO starts over. It re-checks the feature gate, policy, upgrade
state, and SNO topology just as it would on a fresh cluster, and installs (or records
the appropriate skip reason) accordingly. Note that re-evaluation is subject to the
*current* cluster state. Most notably, a cluster that has since been upgraded will now
record `UpgradedCluster` under the default policy, so set
`installationPolicy: InstallAndEnable` if you want the reset to reinstall.

## Notes and edge cases

- **Pre-existing install.** If the `FlowCollector` CRD already exists and CNO did not
  create it, CNO reports `PreExisting` and does not manage Network Observability.
- **SNO ordering.** On a single-node cluster that was also upgraded, the upgrade check
  runs first, so the reason is `UpgradedCluster` (the more actionable one) rather than
  `SNO`. Both are overridden by `InstallAndEnable`.
- **Hand-off, not liveness.** `NetworkObservabilityInstalledByCNO` records that CNO
  performed (or declined) the install; it is not re-validated against live state after
  it latches terminal. Removing the operator out-of-band later does not change it.
