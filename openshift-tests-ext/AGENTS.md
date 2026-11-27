# AGENTS.md — OpenShift Tests Extension (OTE)

Context for AI coding agents working on `openshift-tests-ext/`.

## What is OTE?

OTE discovers native CNO Ginkgo specs from `test/e2e`, assigns lifecycle
(informing/blocking), suite labels, and ships as `cluster-network-operator-tests-ext`
in the operator image for OpenShift CI.

Do **not** add Ginkgo specs under this directory. Put new cases in `test/e2e`
so they are blank-imported here and runnable via this OTE binary (local + CI).

## Directory Layout

```text
openshift-tests-ext/
  cmd/
    cluster-network-operator-tests-ext/  # OTE binary: suites, labels, TestContext init
    list-test-names/                     # Prints names from tests.go (validate-test-lists)
  test/
    tests.go                             # InformingTests / BlockingTests (opt-in; edit by hand)
    e2e_test.go                          # Blank-imports test/e2e; go:generate annotations
    annotate/                            # Annotation generator (rules + Run)
    generated/                           # zz_generated.annotations.go (do not hand-edit)
  hack/
    build-tests-ext.sh                   # Vendor + build; runs go.mod consistency check
    check-go-mod-consistency.sh          # Keep test/e2e and OTE go.mod kube deps aligned
    update-annotations.sh                # Regenerate zz_generated.annotations.go
    run-e2e.sh                           # Expand NAMES= and invoke run-test per match
    validate-test-lists.sh               # Diff tests.go vs binary discovery
  Makefile
  README.md
```

Native specs and helpers live in `test/e2e/` (separate Go module), including:

- `cno.go` — migrated OTP conformance specs (`CNO …`, `Label("Disruptive")`)
- `env_overrides.go` — env-overrides `_global` coverage
- `cli.go` / `util.go` — shared helpers for OTP specs

## Build and Run

```bash
# Build (also runs check-go-mod-consistency.sh)
make -C openshift-tests-ext build

# Local or CI-parity: run enabled specs via the OTE binary
export KUBECONFIG=/path/to/kubeconfig
make -C openshift-tests-ext test-e2e NAMES='env-overrides'
```

## How Specs Are Registered

1. `test/e2e` packages register Ginkgo `Describe` / `It` (or `Specify`) specs.
2. OTE blank-imports `test/e2e` and calls `BuildExtensionTestSpecsFromOpenShiftGinkgoSuite`.
3. Each spec gets Labels `sig-network` and `cluster-network-operator-ote`.
   `AppendedAnnotations` appends `[Serial]` (when labeled) and `[Suite:…]` to the
   original Ginkgo name; remaining Labels are sorted and prepended (ovn-kubernetes
   OTE pattern). Serial/Disruptive stay out of the prefix because they live in the
   annotation suffix.
4. Only names listed in `InformingTests` / `BlockingTests` are enabled.

After adding a spec under `test/e2e`:

1. `make -C openshift-tests-ext update-annotations`
2. Add the full OTE name to `InformingTests` or `BlockingTests` in `test/tests.go`
3. `make -C openshift-tests-ext build` and verify with `list tests`
4. `make -C openshift-tests-ext validate-test-lists`

## Go Module Consistency

`test/e2e/go.mod` and `openshift-tests-ext/go.mod` must share the same `go`
version and the same `k8s.io/{api,apimachinery,client-go,kubernetes}` versions.
`hack/check-go-mod-consistency.sh` enforces this on every OTE build. Bump both
modules together when updating the e2e kube stack.
