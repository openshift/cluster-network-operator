# Cluster Network Operator - AI Assistant Guide

This guide provides context and conventions for working with the OpenShift Cluster Network Operator (CNO) repository. It's designed to help AI assistants (Claude, Cursor, GitHub Copilot, etc.) understand the codebase structure, development workflow, and key concepts.

**For AI Tools:**
- **Claude Code/Claude API**: Read this file for repository context
- **Cursor IDE**: This file complements `.cursorrules` with detailed context
- **GitHub Copilot**: Use as reference for code suggestions and completions
- **Other AI Assistants**: Ingest this as codebase documentation

**For Humans**: This file also serves as a quick reference guide for developers new to the project.

## Repository Overview

The Cluster Network Operator (CNO) installs and upgrades networking components on OpenShift Kubernetes clusters. It's a **Second-Level Operator (SLO)** managed by the Cluster Version Operator (CVO).

**Key Facts:**
- **Component**: Networking
- **Subcomponent**: cluster-network-operator
- **Primary Language**: Go
- **Architecture**: Controller pattern using controller-runtime
- **Default Network Plugin**: OVNKubernetes
- **Run Level**: 07 (early in upgrade process)

## Architecture Overview

CNO follows the Kubernetes controller pattern:

1. **Watch** configuration objects (Network.config.openshift.io, Network.operator.openshift.io)
2. **Reconcile** cluster state against desired configuration
3. **Create/Update** downstream operand resources (DaemonSets, Deployments, etc.)
4. **Report** status via ClusterOperator object

### Two Configuration Levels

**Cluster Config** (`Network.config.openshift.io/cluster`):
- High-level user-facing API
- Set during installation via install-config.yaml
- View: `oc get Network.config.openshift.io cluster -oyaml`

**Operator Config** (`Network.operator.openshift.io/cluster`):
- Lower-level detailed configuration
- Auto-generated from Cluster Config
- Can be customized in `manifests/cluster-network-03-config.yml`
- View: `oc get network.operator cluster -oyaml`

### Key Controllers

Located in `pkg/controller/`:

1. **Cluster Config Controller**: Translates config.openshift.io to operator.openshift.io
2. **Network Controller**: Main controller that deploys network operands
3. **Egress Router Controller**: Manages egress routing configuration
4. **Ingress Config Controller**: Watches ingress configuration
5. **Operator PKI Controller**: Manages certificates and signing
6. **Proxy Config Controller**: Handles proxy configuration
7. **Connectivity Check Controller**: Validates network connectivity

## Directory Structure

```text
cluster-network-operator/
├── cmd/                      # Main entry points
│   ├── cluster-network-operator/    # Main operator binary
│   └── cluster-network-*-check-*/   # Connectivity check binaries
├── pkg/                      # Core implementation
│   ├── controller/           # Controller implementations (16 controllers)
│   ├── network/              # Network plugin implementations
│   ├── platform/             # Platform detection (AWS, Azure, GCP, etc.)
│   ├── render/               # Manifest rendering logic
│   ├── apply/                # Resource application logic
│   ├── bootstrap/            # Bootstrap configuration
│   └── util/                 # Utility packages
├── manifests/                # Kubernetes manifests and CRDs
│   ├── 0000_*.yaml          # CRD definitions
│   ├── 01-*.yaml            # Namespace, ServiceAccount, RBAC
│   └── image-references     # Image references for CVO
├── bindata/                  # Embedded network plugin manifests
│   ├── network/ovn-kubernetes/      # OVNKubernetes manifests
│   ├── network/openshift-sdn/       # OpenShift SDN manifests (deprecated)
│   └── network/multus/              # Multus CNI manifests
├── profile-patches/          # Configuration profile patches
├── hack/                     # Development scripts
│   ├── update-codegen.sh    # Code generation
│   └── test-*.sh            # Test scripts
├── docs/                     # Documentation
│   ├── architecture.md      # Architecture overview
│   ├── operands.md          # Operand descriptions
│   └── *.md                 # Feature-specific docs
└── vendor/                   # Vendored dependencies
```

## Development Workflow

### Prerequisites

- Go 1.21+
- golangci-lint (install via `make install.tools`)
- OpenShift cluster for testing (optional but recommended)

### Common Commands

```bash
# Build the operator
make build

# Run all checks (verify + test-unit + golangci-lint)
make check

# Run unit tests
make test-unit

# Run linter
make golangci-lint

# Update generated code
make update-codegen

# Verify generated code is up-to-date
make verify-update-codegen
make verify

# Clean build artifacts
make clean
```

