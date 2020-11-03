all: build
.PHONY: all

# Include the library makefile
include $(addprefix ./vendor/github.com/openshift/build-machinery-go/make/, \
	golang.mk \
	targets/openshift/deps-gomod.mk \
)

# Run core verification and all self contained tests.
#
# Example:
#   make check
check: | verify test-unit
.PHONY: check

clean:
	$(RM) cluster-network-operator
.PHONY: clean

GO_TEST_PACKAGES :=./pkg/... ./cmd/...
