.PHONY: test race vet build cross-build no-cgo-audit

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

no-cgo-audit:
	@set -eu; \
	imports=$$(rg -n '^[[:space:]]*import[[:space:]]+"C"' --glob '*.go' internal cmd || true); \
	if [ -n "$$imports" ]; then \
		echo "Found import C in application source:"; \
		echo "$$imports"; \
		exit 1; \
	fi; \
	cgo_files=$$(CGO_ENABLED=0 go list -deps -f '{{if .CgoFiles}}{{.ImportPath}}:{{join .CgoFiles ","}}{{end}}' ./... | sed '/^$$/d'); \
	if [ -n "$$cgo_files" ]; then \
		echo "Found cgo files in the disabled-cgo dependency graph:"; \
		echo "$$cgo_files"; \
		exit 1; \
	fi; \
	echo "No import C or CgoFiles found in the CGO_ENABLED=0 dependency graph"