### Testing

**Unit Tests:**
- Located alongside source files with `_test.go` suffix
- Run with: `make test-unit`
- Test packages: `./pkg/... ./cmd/...`

**Integration Tests:**
- Require live OpenShift cluster
- Located in e2e test suites (separate repo)

### Code Generation

The operator uses code generation for:
- Client libraries
- Informers
- Listers
- Deep copy functions

**Update generated code:**
```bash
make update-codegen
```

**Verify it's up-to-date:**
```bash
make verify-update-codegen
```

## Key Concepts

### Network Operands

The CNO deploys and manages several **operands** (downstream components):

1. **OVNKubernetes**: Default network plugin
   - DaemonSet: `ovnkube-node`
   - DaemonSet: `ovnkube-control-plane`
   - Components: ovn-controller, ovn-northd, OVN databases

2. **Multus**: Manages multiple network interfaces
   - DaemonSet: `multus`

3. **Network Metrics Daemon**: Collects network metrics
   - DaemonSet: `network-metrics-daemon`

4. **Kube Proxy** (optional): Service proxy
   - DaemonSet: `kube-proxy` (disabled by default with OVN)

See `docs/operands.md` for detailed operand documentation.

### Image References

**CRITICAL**: Only use images provided by CVO via environment variables.

The CVO sets environment variables like:
- `NETWORK_OPERATOR_IMAGE`
- `OVN_IMAGE`
- `MULTUS_IMAGE`
- `KUBE_RBAC_PROXY_IMAGE`

These are referenced in code via `os.Getenv()` and injected at runtime.

**Never hardcode image references** - even to `quay.io/openshift`.

### Bootstrap Process

During cluster installation:
1. Bootstrap API server starts
2. CNO deploys early, before full cluster is ready
3. Nodes are tainted: `node.kubernetes.io/not-ready`, `node.kubernetes.io/network-unavailable`
4. CNO operands must tolerate these taints
5. As networking starts, taints are removed
6. Rest of cluster components can start

**Key Tolerance Requirements:**
- `node.kubernetes.io/not-ready`
- `node.kubernetes.io/network-unavailable`
- `node-role.kubernetes.io/master` (no workers yet during bootstrap)

### Upgrade Ordering

- **CNO Run Level**: 07 (early)
- **MCO (Machine Config Operator)**: Runs AFTER CNO
- **Implication**: New network components may run on older machine images
- **Requirement**: Maintain backward compatibility with older node configurations

## Configuration Examples

### Basic OVNKubernetes Cluster

```yaml
apiVersion: operator.openshift.io/v1
kind: Network
metadata:
  name: cluster
spec:
  clusterNetwork:
  - cidr: 10.128.0.0/14
    hostPrefix: 23
  serviceNetwork:
  - 172.30.0.0/16
  defaultNetwork:
    type: OVNKubernetes
    ovnKubernetesConfig:
      mtu: 1400
      genevePort: 6081
```

### Enabling IPsec

See `docs/enabling_ns_ipsec.md` for details.

```yaml
spec:
  defaultNetwork:
    ovnKubernetesConfig:
      ipsecConfig:
        mode: Full
```

### Hybrid Networking (Linux + Windows)

```yaml
spec:
  defaultNetwork:
    ovnKubernetesConfig:
      hybridOverlayConfig:
        hybridClusterNetwork:
        - cidr: 10.132.0.0/14
          hostPrefix: 23
```

## Code Navigation Tips

### Finding Controllers

Controllers are in `pkg/controller/<name>/`:
- Each controller has its own package
- Main reconciliation logic is typically in `controller.go`
- Look for `Reconcile()` methods implementing controller-runtime's reconcile.Reconciler

### Finding Network Plugin Logic

Network plugin implementations are in:
- `pkg/network/`: Core network configuration logic
- `bindata/network/<plugin>/`: Embedded manifests for each plugin

### Platform Detection

Platform-specific logic is in `pkg/platform/`:
- AWS, Azure, GCP, BareMetal, vSphere, etc.
- Platform detection affects MTU, networking features, etc.

### Rendering Manifests

The `pkg/render/` package handles rendering manifests with:
- Template substitution
- Platform-specific customization
- Feature flag handling

## Common Development Tasks

### Adding a New Configuration Field

1. Update the API type in `openshift/api` repository (separate repo)
2. Vendor the updated API: `go mod vendor`
3. Update controller logic in `pkg/controller/`
4. Update rendering logic in `pkg/render/` if needed
5. Add unit tests
6. Update codegen: `make update-codegen`
7. Verify: `make check`

