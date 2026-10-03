# Test Plan: TLS Curve/Group Preferences in OpenShift Networking Components

**Feature:** [CORENET-7478](https://redhat.atlassian.net/browse/CORENET-7478) — TLS curve/group preferences support in cluster-network-operator  
**Parent Feature:** [OCPSTRAT-3145](https://redhat.atlassian.net/browse/OCPSTRAT-3145) — Add TLSGroupPreferences field to support TLS 1.3 curve configuration  
**OCP Version:** 5.1+  
**Target Release:** OpenShift 5.1

## Table of Contents

1. [References](#references) — Enhancement proposals, JIRA tickets, and standards
2. [Introduction](#introduction) — Feature overview and scope
3. [Test Strategy](#test-strategy) — Testing approach for networking components
4. [Test Scope](#test-scope) — Functional, negative, and FIPS compliance scenarios
5. [Out of Scope](#out-of-scope) — What we're not testing
6. [Target Environments](#target-environments) — OCP versions, FIPS, architectures
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
| **Feature Gate** | `TLSCurvePreferences` | Required for PQC hybrid curves (DevPreviewNoUpgrade / TechPreviewNoUpgrade) |
| **Repository** | [openshift/cluster-network-operator](https://github.com/openshift/cluster-network-operator) | CNO and managed networking components |

## Introduction

The TLS curve/group preferences feature enables cluster administrators to configure which elliptic curve cryptography (ECC) groups are used during TLS 1.3 handshakes. This enhancement affects all OpenShift components that serve HTTPS endpoints, including networking components managed by the Cluster Network Operator (CNO).

### What Changed

**Before OCP 5.1:**
- OpenShift components used hardcoded default TLS curves from the Go `crypto/tls` library
- FIPS-mode filtering was applied automatically without admin control

**Starting in OCP 5.1, administrators can:**

1. **Configure custom TLS curve preferences** via `spec.tlsSecurityProfile.custom.groups` on the API server
2. **Specify classical curves:**
   - NIST curves: `secp256r1`, `secp384r1`, `secp521r1`
   - Non-NIST curves: `X25519`
3. **Enable post-quantum cryptography (PQC) hybrid curves** when the `TLSCurvePreferences` feature gate is active:
   - `X25519MLKEM768`
   - `SecP256r1MLKEM768`
   - `SecP384r1MLKEM1024`

### Affected Components

The CNO propagates TLS curve preferences to the following networking components that serve TLS endpoints:

| Component | Namespace | Purpose |
|-----------|-----------|---------|
| **multus-admission-controller** | `openshift-multus` | Validates NetworkAttachmentDefinition admissions over HTTPS |
| **ovn-kubernetes nodes** | `openshift-ovn-kubernetes` | Serves metrics and health endpoints |
| **cluster-network-operator** | `openshift-network-operator` | CNO's own metrics and health endpoints |
| **networking-console-plugin** | `openshift-network-console` | OpenShift Console networking plugin backend |

### FIPS Mode Behavior

When OpenShift runs in FIPS mode (`/proc/sys/crypto/fips_enabled=1`), each CNO-managed component filters out non-FIPS-approved curves before TLS configuration, ensuring only FIPS-compliant groups are available during handshake negotiation.

**FIPS-Approved Curves (Allowed):**
- `secp256r1` (NIST P-256)
- `secp384r1` (NIST P-384)
- `secp521r1` (NIST P-521)
- `SecP384r1MLKEM1024` (PQC hybrid, OCP 5.1+)

**Non-FIPS Curves (Filtered):**
- `X25519` (non-NIST classical curve)
- `X25519MLKEM768` (contains non-FIPS X25519 component)

**Future Availability:**
- `SecP256r1MLKEM768` — FIPS-approved PQC hybrid curve (requires Go 1.26+)

**Important:** FIPS filtering is **silent** — the configuration is accepted, but non-compliant curves are ignored during TLS handshake negotiation.

## Test Strategy

### Testing Approach

This test plan validates that CNO-managed networking components correctly:

1. **Apply** configured TLS curves from the cluster-level `tlsSecurityProfile`
2. **Filter** non-FIPS-approved curves when running in FIPS mode
3. **Enforce** feature gate requirements for PQC hybrid curves
4. **Negotiate** TLS handshakes using the configured curves

### Test Methodology

**Component Coverage:** Each of the 4 networking components will be tested independently

**Environment Coverage:** Each component will be verified in:
- Non-FIPS clusters
- FIPS-enabled clusters
- Hypershift environments

**Curve Coverage:** All supported TLS curves/groups will be tested (see [Supported TLS Curves/Groups](#supported-tls-curvesgroups))

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

### Supported TLS Curves/Groups

| Curve / Group | Type | FIPS Status | Feature Gate Required | Availability |
|---------------|------|-------------|----------------------|--------------|
| **X25519** | Classical (non-NIST) | Filtered | No | All OCP versions |
| **secp256r1** | Classical (NIST P-256) | Approved | No | All OCP versions |
| **secp384r1** | Classical (NIST P-384) | Approved | No | All OCP versions |
| **secp521r1** | Classical (NIST P-521) | Approved | No | All OCP versions |
| **X25519MLKEM768** | PQC Hybrid | Filtered (contains X25519) | Yes | OCP 5.1+ |
| **SecP256r1MLKEM768** | PQC Hybrid | Would be approved | Yes | Requires Go 1.26+ |
| **SecP384r1MLKEM1024** | PQC Hybrid | Approved | Yes | OCP 5.1+ |

### E2E Test Cases

#### E1: Verify TLS curve preferences for multus-admission-controller

**Component:** `multus-admission-controller`  
**Namespace:** `openshift-multus`

**Objective:**  
Validates that the multus admission controller webhook server correctly applies configured TLS curve preferences during HTTPS connections for NetworkAttachmentDefinition resource admissions.

**Test Coverage:**
- ✓ Verify all supported TLS curves/groups (see [Supported TLS Curves/Groups](#supported-tls-curvesgroups))
- ✓ Validate FIPS filtering behavior (non-FIPS curves are silently filtered in FIPS mode)
- ✓ Validate successful TLS handshakes using the configured curves
- ✓ Verify feature gate enforcement for PQC hybrid curves

**Test Environments:**
- Non-FIPS cluster
- FIPS-enabled cluster
- Hypershift

**Expected Results:**
- TLS connections successfully negotiate using configured curves
- Non-FIPS curves are filtered when FIPS mode is enabled
- PQC hybrid curves require `TLSCurvePreferences` feature gate

---

#### E2: Verify TLS curve preferences for ovn-kubernetes nodes

**Component:** `ovn-kubernetes` node daemons  
**Namespace:** `openshift-ovn-kubernetes`

**Objective:**  
Validates that OVN-Kubernetes node components (ovnkube-node, ovnkube-controller) correctly apply TLS curve preferences for metrics and health HTTPS endpoints.

**Test Coverage:**
- ✓ Verify all supported TLS curves/groups (see [Supported TLS Curves/Groups](#supported-tls-curvesgroups))
- ✓ Validate FIPS filtering behavior (non-FIPS curves are silently filtered in FIPS mode)
- ✓ Validate successful TLS handshakes using the configured curves
- ✓ Verify feature gate enforcement for PQC hybrid curves

**Test Environments:**
- Non-FIPS cluster
- FIPS-enabled cluster
- Hypershift

**Expected Results:**
- TLS connections successfully negotiate using configured curves
- Non-FIPS curves are filtered when FIPS mode is enabled
- PQC hybrid curves require `TLSCurvePreferences` feature gate

---

#### E3: Verify TLS curve preferences for cluster-network-operator

**Component:** `cluster-network-operator` (CNO)  
**Namespace:** `openshift-network-operator`

**Objective:**  
Validates that the CNO's own metrics and health HTTPS endpoints correctly apply configured TLS curve preferences.

**Test Coverage:**
- ✓ Verify all supported TLS curves/groups (see [Supported TLS Curves/Groups](#supported-tls-curvesgroups))
- ✓ Validate FIPS filtering behavior (non-FIPS curves are silently filtered in FIPS mode)
- ✓ Validate successful TLS handshakes using the configured curves
- ✓ Verify feature gate enforcement for PQC hybrid curves

**Test Environments:**
- Non-FIPS cluster
- FIPS-enabled cluster
- Hypershift

**Expected Results:**
- TLS connections successfully negotiate using configured curves
- Non-FIPS curves are filtered when FIPS mode is enabled
- PQC hybrid curves require `TLSCurvePreferences` feature gate

---

#### E4: Verify TLS curve preferences for networking-console-plugin

**Component:** `networking-console-plugin`  
**Namespace:** `openshift-network-console`

**Objective:**  
Validates that the OpenShift Console networking plugin backend server correctly applies TLS curve preferences for HTTPS connections.

**Test Coverage:**
- ✓ Verify all supported TLS curves/groups (see [Supported TLS Curves/Groups](#supported-tls-curvesgroups))
- ✓ Validate FIPS filtering behavior (non-FIPS curves are silently filtered in FIPS mode)
- ✓ Validate successful TLS handshakes using the configured curves
- ✓ Verify feature gate enforcement for PQC hybrid curves

**Test Environments:**
- Non-FIPS cluster
- FIPS-enabled cluster
- Hypershift

**Expected Results:**
- TLS connections successfully negotiate using configured curves
- Non-FIPS curves are filtered when FIPS mode is enabled
- PQC hybrid curves require `TLSCurvePreferences` feature gate

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
| **Non-FIPS** | Standard OCP installation | Critical | Baseline functionality validation |
| **FIPS-enabled** | FIPS mode enabled (`/proc/sys/crypto/fips_enabled=1`) | Critical | Compliance validation |
| **Hypershift** | Hosted control plane | High | Cloud-native deployment model |

## Test Deliverables

| Deliverable | Description | Target Audience |
|-------------|-------------|-----------------|
| **E2E Test Automation** | Downstream PRs with e2e tests in `cluster-network-operator` repository | QE, CI/CD (Prow) |
| **Test Execution Report** | Comprehensive test results with coverage metrics and findings | QE, Engineering |
| **Documentation Feedback** | Configuration examples, FIPS behavior notes, known limitations | Docs team |
| **Bug Reports** | Detailed reports for any discrepancies between expected and actual behavior | Engineering |

## Test Tasks

| Task | Status | Owner | Notes |
|------|--------|-------|-------|
| Test plan creation and review | ☐ | QE Team | This document |
| Execute e2e tests on non-FIPS cluster | ☐ | QE Team | All 4 components |
| Execute e2e tests on FIPS cluster | ☐ | QE Team | Validate curve filtering |
| Execute e2e tests on Hypershift | ☐ | QE Team | Hosted control plane |
| Automate curve validation script | ☐ | QE Team | For all 4 CNO-managed components |
| Document test results | ☐ | QE Team | Create test execution report |
| File bugs for failures | ☐ | QE Team | Track issues in Jira |
| Provide documentation feedback | ☐ | QE Team | Configuration examples and notes |

## Pass/Fail Criteria

### Pass Criteria

The feature is considered **ready for release** when all of the following conditions are met:

- ✓ **Zero critical or major defects** remain open in Jira
- ✓ **All e2e test cases** (E1-E4) pass consistently in Prow CI
- ✓ **FIPS compliance validated** — Non-FIPS curves are correctly filtered in FIPS mode
- ✓ **Feature gate enforcement verified** — PQC hybrid curves require `TLSCurvePreferences` feature gate
- ✓ **Test automation complete** — All tests merged into `cluster-network-operator` repository
- ✓ **Documentation feedback provided** — Configuration examples and known limitations shared with Docs team
- ✓ **Test execution report published** — Comprehensive results documented

### Fail Criteria

The feature is **NOT ready for release** if any of the following occur:

- ✗ **Critical defects** that block core TLS functionality
- ✗ **FIPS compliance failures** — Non-FIPS curves negotiated during TLS handshake in FIPS mode
- ✗ **Feature gate bypass** — PQC curves work without required feature gate
- ✗ **Consistent test failures** on Prow CI
- ✗ **Security vulnerabilities** identified in TLS configuration handling

## Risks and Mitigation

| Risk | Impact | Probability | Mitigation |
|------|--------|-------------|------------|
| **Go version dependency** | `SecP256r1MLKEM768` requires Go 1.26+ | Medium | Document as future availability; test other PQC curves |
| **FIPS certification delays** | PQC hybrid curves may need additional FIPS approval | Low | Focus on classical curve validation first |
| **Platform-specific issues** | Different behavior on ARM64, s390x, ppc64le | Low | Test on all supported architectures |
| **Third-party client compatibility** | Older clients may not support PQC curves | Medium | Ensure graceful fallback to classical curves |

### Current Status

**No known blockers or critical limitations** at the time of test plan creation.

---


**End of Test Plan**
