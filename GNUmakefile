SHELL = bash
default: dev

GIT_COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE  := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
GO          := go
BINARY_NAME := nomad-driver-container
BUILD_DIR   := ./build

LDFLAGS := -X github.com/hashicorp/nomad-driver-container/version.Version=$(shell cat version/version.go | grep 'Version =' | head -1 | awk '{print $$3}' | tr -d '"') \
           -X github.com/hashicorp/nomad-driver-container/version.VersionPreRelease=$(shell cat version/version.go | grep 'VersionPreRelease =' | head -1 | awk '{print $$3}' | tr -d '"')

.PHONY: default dev build clean lint test fmt tidy sign

# The signing identity to use.  Override on the command line:
#   make sign SIGNING_IDENTITY="Developer ID Application: Your Name (TEAMID)"
#   make sign SIGNING_IDENTITY="Apple Development: Your Name (TEAMID)"
SIGNING_IDENTITY ?=

# Build a development binary (includes debug symbols).
dev:
	@mkdir -p $(BUILD_DIR)
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) .
	@echo "=> $(BUILD_DIR)/$(BINARY_NAME)"

# Build a release binary (stripped, no debug info).
build:
	@mkdir -p $(BUILD_DIR)
	$(GO) build \
		-ldflags "-s -w $(LDFLAGS)" \
		-trimpath \
		-o $(BUILD_DIR)/$(BINARY_NAME) .
	@echo "=> $(BUILD_DIR)/$(BINARY_NAME)"

# Run tests (requires a Nomad dev agent and the container CLI).
test:
	$(GO) test -v -timeout=10m ./...

# Run go vet and staticcheck linting.
lint:
	$(GO) vet ./...
	@which staticcheck > /dev/null 2>&1 && staticcheck ./... || \
		echo "staticcheck not found; install with: go install honnef.co/go/tools/cmd/staticcheck@latest"

# Format source files.
fmt:
	$(GO) fmt ./...

# Tidy the module's dependency graph.
tidy:
	$(GO) mod tidy

# Remove build artefacts.
clean:
	rm -rf $(BUILD_DIR)

# Sign the binary with a Developer ID (or Apple Development) certificate.
# Requires Xcode Command Line Tools.  Run after `make build` or `make dev`.
#   make sign
#   make sign SIGNING_IDENTITY="Developer ID Application: Your Name (TEAMID)"
sign: $(BUILD_DIR)/$(BINARY_NAME)
	@if [ -z "$(SIGNING_IDENTITY)" ]; then \
		echo "error: SIGNING_IDENTITY is not set"; exit 1; \
	fi
	@security find-identity -v -p codesigning | grep -qF "$(SIGNING_IDENTITY)" || { \
		echo "error: signing identity \"$(SIGNING_IDENTITY)\" not found in keychain"; exit 1; \
	}
	codesign \
		--sign "$(SIGNING_IDENTITY)" \
		--options runtime \
		--entitlements entitlements.plist \
		--timestamp \
		--force \
		$(BUILD_DIR)/$(BINARY_NAME)
	codesign --verify --strict --verbose=2 $(BUILD_DIR)/$(BINARY_NAME)
	@codesign --display --verbose=2 $(BUILD_DIR)/$(BINARY_NAME) 2>&1 | grep -E "Authority|TeamIdentifier|Timestamp"
	@echo "=> signed $(BUILD_DIR)/$(BINARY_NAME)"
