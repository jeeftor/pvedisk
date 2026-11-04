.DEFAULT_GOAL := help

.PHONY: help build fmt vet test integration check

help: ## Show available targets (default)
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  %-14s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the native binary (Linux requires libguestfs-dev and CGO)
	sh scripts/build.sh

fmt: ## Format Go source
	gofmt -w main.go internal

vet: ## Run Go vet
	go vet ./...

test: ## Run unit tests
	go test ./...
	sh scripts/test-build.sh

integration: ## Run Linux disk-image integration tests (requires libguestfs)
	LIBGUESTFS_BACKEND=direct LIBGUESTFS_BACKEND_SETTINGS=force_tcg go test -tags integration -timeout 15m ./internal/appliance

check: ## Verify formatting, vetting, tests, and shell syntax
	@test -z "$$(gofmt -l main.go internal)" || { gofmt -l main.go internal; exit 1; }
	go vet ./...
	go test ./...
	shellcheck scripts/*.sh
	sh scripts/test-build.sh
