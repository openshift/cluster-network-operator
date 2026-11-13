all: build
.PHONY: all

# Include the library makefile
include $(addprefix ./vendor/github.com/openshift/build-machinery-go/make/, \
	golang.mk \
	targets/openshift/deps-gomod.mk \
	targets/openshift/operator/profile-manifests.mk \
)

# This will include additional actions on the update and verify targets to ensure that profile patches are applied
# to manifest files
# $0 - macro name
# $1 - target name
# $2 - profile patches directory
# $3 - manifests directory
$(call add-profile-manifests,manifests,./profile-patches,./manifests)

# Run core verification and all self contained tests.
#
# Example:
#   make check
check: | verify test-unit golangci-lint
.PHONY: check

# Pin the linter in-repo so local and CI (`make golangci-lint`) stay aligned.
# Bump this deliberately (e.g. with a kubernetes bump) and fix any new findings in the
# same change — do not rely on the CI image's floating `latest` tag.
GOLANGCI_LINT_VERSION ?= v2.10.1
# GOTOOLCHAIN must match the Go version the pinned linter was built with (and
# go.mod)
GOLANGCI_LINT_GOTOOLCHAIN ?= go1.26.0
GOLANGCI_LINT ?= $(abspath _output/tools/bin/golangci-lint-$(GOLANGCI_LINT_VERSION))

$(GOLANGCI_LINT):
	mkdir -p $(dir $@)
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/$(GOLANGCI_LINT_VERSION)/install.sh | sh -s -- -b $(dir $@) $(GOLANGCI_LINT_VERSION)
	mv $(dir $@)/golangci-lint $@

golangci-lint: $(GOLANGCI_LINT)
	GOTOOLCHAIN=$(GOLANGCI_LINT_GOTOOLCHAIN) $(GOLANGCI_LINT) run --verbose --print-resources-usage --modules-download-mode=vendor --timeout=5m0s
	cd test && GOTOOLCHAIN=$(GOLANGCI_LINT_GOTOOLCHAIN) $(GOLANGCI_LINT) run --verbose --modules-download-mode=mod --timeout=5m0s ./...
.PHONY: golangci-lint

install.tools: $(GOLANGCI_LINT)
.PHONY: install.tools

.PHONY: update-codegen
update-codegen:
	hack/update-codegen.sh

.PHONY: verify-update-codegen
verify-update-codegen: update-codegen
	git diff --exit-code

.PHONY: verify
verify: verify-update-codegen

clean:
	$(RM) cluster-network-operator cluster-network-check-endpoints cluster-network-check-target
.PHONY: clean

.PHONY: build-e2e-tests
build-e2e-tests:
	@echo "Building cluster-network-operator-tests-ext binary..."
	cd test && ./hack/build-tests-ext.sh

GO_TEST_PACKAGES :=./pkg/... ./cmd/...
