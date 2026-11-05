.PHONY: test race vet build cross-build cross-test-compile no-cgo-audit

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

build:
	CGO_ENABLED=0 go build ./...

cross-build:
	@set -eu; \
	for target in darwin/arm64 darwin/amd64 windows/amd64 linux/amd64 linux/arm64; do \
		GOOS=$${target%/*}; GOARCH=$${target#*/}; \
		for package in ./cmd/blur-core ./cmd/blurctl; do \
			echo "Building $$package for $$GOOS/$$GOARCH with cgo disabled"; \
			CGO_ENABLED=0 GOOS=$$GOOS GOARCH=$$GOARCH go build -o /dev/null $$package; \
		done; \
	done

cross-test-compile:
	@set -eu; \
	for target in darwin/arm64 windows/amd64 linux/amd64; do \
		GOOS=$${target%/*}; GOARCH=$${target#*/}; \
		echo "Compiling platform conformance tests for $$GOOS/$$GOARCH with cgo disabled"; \
		CGO_ENABLED=0 GOOS=$$GOOS GOARCH=$$GOARCH go test -c -o /dev/null ./internal/platform/conformance; \
	done

no-cgo-audit:
	go run ./cmd/no-cgo-audit
