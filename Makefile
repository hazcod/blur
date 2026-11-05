.PHONY: test race vet build cross-build physical-cross-build cross-test-compile no-cgo-audit

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

build:
	CGO_ENABLED=0 go build ./...

# Keep shipped target tuples synchronized with tools/no-cgo-audit.SupportedTargets.
cross-build: physical-cross-build
	@set -eu; \
	for target in darwin/arm64 darwin/amd64 windows/amd64 linux/amd64 linux/arm64; do \
		GOOS=$${target%/*}; GOARCH=$${target#*/}; \
		for package in ./cmd/blur-core ./cmd/blurctl; do \
			echo "Building $$package for $$GOOS/$$GOARCH with cgo disabled"; \
			CGO_ENABLED=0 GOOS=$$GOOS GOARCH=$$GOARCH go build -o /dev/null $$package; \
		done; \
	done

# Keep the physical-package matrix synchronized with the audit and command matrix above.
physical-cross-build:
	@set -eu; \
	for target in darwin/arm64 darwin/amd64 windows/amd64 linux/amd64 linux/arm64; do \
		GOOS=$${target%/*}; GOARCH=$${target#*/}; \
		echo "Building ./internal/storage/physical/bolt for $$GOOS/$$GOARCH with cgo disabled"; \
		CGO_ENABLED=0 GOOS=$$GOOS GOARCH=$$GOARCH go build -o /dev/null ./internal/storage/physical/bolt; \
	done

cross-test-compile:
	@set -eu; \
	for target in darwin/arm64 windows/amd64 linux/amd64; do \
		GOOS=$${target%/*}; GOARCH=$${target#*/}; \
		echo "Compiling platform conformance tests for $$GOOS/$$GOARCH with cgo disabled"; \
		CGO_ENABLED=0 GOOS=$$GOOS GOARCH=$$GOARCH go test -c -o /dev/null ./internal/platform/conformance; \
	done

no-cgo-audit:
	go run ./tools/no-cgo-audit
