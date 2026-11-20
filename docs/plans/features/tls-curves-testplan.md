# Test Plan: TLS Curve/Group Preferences in OpenShift Networking Components

**Feature:** [CORENET-7478](https://redhat.atlassian.net/browse/CORENET-7478) — TLS curve/group preferences support in cluster-network-operator  
**Parent Feature:** [OCPSTRAT-3145](https://redhat.atlassian.net/browse/OCPSTRAT-3145) — Add TLSGroupPreferences field to support TLS 1.3 curve configuration  
**OCP Version:** 5.1+  
**Target Release:** OpenShift 5.1

## Table of Contents

1. [References](#references) — Enhancement proposals, JIRA tickets, and standards
2. [Introduction](#introduction) — Feature overview and scope
3. [Test Strategy](#test-strategy) — Testing approach for networking components
4. [Test Scope](#test-scope) — Functional and negative scenarios
5. [Out of Scope](#out-of-scope) — What we're not testing
6. [Target Environments](#target-environments) — OCP versions and architectures
7. [Test Deliverables](#test-deliverables) — Test reports and automation
8. [Test Tasks](#test-tasks) — Work breakdown
9. [Pass/Fail Criteria](#passfail-criteria) — Exit criteria
10. [Risks and Mitigation](#risks-and-mitigation) — Known blockers and limitations

## References

### Documentation and Enhancement Proposals

| Type | Reference | Description |
|------|-----------|-------------|
| **Enhancement Proposal** | [api-tls-curves-config.md](https://github.com/openshift/enhancements/blob/master/enhancements/security/api-tls-curves-config.md) | TLS curve/group preferences configuration API |
| **Primary Jira** | [CORENET-7478](https://redhat.atlassian.net/browse/CORENET-7478) | CNO TLS curve/group preferences support |
| **Parent Jira** | [OCPSTRAT-3145](https://redhat.atlassian.net/browse/OCPSTRAT-3145) | TLSGroupPreferences field for TLS 1.3 |
| **Repository** | [openshift/cluster-network-operator](https://github.com/openshift/cluster-network-operator) | CNO and managed networking components |

## Introduction

The TLS curve/group preferences feature enables cluster administrators to configure which elliptic curve cryptography (ECC) groups are used during TLS 1.3 handshakes. This enhancement affects all OpenShift components that serve HTTPS endpoints, including networking components managed by the Cluster Network Operator (CNO).

### What Changed

**Before OCP 5.1:**
- OpenShift components used hardcoded default TLS curves from the Go `crypto/tls` library

**Starting in OCP 5.1, administrators can:**

1. **Configure custom TLS curve preferences** via `spec.tlsSecurityProfile.custom.groups` on the API server
2. **Specify classical curves:**
   - NIST curves: `secp256r1`, `secp384r1`, `secp521r1`
   - Non-NIST curves: `X25519`

### Affected Components

The CNO propagates TLS curve preferences to the following networking components that serve TLS endpoints:

| Component | Namespace | Purpose |
|-----------|-----------|---------|
| **multus-admission-controller** | `openshift-multus` | Validates NetworkAttachmentDefinition admissions over HTTPS |
| **ovn-kubernetes nodes** | `openshift-ovn-kubernetes` | Serves metrics and health endpoints |
| **cluster-network-operator** | `openshift-network-operator` | CNO's own metrics and health endpoints |
| **networking-console-plugin** | `openshift-network-console` | OpenShift Console networking plugin backend |

## Test Strategy

### Testing Approach

This test plan validates that CNO-managed networking components correctly:

1. **Apply** configured TLS curves from the cluster-level `tlsSecurityProfile`
2. **Enforce** feature gate requirements for PQC hybrid curves
3. **Negotiate** TLS handshakes using the configured curves

### Test Methodology

**Component Coverage:** Each of the 4 networking components will be tested independently

**Curve Coverage:** All supported TLS curves/groups will be tested

### Component-Specific Testing

| Component | Purpose | Test Focus |
|-----------|---------|------------|
| **multus-admission-controller** | NetworkAttachmentDefinition admissions | HTTPS webhook server curve negotiation |
| **ovn-kubernetes nodes** | Metrics and health endpoints | Node daemon TLS configuration |
| **cluster-network-operator** | CNO operations | Operator metrics and health endpoints |
| **networking-console-plugin** | Console integration | Plugin backend HTTPS server |

## Test Scope

### Downstream Testing Focus

This test plan focuses on downstream OpenShift networking components managed by the Cluster Network Operator (CNO) that require TLS curve/group preferences validation.

**Primary Repository:**
- [openshift/cluster-network-operator](https://github.com/openshift/cluster-network-operator)

**Managed Components:**
- `multus-admission-controller`
- `ovn-kubernetes` node daemons
- `cluster-network-operator` (CNO itself)
- `networking-console-plugin`

### E2E Test Cases

#### E1: Verify TLS curve preferences for multus-admission-controller

**Component:** `multus-admission-controller`  
**Namespace:** `openshift-multus`

**Objective:**  
Validates that the multus admission controller webhook server correctly applies configured TLS curve preferences during HTTPS connections for NetworkAttachmentDefinition resource admissions.

**Test Coverage:**
- ✓ Verify all supported TLS curves/groups
- ✓ Validate successful TLS handshakes using the configured curves

**Expected Results:**
- TLS connections successfully negotiate using configured curves

---

#### E2: Verify TLS curve preferences for ovn-kubernetes nodes

**Component:** `ovn-kubernetes` node daemons  
**Namespace:** `openshift-ovn-kubernetes`

**Objective:**  
Validates that OVN-Kubernetes node components (ovnkube-node, ovnkube-controller) correctly apply TLS curve preferences for metrics and health HTTPS endpoints.

**Test Coverage:**
- ✓ Verify all supported TLS curves/groups
- ✓ Validate successful TLS handshakes using the configured curves

**Expected Results:**
- TLS connections successfully negotiate using configured curves

---

#### E3: Verify TLS curve preferences for cluster-network-operator

**Component:** `cluster-network-operator` (CNO)  
**Namespace:** `openshift-network-operator`

**Objective:**  
Validates that the CNO's own metrics and health HTTPS endpoints correctly apply configured TLS curve preferences.

**Test Coverage:**
- ✓ Verify all supported TLS curves/groups
- ✓ Validate successful TLS handshakes using the configured curves

**Expected Results:**
- TLS connections successfully negotiate using configured curves

---

#### E4: Verify TLS curve preferences for networking-console-plugin

**Component:** `networking-console-plugin`  
**Namespace:** `openshift-network-console`

**Objective:**  
Validates that the OpenShift Console networking plugin backend server correctly applies TLS curve preferences for HTTPS connections.

**Test Coverage:**
- ✓ Verify all supported TLS curves/groups
- ✓ Validate successful TLS handshakes using the configured curves

**Expected Results:**
- TLS connections successfully negotiate using configured curves

---

### Manual Test Procedure

This section provides detailed manual test steps for validating TLS curve preferences across all CNO-managed networking components.

#### Step 1: Enable TLSGroupPreferences Feature Gate

If the `TLSGroupPreferences` feature gate is not already enabled, enable it:

```bash
oc patch featuregate cluster --type=merge -p '
{
  "spec": {
    "featureSet": "CustomNoUpgrade",
    "customNoUpgrade": {
      "enabled": ["TLSGroupPreferences"]
    }
  }
}'
```

#### Step 2: Wait for Cluster Stabilization

Use existing functions to wait for MachineConfigPool (MCP), nodes, and operators to reboot and return to healthy state.

#### Step 3: Configure Custom TLS Groups

Apply custom TLS security profile with specific groups:

```bash
oc patch apiserver cluster --type=merge -p '
{
  "spec": {
    "tlsSecurityProfile": {
      "type": "Custom",
      "custom": {
        "minTLSVersion": "VersionTLS12",
        "groups": [
          "secp521r1",
          "secp256r1"
        ],
        "ciphers": [
          "TLS_AES_128_GCM_SHA256",
          "TLS_AES_256_GCM_SHA384",
          "TLS_CHACHA20_POLY1305_SHA256",
          "ECDHE-ECDSA-AES128-GCM-SHA256",
          "ECDHE-RSA-AES128-GCM-SHA256",
          "ECDHE-ECDSA-AES256-GCM-SHA384",
          "ECDHE-RSA-AES256-GCM-SHA384"
        ]
      }
    },
    "tlsAdherence": "StrictAllComponents"
  }
}'
```

#### Step 4: Wait for Cluster Stabilization Again

Use existing functions to wait for MachineConfigPool (MCP), nodes, and operators to reboot and return to healthy state.

#### Step 5: Validate API TLS Group Configuration

Verify the configured TLS groups:

```bash
oc get apiserver/cluster -o jsonpath='{.spec.tlsSecurityProfile.custom.groups}' | jq .
```

#### Step 6: Test Network Components

##### Test OVN-Kubernetes

```bash
# Get OVN pod
oc get pod -n openshift-ovn-kubernetes
export ovnpod="ovnkube-node-h4xpv"

# Test kube-rbac-proxy-ovn-metrics container (port 9105)
oc exec -n openshift-ovn-kubernetes $ovnpod -c kube-rbac-proxy-ovn-metrics -- \
      timeout 3 openssl s_client -connect localhost:9105 -groups secp521r1 -brief </dev/null
oc exec -n openshift-ovn-kubernetes $ovnpod -c kube-rbac-proxy-ovn-metrics -- \
      timeout 3 openssl s_client -connect localhost:9105 -groups secp256r1 -brief </dev/null
oc exec -n openshift-ovn-kubernetes $ovnpod -c kube-rbac-proxy-ovn-metrics -- \
      timeout 3 openssl s_client -connect localhost:9105 -groups secp384r1 -brief </dev/null

# Test kube-rbac-proxy-node container (port 9103)
oc exec -n openshift-ovn-kubernetes $ovnpod -c kube-rbac-proxy-node -- \
      timeout 3 openssl s_client -connect localhost:9103 -groups secp521r1 -brief </dev/null
oc exec -n openshift-ovn-kubernetes $ovnpod -c kube-rbac-proxy-node -- \
      timeout 3 openssl s_client -connect localhost:9103 -groups secp256r1 -brief </dev/null
oc exec -n openshift-ovn-kubernetes $ovnpod -c kube-rbac-proxy-node -- \
      timeout 3 openssl s_client -connect localhost:9103 -groups secp384r1 -brief </dev/null
```

##### Test Cluster Network Operator (CNO)

```bash
# Get CNO pod
oc get pod -n openshift-network-operator
export cnopod="network-operator-768b5b658-dlhxg"

# Port 9104 (CNO Metrics)
oc exec -n openshift-network-operator $cnopod -c network-operator -- \
    timeout 3 openssl s_client -connect localhost:9104 -groups secp521r1 -brief </dev/null
oc exec -n openshift-network-operator $cnopod -c network-operator -- \
    timeout 3 openssl s_client -connect localhost:9104 -groups secp256r1 -brief </dev/null
oc exec -n openshift-network-operator $cnopod -c network-operator -- \
    timeout 3 openssl s_client -connect localhost:9104 -groups secp384r1 -brief </dev/null

# Port 9103 (kube-rbac-proxy)
oc exec -n openshift-network-operator $cnopod -c network-operator -- \
    timeout 3 openssl s_client -connect localhost:9103 -groups secp521r1 -brief </dev/null
oc exec -n openshift-network-operator $cnopod -c network-operator -- \
    timeout 3 openssl s_client -connect localhost:9103 -groups secp256r1 -brief </dev/null
oc exec -n openshift-network-operator $cnopod -c network-operator -- \
    timeout 3 openssl s_client -connect localhost:9103 -groups secp384r1 -brief </dev/null

# Port 9105 (kube-rbac-proxy)
oc exec -n openshift-network-operator $cnopod -c network-operator -- \
    timeout 3 openssl s_client -connect localhost:9105 -groups secp521r1 -brief </dev/null
oc exec -n openshift-network-operator $cnopod -c network-operator -- \
    timeout 3 openssl s_client -connect localhost:9105 -groups secp256r1 -brief </dev/null
oc exec -n openshift-network-operator $cnopod -c network-operator -- \
    timeout 3 openssl s_client -connect localhost:9105 -groups secp384r1 -brief </dev/null
```

##### Test Networking Console Plugin

```bash
# Get networking console plugin pod
oc get pod -n openshift-network-console
export consolepod="networking-console-plugin-8457555769-g6rrn"

# Port 9443 (console plugin backend)
oc exec -n openshift-network-console $consolepod -c networking-console-plugin -- \
    timeout 3 openssl s_client -connect localhost:9443 -groups secp521r1 -brief </dev/null
oc exec -n openshift-network-console $consolepod -c networking-console-plugin -- \
    timeout 3 openssl s_client -connect localhost:9443 -groups secp256r1 -brief </dev/null
oc exec -n openshift-network-console $consolepod -c networking-console-plugin -- \
    timeout 3 openssl s_client -connect localhost:9443 -groups secp384r1 -brief </dev/null
```

##### Test Multus Admission Controller

```bash
# Get Multus admission controller pod
oc get all -n openshift-multus | grep pod/multus-admission-controller
export multuspod="multus-admission-controller-5bc5d4cb95-8r5hj"

# Port 6443 (multus-admission-controller container)
oc exec -n openshift-multus $multuspod -c multus-admission-controller -- \
    timeout 3 openssl s_client -connect localhost:6443 -groups secp521r1 -brief </dev/null
oc exec -n openshift-multus $multuspod -c multus-admission-controller -- \
    timeout 3 openssl s_client -connect localhost:6443 -groups secp256r1 -brief </dev/null
oc exec -n openshift-multus $multuspod -c multus-admission-controller -- \
    timeout 3 openssl s_client -connect localhost:6443 -groups secp384r1 -brief </dev/null

# Port 8443 (kube-rbac-proxy container)
oc exec -n openshift-multus $multuspod -c kube-rbac-proxy -- \
    timeout 3 openssl s_client -connect localhost:8443 -groups secp521r1 -brief </dev/null
oc exec -n openshift-multus $multuspod -c kube-rbac-proxy -- \
    timeout 3 openssl s_client -connect localhost:8443 -groups secp256r1 -brief </dev/null
oc exec -n openshift-multus $multuspod -c kube-rbac-proxy -- \
    timeout 3 openssl s_client -connect localhost:8443 -groups secp384r1 -brief </dev/null
```

#### Step 7: Expected Test Results

**Pass (successful TLS handshake):**
- `secp521r1` — Configured in the groups list
- `secp256r1` — Configured in the groups list

**Fail (connection refused or handshake failure):**
- `secp384r1` — NOT configured in the groups list (should be rejected)

## Out of Scope

The following components and scenarios are **explicitly excluded** from this test plan:

### Other OpenShift Components
| Component | Reason |
|-----------|--------|
| **API Server TLS** | Covered by separate API server test plans |
| **Ingress Controller TLS** | Covered by ingress/router team test plans |
| **etcd TLS Configuration** | Managed separately from CNO |
| **Service Mesh / Istio TLS** | Not managed by CNO |

### Additional Exclusions
- **User Workload TLS** — Applications deployed by users manage their own TLS configuration
- **OpenSSL Version Compatibility** — Assumes OpenSSL 3.x+ with modern curve support
- **Certificate Authority Changes** — This plan only covers curve negotiation, not CA trust chains
- **Performance Impact** — Curve selection performance benchmarking is not included
- **Upgrade Scenarios** — TLS curve migration during cluster upgrades (covered separately)

## Target Environments

| Environment Type | Version/Configuration | Priority | Notes |
|-----------------|----------------------|----------|-------|
| **OpenShift** | 5.1+ | Critical | Primary target release |

## Test Deliverables

| Deliverable | Description | Target Audience |
|-------------|-------------|-----------------|
| **E2E Test Automation** | Downstream PRs with e2e tests in `cluster-network-operator` repository | QE, CI/CD (Prow) |
| **Test Execution Report** | Comprehensive test results with coverage metrics and findings | QE, Engineering |
| **Documentation Feedback** | Configuration examples and known limitations | Docs team |
| **Bug Reports** | Detailed reports for any discrepancies between expected and actual behavior | Engineering |

## Test Tasks

| Task | Status | Owner | Notes |
|------|--------|-------|-------|
| Test plan creation and review | ☐ | QE Team | This document |
| Execute e2e tests on OCP cluster | ☐ | QE Team | All 4 components |
| Automate curve validation script | ☐ | QE Team | For all 4 CNO-managed components |
| Document test results | ☐ | QE Team | Create test execution report |
| File bugs for failures | ☐ | QE Team | Track issues in Jira |
| Provide documentation feedback | ☐ | QE Team | Configuration examples and notes |

## Pass/Fail Criteria

### Pass Criteria

The feature is considered **ready for release** when all of the following conditions are met:

- ✓ **Zero critical or major defects** remain open in Jira
- ✓ **All e2e test cases** (E1-E4) pass consistently in Prow CI
- ✓ **Test automation complete** — All tests merged into `cluster-network-operator` repository
- ✓ **Documentation feedback provided** — Configuration examples and known limitations shared with Docs team
- ✓ **Test execution report published** — Comprehensive results documented

### Fail Criteria

The feature is **NOT ready for release** if any of the following occur:

- ✗ **Critical defects** that block core TLS functionality
- ✗ **Consistent test failures** on Prow CI
- ✗ **Security vulnerabilities** identified in TLS configuration handling

## Risks and Mitigation

| Risk | Impact | Probability | Mitigation |
|------|--------|-------------|------------|
| **Platform-specific issues** | Different behavior on ARM64, s390x, ppc64le | Low | Test on all supported architectures |
| **Third-party client compatibility** | Older clients may not support newer curves | Low | Ensure graceful fallback to supported curves |

### Current Status

**No known blockers or critical limitations** at the time of test plan creation.

---


**End of Test Plan**
