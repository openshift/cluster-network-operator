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

### Native Ginkgo (authoring / any cluster)

Specs live under [`../test/e2e`](../test/e2e). The suite configures the k8s e2e
`framework.TestContext` from `-kubeconfig` / `KUBECONFIG`:

```bash
export KUBECONFIG=/path/to/kubeconfig
make -C test test-e2e
# or: FOCUS='substring' make -C test test-e2e
# or: ./test/scripts/run.sh substring
```

### OTE binary (CI parity)

OTE initializes `framework.TestContext` via `AddBeforeAll` (see `provider.go`)
before specs run.

```bash
export KUBECONFIG=/path/to/kubeconfig
make -C openshift-tests-ext build

./openshift-tests-ext/bin/cluster-network-operator-tests-ext list suites
./openshift-tests-ext/bin/cluster-network-operator-tests-ext list tests

./openshift-tests-ext/bin/cluster-network-operator-tests-ext run-test --names "substring"
```

## Enable / disable tests (Informing / Blocking)

Only specs listed in [`test/tests.go`](test/tests.go) run (opt-in).

| List | Lifecycle | Meaning |
|------|-----------|---------|
| `InformingTests` | informing | Runs in CI; failures do not block |
| `BlockingTests` | blocking | Runs in CI; failures block |
| (unlisted) | — | Not included in the extension |

After adding a Ginkgo spec under `test/e2e`:

```bash
# Regenerate lists: new names land in InformingTests; BlockingTests membership is kept
make -C openshift-tests-ext update-test-lists

# Or validate that tests.go matches discovered specs
make -C openshift-tests-ext validate-test-lists
```

To graduate a test: move its full name from `InformingTests` to `BlockingTests`,
rebuild, and confirm lifecycle with `list tests`.