### Modifying an Operand

1. Update manifests in `bindata/network/<plugin>/`
2. Update rendering logic if needed
3. Consider upgrade path and backward compatibility
4. Test on live cluster
5. Update `docs/operands.md` if behavior changes

### Debugging

**View Operator Logs:**
```bash
oc logs -n openshift-network-operator deployment/network-operator
```

**View Network Config:**
```bash
# High-level config
oc get Network.config.openshift.io cluster -oyaml

# Operator config
oc get network.operator cluster -oyaml

# Operator status
oc get clusteroperator network
```

**View Operand Status:**
```bash
# OVN pods
oc get pods -n openshift-ovn-kubernetes

# Multus pods
oc get pods -n openshift-multus

# All network-related pods
oc get pods -A | grep -E '(ovn|multus|network)'
```

## Important Conventions

### Code Style

- Follow standard Go conventions
- Use `golangci-lint` for linting
- Vendor dependencies (use `go mod vendor`)
- Keep controller logic focused and single-purpose

### Commit Messages

Use conventional commit format:
```text
<type>: <subject>

<body>

<footer>
```

Types: `fix`, `feat`, `docs`, `chore`, `test`, `refactor`

### Pull Requests

- All PRs require review from `core-reviewers`
- Approval from `core-approvers`
- CI must pass (Prow jobs)
- Component: Networking
- Subcomponent: cluster-network-operator

### Testing Requirements

- Unit tests for new code
- Integration tests for significant features
- Upgrade tests for configuration changes
- All tests must pass before merge

## Troubleshooting Tips

### Network Operands Not Starting

1. Check operator logs for errors
2. Verify ClusterOperator status: `oc get co network`
3. Check if required images are available
4. Verify node taints and tolerations

### Configuration Not Applied

1. Check if Cluster Config matches Operator Config
2. Operator Config takes precedence
3. Some fields cannot be changed post-install (clusterNetwork, serviceNetwork)

### Upgrade Issues

1. Remember CNO upgrades before MCO
2. Check compatibility with older machine configs
3. Review runlevel dependencies
4. Check ClusterOperator conditions

## Useful Resources

- **Architecture**: `docs/architecture.md`
- **Operands**: `docs/operands.md`
- **OVN Node Mode**: `docs/ovn_node_mode.md`
- **IPsec**: `docs/enabling_ns_ipsec.md`
- **API Reference**: [openshift/api](https://github.com/openshift/api) (separate repo)
- **CVO Operator Guide**: [CVO Operator Guide](https://github.com/openshift/cluster-version-operator/blob/master/docs/dev/operators.md)

## File Patterns to Recognize

- `*_test.go`: Unit tests
- `zz_generated_*.go`: Auto-generated code (DO NOT EDIT)
- `bindata/`: Embedded static data
- `manifests/`: Kubernetes manifests deployed by CVO
- `profile-patches/`: Manifest patches for different profiles
- `hack/`: Development and CI scripts

## Special Considerations

### Image References

Always use environment variables for images:
```go
os.Getenv("OVN_IMAGE")
os.Getenv("MULTUS_IMAGE")
```

Never hardcode `quay.io` or registry URLs.

### Backward Compatibility

- Configuration changes must be backward compatible
- Support running new code on old machine images
- Consider upgrade paths from N-1 versions

### Bootstrap Taints

All critical operands must tolerate:
```yaml
tolerations:
- key: node.kubernetes.io/not-ready
  operator: Exists
- key: node.kubernetes.io/network-unavailable
  operator: Exists
- key: node-role.kubernetes.io/master
  operator: Exists
```

### Platform Differences

Network configuration varies by platform:
- MTU differs (AWS: 9001, others: 1500)
- Security groups (AWS, GCP)
- Load balancer integration
- Always check `pkg/platform/` for platform-specific logic

## Quick Reference

| Task | Command |
|------|---------|
| Build | `make build` |
| Test | `make test-unit` |
| Lint | `make golangci-lint` |
| Check All | `make check` |
| Update Codegen | `make update-codegen` |
| Verify Codegen | `make verify` |
| Clean | `make clean` |
| View Config | `oc get network.operator cluster -oyaml` |
| View Status | `oc get clusteroperator network` |
| View Logs | `oc logs -n openshift-network-operator deployment/network-operator` |

---

**Remember**: This is a critical operator running early in cluster lifecycle. Changes must be thoroughly tested, backward compatible, and consider the bootstrap process, upgrade ordering, and platform differences.
