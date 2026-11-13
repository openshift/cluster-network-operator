# Makefile for CNO OpenShift Tests Extension
#
# Build the OTE binary (also packaged into the operator image via Dockerfile):
#   make -C openshift-tests-ext build
#
# Run specs via the OTE binary (local or CI-parity; requires KUBECONFIG):
#   make -C openshift-tests-ext test-e2e NAMES='env-overrides'

BINARY_NAME := cluster-network-operator-tests-ext
BUILD_DIR := bin

.PHONY: all
all: build

.PHONY: build
build:
	@echo "Building $(BINARY_NAME)..."
	./hack/build-tests-ext.sh

.PHONY: verify-go-mod
verify-go-mod:
	./hack/check-go-mod-consistency.sh

# Run enabled specs through the OTE binary (NAMES= substring or exact name).
.PHONY: test-e2e
test-e2e: build
	./hack/run-e2e.sh "$(NAMES)"

.PHONY: update-annotations
update-annotations:
	./hack/update-annotations.sh

.PHONY: validate-test-lists
validate-test-lists:
	./hack/validate-test-lists.sh

.PHONY: clean
clean:
	@echo "Cleaning build artifacts..."
	rm -rf $(BUILD_DIR)
	rm -rf vendor
