# Makefile for CNO OpenShift Tests Extension
#
# Build the OTE binary (also packaged into the operator image via Dockerfile):
#   make -C openshift-tests-ext build

BINARY_NAME := cluster-network-operator-tests-ext
BUILD_DIR := bin

.PHONY: all
all: build

.PHONY: build
build:
	@echo "Building $(BINARY_NAME)..."
	./hack/build-tests-ext.sh

.PHONY: update-test-lists
update-test-lists:
	./hack/update-test-lists.sh

.PHONY: validate-test-lists
validate-test-lists:
	./hack/validate-test-lists.sh

.PHONY: clean
clean:
	@echo "Cleaning build artifacts..."
	rm -rf $(BUILD_DIR)
	rm -rf vendor
