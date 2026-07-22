# Makefile for CNO OTP Test

# Binary name
BINARY_NAME := cluster-network-operator-tests-ext

# Build directory
BUILD_DIR := bin

# Default target
.PHONY: all
all: build

# Build the binary
.PHONY: build
build:
	@echo "Building $(BINARY_NAME)..."
	./hack/build-tests-ext.sh

# Clean build artifacts
.PHONY: clean
clean:
	@echo "Cleaning build artifacts..."
	rm -rf $(BUILD_DIR)
	rm -rf vendor
