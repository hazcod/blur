.PHONY: version release-snapshot test race vet build vulncheck cross-build physical-cross-build cross-test-compile no-cgo-audit portable-packages safari-extension-test safari-extension-build macos-app macos-app-stable macos-app-sandbox-check macos-app-safari-unsigned macos-app-safari-signed run-macos

# Release version metadata, injected as main.version/commit/date. A leading "v"
# is stripped so tag v1.2.3 reports 1.2.3; untagged builds report e.g. 1.2.3-4-gabc-dirty.
VERSION ?= $(patsubst v%,%,$(shell git describe --tags --always --dirty 2>/dev/null || echo dev))
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
VERSION_LDFLAGS = -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(BUILD_DATE)

version:
	@echo $(VERSION)

# Local dry run of the release pipeline: no signing, no publishing. GoReleaser
# is fetched on demand.
release-snapshot:
	go run github.com/goreleaser/goreleaser/v2@latest release --config .github/goreleaser.yaml --snapshot --clean --skip=sign,publish

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

vulncheck:
	go run -mod=mod golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

build:
	CGO_ENABLED=0 go build ./...

# Keep shipped target tuples synchronized with cmd/no-cgo-audit.SupportedTargets.
cross-build: physical-cross-build
	@set -eu; \
	for target in darwin/arm64 windows/amd64 linux/amd64; do \
		GOOS=$${target%/*}; GOARCH=$${target#*/}; \
		for package in ./cmd/blur ./cmd/blur-core ./cmd/blurctl ./cmd/blur-sign; do \
			echo "Building $$package for $$GOOS/$$GOARCH with cgo disabled"; \
			CGO_ENABLED=0 GOOS=$$GOOS GOARCH=$$GOARCH go build -o /dev/null $$package; \
		done; \
	done

# Keep the physical-package matrix synchronized with the audit and command matrix above.
physical-cross-build:
	@set -eu; \
	for target in darwin/arm64 windows/amd64 linux/amd64; do \
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
		CGO_ENABLED=0 GOOS=$$GOOS GOARCH=$$GOARCH go test -c -o /dev/null ./internal/macospackage; \
		if [ "$$GOOS" = windows ]; then \
			echo "Compiling Windows UI Automation API contract tests"; \
			CGO_ENABLED=0 GOOS=$$GOOS GOARCH=$$GOARCH go test -c -o /dev/null ./internal/capture; \
			CGO_ENABLED=0 GOOS=$$GOOS GOARCH=$$GOARCH go test -c -o /dev/null github.com/lpintes/walk/uia; \
		fi; \
		if [ "$$GOOS" = linux ]; then \
			echo "Compiling Linux adapter tests for $$GOOS/$$GOARCH with cgo disabled"; \
			CGO_ENABLED=0 GOOS=$$GOOS GOARCH=$$GOARCH go test -c -o /dev/null ./internal/platform/linux; \
		fi; \
	done

no-cgo-audit:
	go run ./cmd/no-cgo-audit

PORTABLE_PACKAGE_DIR ?= dist/packages

# Unsigned portable Windows/Linux releases. macOS packaging remains the
# separately signed `.app` workflow below.
portable-packages:
	mkdir -p "$(PORTABLE_PACKAGE_DIR)"
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags '-H=windowsgui' -o "$(PORTABLE_PACKAGE_DIR)/Blur-windows-amd64.exe" ./cmd/blur
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags '-X main.mcpOnlyBinary=true' -o "$(PORTABLE_PACKAGE_DIR)/blur-mcp-windows-amd64.exe" ./cmd/blur
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -o "$(PORTABLE_PACKAGE_DIR)/blur-linux-amd64" ./cmd/blur
	go run ./cmd/blur-package -root . -binary "$(PORTABLE_PACKAGE_DIR)/Blur-windows-amd64.exe" -mcp-binary "$(PORTABLE_PACKAGE_DIR)/blur-mcp-windows-amd64.exe" -output "$(PORTABLE_PACKAGE_DIR)/blur-windows-amd64.zip" -os windows -arch amd64
	go run ./cmd/blur-package -root . -binary "$(PORTABLE_PACKAGE_DIR)/blur-linux-amd64" -output "$(PORTABLE_PACKAGE_DIR)/blur-linux-amd64.tar.gz" -os linux -arch amd64

# The checked-in Xcode project builds an Apple silicon, unsigned Safari .appex.
# Signing, App Group provisioning, embedding, and Safari runtime tests remain
# separate release gates; an unsigned extension is not installable.
XCODE_DEVELOPER_DIR ?= $(shell xcode-select -p 2>/dev/null)
ifeq ($(notdir $(XCODE_DEVELOPER_DIR)),CommandLineTools)
ifneq ($(wildcard /Applications/Xcode.app/Contents/Developer),)
XCODE_DEVELOPER_DIR := /Applications/Xcode.app/Contents/Developer
endif
endif
SAFARI_EXTENSION_DERIVED_DATA ?= dist/safari-extension-derived
SAFARI_EXTENSION_PRODUCT = $(SAFARI_EXTENSION_DERIVED_DATA)/Build/Products/Release/BlurSafariCapture.appex
SAFARI_EXTENSION_INFO = $(SAFARI_EXTENSION_PRODUCT)/Contents/Info.plist
MACOS_SANDBOX_DEV_PATH ?=
MACOS_SANDBOX_ENTITLEMENTS ?= platform/macos/Blur.sandbox.entitlements

