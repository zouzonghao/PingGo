# Makefile for PingGo

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GORUN=$(GOCMD) run

# Platform detection
NATIVE_GOOS := $(shell go env GOHOSTOS)
NATIVE_GOARCH := $(shell go env GOHOSTARCH)
TARGET_GOOS := $(or $(GOOS),$(NATIVE_GOOS))
TARGET_GOARCH := $(or $(GOARCH),$(NATIVE_GOARCH))

# Binary naming
BINARY_NAME=pinggo

# Default target
default: run

# Development mode
run:
	@echo "Running in development mode..."
	@$(GORUN) .

# Minify assets (manually call this if needed)
minify:
	@echo "Minifying assets..."
	@$(GORUN) scripts/build_assets.go

# Build for current platform
build:
	@{ \
		BINARY_FILENAME=$(BINARY_NAME)-$(TARGET_GOOS)-$(TARGET_GOARCH); \
		if [ "$(TARGET_GOOS)" = "windows" ]; then \
			BINARY_FILENAME=$${BINARY_FILENAME}.exe; \
		fi; \
		echo "--> Building for $(TARGET_GOOS)/$(TARGET_GOARCH)..."; \
		CGO_ENABLED=0 GOOS=$(TARGET_GOOS) GOARCH=$(TARGET_GOARCH) $(GOBUILD) -ldflags="-s -w" -o $$BINARY_FILENAME .; \
		echo "--> Built: $$BINARY_FILENAME"; \
	}

# Build all platforms
release-all:
	@$(MAKE) build GOOS=linux GOARCH=amd64
	@$(MAKE) build GOOS=windows GOARCH=amd64
	@$(MAKE) build GOOS=darwin GOARCH=arm64
	@echo "All release builds complete."

# Clean build artifacts
clean:
	@echo "Cleaning up..."
	@rm -f pinggo pinggo-*
	@echo "Clean complete."

.PHONY: default run minify build release-all clean
