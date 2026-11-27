# OpenShift Tests Extension for Cluster Network Operator

OTE binary that **enables** native CNO e2e specs from [`../test/e2e`](../test/e2e).
Do not add Ginkgo tests under this directory — put new e2e cases in `test/e2e`
so they are discovered via the blank import in [`test/e2e_test.go`](test/e2e_test.go).

## Build

```bash
make -C openshift-tests-ext build
```

Binary: `openshift-tests-ext/bin/cluster-network-operator-tests-ext`

## Run locally

Use the OTE binary for local runs and CI parity. Specs live in `test/e2e`
(for example `cno.go`, `env_overrides.go`); this directory only enables them.
OTE initializes `framework.TestContext` via `AddBeforeAll` (see `provider.go`).

```bash
export KUBECONFIG=/path/to/kubeconfig
make -C openshift-tests-ext build

./openshift-tests-ext/bin/cluster-network-operator-tests-ext list suites
./openshift-tests-ext/bin/cluster-network-operator-tests-ext list tests

# Substring or exact InformingTests/BlockingTests name (NAMES= required)
make -C openshift-tests-ext test-e2e NAMES='env-overrides'
```

## Enable / disable tests (Informing / Blocking)

Only specs listed in [`test/tests.go`](test/tests.go) run (opt-in).

| List | Lifecycle | Meaning |
|------|-----------|---------|
| `InformingTests` | informing | Runs in CI; failures do not block |
| `BlockingTests` | blocking | Runs in CI; failures block |
| (unlisted) | — | Not included in the extension |

After adding a Ginkgo spec under `test/e2e`:

1. Regenerate annotations: `make -C openshift-tests-ext update-annotations`
2. Compose the full OTE name (prepended labels + Ginkgo text + appended annotations)
   and add it to `InformingTests` or `BlockingTests` in [`test/tests.go`](test/tests.go)
3. Rebuild and confirm lifecycle with `list tests`
4. Validate: `make -C openshift-tests-ext validate-test-lists`

To graduate a test: move its full name from `InformingTests` to `BlockingTests`,
rebuild, and confirm lifecycle with `list tests`.