safari-extension-build:
	DEVELOPER_DIR="$(XCODE_DEVELOPER_DIR)" xcodebuild -quiet -project platform/macos/BlurSafariCapture.xcodeproj -scheme BlurSafariCapture -configuration Release -sdk macosx -destination 'generic/platform=macOS' -derivedDataPath "$(SAFARI_EXTENSION_DERIVED_DATA)" ARCHS=arm64 ONLY_ACTIVE_ARCH=NO CODE_SIGNING_ALLOWED=NO BLUR_APP_GROUP_ID="$(BLUR_APP_GROUP_ID)" BLUR_KEYCHAIN_ACCESS_GROUP="$(BLUR_KEYCHAIN_ACCESS_GROUP)" build
	test -s "$(SAFARI_EXTENSION_PRODUCT)/Contents/MacOS/BlurSafariCapture"
	test -f "$(SAFARI_EXTENSION_PRODUCT)/Contents/Resources/Injected.js"
	test "$$(/usr/libexec/PlistBuddy -c 'Print :NSExtension:NSExtensionPointIdentifier' "$(SAFARI_EXTENSION_INFO)")" = "com.apple.Safari.extension"
	test "$$(/usr/libexec/PlistBuddy -c 'Print :NSExtension:NSExtensionPrincipalClass' "$(SAFARI_EXTENSION_INFO)")" = "BlurSafariCapture.SafariExtensionHandler"
	plutil -lint "$(SAFARI_EXTENSION_INFO)"

safari-extension-test:
	$(MAKE) safari-extension-build BLUR_APP_GROUP_ID=group.com.hazcod.blur.test BLUR_KEYCHAIN_ACCESS_GROUP=TESTTEAM123.com.hazcod.blur.safaricapture
	test "$$(/usr/libexec/PlistBuddy -c 'Print :com.apple.security.application-groups:0' platform/macos/SafariExtension/Extension.entitlements)" = '$$(BLUR_APP_GROUP_ID)'
	test "$$(/usr/libexec/PlistBuddy -c 'Print :keychain-access-groups:0' platform/macos/SafariExtension/Extension.entitlements)" = '$$(BLUR_KEYCHAIN_ACCESS_GROUP)'
	test "$$(/usr/libexec/PlistBuddy -c 'Print :com.apple.security.app-sandbox' platform/macos/SafariExtension/Extension.entitlements)" = "true"
	test "$$(/usr/libexec/PlistBuddy -c 'Print :com.apple.security.application-groups:0' platform/macos/Blur.entitlements)" = '$$(BLUR_APP_GROUP_ID)'
	test "$$(/usr/libexec/PlistBuddy -c 'Print :keychain-access-groups:0' platform/macos/Blur.entitlements)" = '$$(BLUR_KEYCHAIN_ACCESS_GROUP)'
	test "$$(/usr/libexec/PlistBuddy -c 'Print :BlurAppGroupIdentifier' "$(SAFARI_EXTENSION_INFO)")" = "group.com.hazcod.blur.test"
	test "$$(/usr/libexec/PlistBuddy -c 'Print :BlurKeychainAccessGroup' "$(SAFARI_EXTENSION_INFO)")" = "TESTTEAM123.com.hazcod.blur.safaricapture"
	test -z "$(MACOS_SANDBOX_DEV_PATH)" || { echo "MACOS_SANDBOX_DEV_PATH must be empty by default" >&2; exit 1; }
	! grep -q 'absolute-path' "$(MACOS_SANDBOX_ENTITLEMENTS)" || { echo "$(MACOS_SANDBOX_ENTITLEMENTS) must not carry the dev absolute-path exception" >&2; exit 1; }
	node --test platform/macos/SafariExtensionTests/injected.test.cjs
	DEVELOPER_DIR="$(XCODE_DEVELOPER_DIR)" swiftc -typecheck -target arm64-apple-macosx13.0 platform/macos/SafariExtension/ExtensionHandler.swift
	plutil -lint platform/macos/SafariExtension/Info.plist platform/macos/Blur.entitlements platform/macos/SafariExtension/Extension.entitlements

# The active macOS app lives in this worktree until its implementation is
# integrated. Keep every app-bundle entry point on that source: the older root
# checkout cannot open its encrypted memory. Recursive make preserves command-line
# options such as MACOS_CODESIGN_IDENTITY and propagates build failures.
macos-app: macos-app-stable

macos-app-stable macos-app-sandbox-check macos-app-safari-unsigned macos-app-safari-signed run-macos:
	@test -f "$(CURDIR)/.worktrees/durable-memory-core/Makefile" || { echo "The active macOS worktree is unavailable; refusing to replace Blur with older source." >&2; exit 2; }
	$(MAKE) -C "$(CURDIR)/.worktrees/durable-memory-core" $@
