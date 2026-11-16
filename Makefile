.PHONY: version release-snapshot test race vet build vulncheck cross-build physical-cross-build cross-test-compile no-cgo-audit portable-packages safari-extension-test safari-extension-build macos-app-stable macos-app macos-app-preflight macos-app-identity-check macos-app-sandbox-check macos-app-safari-unsigned macos-app-safari-signed macos-app-compile macos-app-arm64 macos-app-amd64 macos-app-native macos-app-build-tools macos-build-cache-test run-macos

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
	for target in darwin/arm64 darwin/amd64 windows/amd64 linux/amd64 linux/arm64; do \
		GOOS=$${target%/*}; GOARCH=$${target#*/}; \
		for package in ./cmd/blur ./cmd/blur-core ./cmd/blurctl ./cmd/blur-sign; do \
			echo "Building $$package for $$GOOS/$$GOARCH with cgo disabled"; \
			CGO_ENABLED=0 GOOS=$$GOOS GOARCH=$$GOARCH go build -o /dev/null $$package; \
		done; \
	done
	@echo "Compiling the Linux native UI for arm64 with cgo disabled"; \
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go test -c -o /dev/null github.com/golang-gui/goui/ui

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
	for target in darwin/arm64 windows/amd64 linux/amd64 linux/arm64; do \
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
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -buildvcs=false -o "$(PORTABLE_PACKAGE_DIR)/blur-linux-arm64" ./cmd/blur
	go run ./cmd/blur-package -root . -binary "$(PORTABLE_PACKAGE_DIR)/Blur-windows-amd64.exe" -mcp-binary "$(PORTABLE_PACKAGE_DIR)/blur-mcp-windows-amd64.exe" -output "$(PORTABLE_PACKAGE_DIR)/blur-windows-amd64.zip" -os windows -arch amd64
	go run ./cmd/blur-package -root . -binary "$(PORTABLE_PACKAGE_DIR)/blur-linux-amd64" -output "$(PORTABLE_PACKAGE_DIR)/blur-linux-amd64.tar.gz" -os linux -arch amd64
	go run ./cmd/blur-package -root . -binary "$(PORTABLE_PACKAGE_DIR)/blur-linux-arm64" -output "$(PORTABLE_PACKAGE_DIR)/blur-linux-arm64.tar.gz" -os linux -arch arm64

# The checked-in Xcode project builds a universal, unsigned Safari .appex.
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

safari-extension-build:
	DEVELOPER_DIR="$(XCODE_DEVELOPER_DIR)" xcodebuild -quiet -project platform/macos/BlurSafariCapture.xcodeproj -scheme BlurSafariCapture -configuration Release -sdk macosx -destination 'generic/platform=macOS' -derivedDataPath "$(SAFARI_EXTENSION_DERIVED_DATA)" CODE_SIGNING_ALLOWED=NO BLUR_APP_GROUP_ID="$(BLUR_APP_GROUP_ID)" BLUR_KEYCHAIN_ACCESS_GROUP="$(BLUR_KEYCHAIN_ACCESS_GROUP)" build
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

# Build the native macOS bundle used for Finder/Dock identity and permissions.
# The package command consumes the exact binary produced above; it never rebuilds.
MACOS_APP ?= $(HOME)/Downloads/Blur.app
MACOS_APP_STAGING = $(dir $(MACOS_APP)).$(basename $(notdir $(MACOS_APP))).staging.app
MACOS_SAFARI_APP ?= dist/darwin-arm64/Blur-Safari-unsigned.app
MACOS_SAFARI_SIGNED_APP ?= dist/darwin-arm64/Blur-Safari.app
MACOS_SAFARI_STAGING_APP = $(abspath $(dir $(MACOS_SAFARI_SIGNED_APP)))/.$(basename $(notdir $(MACOS_SAFARI_SIGNED_APP))).staging.app
MACOS_BINARY ?= $(dir $(MACOS_APP)).blur-build
MACOS_BINARY_ARM64 ?= $(MACOS_BINARY).arm64
MACOS_BINARY_AMD64 ?= $(MACOS_BINARY).amd64
MACOS_BUNDLE_ID ?= com.hazcod.blur
MACOS_MINIMUM_SYSTEM_VERSION ?= 13.0
MACOS_CODESIGN_IDENTITY ?= $(shell go run ./cmd/macos-signing-identity -app "$(MACOS_APP)" 2>/dev/null)
# Resolve once rather than rerunning go run/security for every flag expansion.
# Ordinary non-macOS make targets never inspect the user's signing identities.
ifneq ($(filter macos-app macos-app-stable macos-app-preflight macos-app-safari-unsigned macos-app-safari-signed run-macos,$(MAKECMDGOALS)),)
MACOS_CODESIGN_IDENTITY := $(MACOS_CODESIGN_IDENTITY)
endif
MACOS_BUILD_TOOLS_DIR ?= dist/build-tools
MACOS_GOUI = $(MACOS_BUILD_TOOLS_DIR)/goui
MACOS_BUILD_JOBS ?= 3
MACOS_NATIVE_JOBS ?= 2
MACOS_HOST_PROVISIONING_PROFILE ?=
MACOS_EXTENSION_PROVISIONING_PROFILE ?=
BLUR_APP_GROUP_ID ?=
BLUR_KEYCHAIN_ACCESS_GROUP ?=
MACOS_LDFLAGS = -X github.com/hazcod/blur/internal/platform/darwin.SafariAppGroupID=$(BLUR_APP_GROUP_ID) -X github.com/hazcod/blur/internal/platform/darwin.SafariKeychainAccessGroup=$(BLUR_KEYCHAIN_ACCESS_GROUP) $(VERSION_LDFLAGS)
MACOS_FOUNDATION_HELPER = $(MACOS_BINARY).foundation-models
MACOS_FOUNDATION_HELPER_ENTITLEMENTS = platform/macos/FoundationModels/Helper.sandbox.entitlements
MACOS_MEETING_LIBRARY = $(MACOS_BINARY).meeting-audio.dylib
MACOS_SCREEN_TEXT_LIBRARY = $(MACOS_BINARY).screen-text.dylib
MACOS_RUNTIME_ENTITLEMENTS = platform/macos/Blur.runtime.entitlements
# Reuse the existing bundle's unambiguous signer or a sole local identity. The
# ad-hoc fallback keeps fresh OSS/dev environments buildable. Existing apps
# require a certificate identity; certificate-signed apps must keep their exact
# certificate and designated requirement.
# Keep Hardened Runtime and library validation on in local builds too. Local
# builds skip Apple's timestamp server; notarized releases request a timestamp.
# Hardened Runtime is independent of the App Sandbox capture restriction.
MACOS_CODESIGN_EXTRA_FLAGS ?= --options runtime --timestamp=none
MACOS_CODESIGN_FLAGS = --force $(if $(strip $(MACOS_CODESIGN_IDENTITY)),--sign "$(MACOS_CODESIGN_IDENTITY)",--sign -)
# Direct-download builds are unsandboxed so macOS Accessibility can read other
# apps. MACOS_SANDBOX=1 remains an explicit compatibility-build opt-in.
# When the Safari App Group / Keychain group are configured
# they are merged into a generated plist next to the build binary (never inside
# the bundle); otherwise the checked-in sandbox entitlements are signed as-is.
MACOS_SANDBOX ?= 0
# Developer hook only: a non-empty absolute directory is added to the merged
# entitlements as a read-write temporary exception so a dev build can write UI
# snapshots there (the container Data directory is unreadable from a shell).
# Empty by default; never set it for builds you use or distribute.
MACOS_SANDBOX_DEV_PATH ?=
MACOS_SANDBOX_ENTITLEMENTS ?= platform/macos/Blur.sandbox.entitlements
MACOS_SANDBOX_ENTITLEMENTS_MERGED = $(MACOS_BINARY).entitlements
MACOS_SANDBOX_ENTITLEMENTS_EFFECTIVE = $(if $(strip $(BLUR_APP_GROUP_ID)$(BLUR_KEYCHAIN_ACCESS_GROUP)$(MACOS_SANDBOX_DEV_PATH)),$(MACOS_SANDBOX_ENTITLEMENTS_MERGED),$(MACOS_SANDBOX_ENTITLEMENTS))
MACOS_ENTITLEMENT_FLAGS = --entitlements "$(if $(filter 1,$(MACOS_SANDBOX)),$(MACOS_SANDBOX_ENTITLEMENTS_EFFECTIVE),$(MACOS_RUNTIME_ENTITLEMENTS))"

# Local builds always replace the same app, preserving its permission identity.
# Release/Safari assembly can still choose its own output via macos-app.
macos-app-stable:
	$(MAKE) macos-app MACOS_APP="$(HOME)/Downloads/Blur.app" MACOS_BUNDLE_ID=com.hazcod.blur MACOS_SANDBOX=0 MACOS_SANDBOX_DEV_PATH= MACOS_CODESIGN_IDENTITY="$(MACOS_CODESIGN_IDENTITY)"

macos-app-preflight:
	@test "$(suffix $(MACOS_APP))" = .app || { echo "MACOS_APP must end in .app" >&2; exit 2; }
	@set -eu; if test -e "$(MACOS_APP)"; then \
		identifier=$$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$(MACOS_APP)/Contents/Info.plist"); \
		test "$$identifier" = "$(MACOS_BUNDLE_ID)" || { echo "Refusing to change the existing app's bundle identifier ($$identifier)." >&2; exit 2; }; \
		codesign -dv --verbose=4 "$(MACOS_APP)" >/dev/null 2>&1 || { echo "Cannot inspect the existing app's signature; leaving it in place." >&2; exit 2; }; \
		test -n "$(MACOS_CODESIGN_IDENTITY)" && test "$(MACOS_CODESIGN_IDENTITY)" != - || { echo "Refusing to replace an existing app with ad-hoc signing. Restore its signing identity." >&2; exit 2; }; \
	fi

# Run before replacement. The leaf certificate must match exactly, even when
# two certificates from the same team would satisfy the same requirement.
macos-app-identity-check:
	@set -eu; if test -e "$(MACOS_APP)"; then \
		signature=$$(codesign -dv --verbose=4 "$(MACOS_APP)" 2>&1) || { echo "Cannot inspect the existing app's signature; leaving it in place." >&2; exit 2; }; \
		if printf '%s\n' "$$signature" | grep -q '^Authority='; then \
			current=$$(codesign -d -r- "$(MACOS_APP)" 2>&1 | sed -n 's/^designated => //p'); \
			staged=$$(codesign -d -r- "$(MACOS_APP_STAGING)" 2>&1 | sed -n 's/^designated => //p'); \
			test -n "$$current" && test "$$current" = "$$staged" || { echo "Signing requirement changed; leaving the existing app in place." >&2; exit 2; }; \
			certificates=$$(mktemp -d "$${TMPDIR:-/tmp}/blur-signing.XXXXXX"); \
			trap 'rm -rf "$$certificates"' EXIT HUP INT TERM; \
			codesign -d --extract-certificates="$$certificates/current-" "$(MACOS_APP)" 2>/dev/null; \
			codesign -d --extract-certificates="$$certificates/staged-" "$(MACOS_APP_STAGING)" 2>/dev/null; \
			cmp -s "$$certificates/current-0" "$$certificates/staged-0" || { echo "Signing certificate changed; leaving the existing app in place." >&2; exit 2; }; \
		fi; \
	fi

macos-app-build-tools:
	mkdir -p "$(MACOS_BUILD_TOOLS_DIR)"
	CGO_ENABLED=0 GOOS=darwin GOARCH=$$(go env GOHOSTARCH) go build -mod=mod -o "$(MACOS_GOUI)" github.com/golang-gui/goui/cmd/goui

macos-app-arm64: macos-app-build-tools
	CGO_ENABLED=0 "$(MACOS_GOUI)" build -target darwin/arm64 -ldflags '$(MACOS_LDFLAGS)' -o "$(MACOS_BINARY_ARM64)"

macos-app-amd64:
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags '$(MACOS_LDFLAGS)' -o "$(MACOS_BINARY_AMD64)" ./cmd/blur

macos-app-native:
	python3 platform/macos/build_native.py --foundation-out "$(MACOS_FOUNDATION_HELPER)" --meeting-out "$(MACOS_MEETING_LIBRARY)" --screen-out "$(MACOS_SCREEN_TEXT_LIBRARY)" --jobs "$(MACOS_NATIVE_JOBS)"

macos-app-compile: macos-app-arm64 macos-app-amd64 macos-app-native

macos-build-cache-test:
	python3 -m unittest discover -s platform/macos -p test_build_native.py

macos-app: macos-app-preflight
	@if test -z "$(MACOS_CODESIGN_IDENTITY)"; then echo "Using ad-hoc signing; macOS may require Accessibility approval again after rebuilds. Set MACOS_CODESIGN_IDENTITY for a stable local identity."; fi
	$(MAKE) --no-print-directory -j "$(MACOS_BUILD_JOBS)" macos-app-compile MACOS_CODESIGN_IDENTITY="$(MACOS_CODESIGN_IDENTITY)" BUILD_DATE="$(BUILD_DATE)" VERSION="$(VERSION)" COMMIT="$(COMMIT)"
	lipo -create "$(MACOS_BINARY_ARM64)" "$(MACOS_BINARY_AMD64)" -output "$(MACOS_BINARY)"
	lipo "$(MACOS_BINARY)" -verify_arch arm64
	lipo "$(MACOS_BINARY)" -verify_arch x86_64
	rm -rf "$(MACOS_APP_STAGING)"
	CGO_ENABLED=0 "$(MACOS_GOUI)" package -target darwin/arm64 -format app -binary "$(MACOS_BINARY_ARM64)" -o "$(MACOS_APP_STAGING)"
	install -m 755 "$(MACOS_BINARY)" "$(MACOS_APP_STAGING)/Contents/MacOS/blur"
	mkdir -p "$(MACOS_APP_STAGING)/Contents/Helpers"
	install -m 755 "$(MACOS_FOUNDATION_HELPER)" "$(MACOS_APP_STAGING)/Contents/Helpers/blur-foundation-models"
	mkdir -p "$(MACOS_APP_STAGING)/Contents/Frameworks"
	install -m 755 "$(MACOS_MEETING_LIBRARY)" "$(MACOS_APP_STAGING)/Contents/Frameworks/libBlurMeetingAudio.dylib"
	install -m 755 "$(MACOS_SCREEN_TEXT_LIBRARY)" "$(MACOS_APP_STAGING)/Contents/Frameworks/libBlurScreenText.dylib"
	/usr/libexec/PlistBuddy -c "Set :CFBundleIdentifier $(MACOS_BUNDLE_ID)" "$(MACOS_APP_STAGING)/Contents/Info.plist"
	/usr/libexec/PlistBuddy -c "Add :LSMinimumSystemVersion string $(MACOS_MINIMUM_SYSTEM_VERSION)" "$(MACOS_APP_STAGING)/Contents/Info.plist"
	/usr/libexec/PlistBuddy -c "Add :NSMicrophoneUsageDescription string Blur transcribes your microphone when you open Voice notes and select microphone audio." "$(MACOS_APP_STAGING)/Contents/Info.plist"
	/usr/libexec/PlistBuddy -c "Add :NSAudioCaptureUsageDescription string Blur transcribes system audio when you open Voice notes and select system audio." "$(MACOS_APP_STAGING)/Contents/Info.plist"
	plutil -lint "$(MACOS_APP_STAGING)/Contents/Info.plist"
	@if test "$(MACOS_SANDBOX)" = 1; then \
		echo "Signing this explicit compatibility build with App Sandbox; data lives in ~/Library/Containers/$(MACOS_BUNDLE_ID)."; \
		if test -n "$(strip $(BLUR_APP_GROUP_ID)$(BLUR_KEYCHAIN_ACCESS_GROUP)$(MACOS_SANDBOX_DEV_PATH))"; then \
			set -e; rm -f "$(MACOS_SANDBOX_ENTITLEMENTS_MERGED)"; cp "$(MACOS_SANDBOX_ENTITLEMENTS)" "$(MACOS_SANDBOX_ENTITLEMENTS_MERGED)"; \
			if test -n "$(BLUR_APP_GROUP_ID)"; then \
				/usr/libexec/PlistBuddy -c "Add :com.apple.security.application-groups array" -c "Add :com.apple.security.application-groups:0 string $(BLUR_APP_GROUP_ID)" "$(MACOS_SANDBOX_ENTITLEMENTS_MERGED)"; \
			fi; \
			if test -n "$(BLUR_KEYCHAIN_ACCESS_GROUP)"; then \
				/usr/libexec/PlistBuddy -c "Add :keychain-access-groups array" -c "Add :keychain-access-groups:0 string $(BLUR_KEYCHAIN_ACCESS_GROUP)" "$(MACOS_SANDBOX_ENTITLEMENTS_MERGED)"; \
			fi; \
			if test -n "$(MACOS_SANDBOX_DEV_PATH)"; then \
				echo "WARNING: dev-only sandbox exception: read-write $(MACOS_SANDBOX_DEV_PATH)/ (do not use or distribute this build)"; \
				/usr/libexec/PlistBuddy -c "Add :com.apple.security.temporary-exception.files.absolute-path.read-write array" -c "Add :com.apple.security.temporary-exception.files.absolute-path.read-write:0 string $(patsubst %/,%,$(MACOS_SANDBOX_DEV_PATH))/" "$(MACOS_SANDBOX_ENTITLEMENTS_MERGED)"; \
			fi; \
			plutil -lint "$(MACOS_SANDBOX_ENTITLEMENTS_MERGED)"; \
		else plutil -lint "$(MACOS_SANDBOX_ENTITLEMENTS)"; fi; \
	fi
	codesign $(MACOS_CODESIGN_FLAGS) $(if $(filter 1,$(MACOS_SANDBOX)),--entitlements "$(MACOS_FOUNDATION_HELPER_ENTITLEMENTS)") --identifier "$(MACOS_BUNDLE_ID).foundation-models" $(MACOS_CODESIGN_EXTRA_FLAGS) "$(MACOS_APP_STAGING)/Contents/Helpers/blur-foundation-models"
	codesign --verify --strict "$(MACOS_APP_STAGING)/Contents/Helpers/blur-foundation-models"
	codesign $(MACOS_CODESIGN_FLAGS) --identifier "$(MACOS_BUNDLE_ID).meeting-audio" $(MACOS_CODESIGN_EXTRA_FLAGS) "$(MACOS_APP_STAGING)/Contents/Frameworks/libBlurMeetingAudio.dylib"
	codesign --verify --strict "$(MACOS_APP_STAGING)/Contents/Frameworks/libBlurMeetingAudio.dylib"
	codesign $(MACOS_CODESIGN_FLAGS) --identifier "$(MACOS_BUNDLE_ID).screen-text" $(MACOS_CODESIGN_EXTRA_FLAGS) "$(MACOS_APP_STAGING)/Contents/Frameworks/libBlurScreenText.dylib"
	codesign --verify --strict "$(MACOS_APP_STAGING)/Contents/Frameworks/libBlurScreenText.dylib"
	codesign $(MACOS_CODESIGN_FLAGS) $(MACOS_ENTITLEMENT_FLAGS) --identifier "$(MACOS_BUNDLE_ID)" $(MACOS_CODESIGN_EXTRA_FLAGS) "$(MACOS_APP_STAGING)"
	codesign --verify --deep --strict "$(MACOS_APP_STAGING)"
	$(MAKE) macos-app-identity-check MACOS_CODESIGN_IDENTITY="$(MACOS_CODESIGN_IDENTITY)"
	rm -rf "$(MACOS_APP)"
	mv "$(MACOS_APP_STAGING)" "$(MACOS_APP)"
	codesign --verify --deep --strict "$(MACOS_APP)"

# Fails unless the built bundle is signed with the App Sandbox entitlement.
macos-app-sandbox-check:
	@codesign -d --entitlements :- "$(MACOS_APP)" 2>/dev/null | grep -q '<key>com.apple.security.app-sandbox</key><true/>' || { echo "$(MACOS_APP) is not signed with com.apple.security.app-sandbox" >&2; exit 1; }
	@echo "$(MACOS_APP) is App Sandbox signed"

# Embeds the compiled extension for bundle-layout inspection only. It is not
# signed or installable in Safari; release packaging must sign both bundles
# with matching, registered App Group and Keychain entitlements.
macos-app-safari-unsigned:
	@test -n "$(BLUR_APP_GROUP_ID)" -a -n "$(BLUR_KEYCHAIN_ACCESS_GROUP)" || { echo "set BLUR_APP_GROUP_ID and BLUR_KEYCHAIN_ACCESS_GROUP for Safari bundle assembly" >&2; exit 2; }
	$(MAKE) macos-app MACOS_APP="$(MACOS_SAFARI_APP)" MACOS_BINARY=dist/darwin-arm64/blur-safari-unsigned
	$(MAKE) safari-extension-build
	mkdir -p "$(MACOS_SAFARI_APP)/Contents/PlugIns"
	ditto "$(SAFARI_EXTENSION_PRODUCT)" "$(MACOS_SAFARI_APP)/Contents/PlugIns/BlurSafariCapture.appex"
	@echo "Assembled unsigned bundle at $(MACOS_SAFARI_APP); it must be signed with matching entitlements before Safari can load it."

# Build to a fresh staging path, validate both provisioning profiles, sign the
# extension before the containing app, verify both signatures, then publish by
# same-volume rename. Nothing overwrites an existing artifact.
macos-app-safari-signed:
	@test "$$(uname -s)" = Darwin || { echo "Safari signing requires macOS" >&2; exit 2; }
	@test "$(suffix $(MACOS_SAFARI_SIGNED_APP))" = .app || { echo "MACOS_SAFARI_SIGNED_APP must end in .app" >&2; exit 2; }
	@test -n "$(MACOS_CODESIGN_IDENTITY)" -a -n "$(BLUR_APP_GROUP_ID)" -a -n "$(BLUR_KEYCHAIN_ACCESS_GROUP)" || { echo "set MACOS_CODESIGN_IDENTITY, BLUR_APP_GROUP_ID, and BLUR_KEYCHAIN_ACCESS_GROUP" >&2; exit 2; }
	@test -n "$(MACOS_HOST_PROVISIONING_PROFILE)" -a -n "$(MACOS_EXTENSION_PROVISIONING_PROFILE)" || { echo "set both macOS provisioning profile paths; each must authorize its bundle ID, team certificate, and configured Keychain access group" >&2; exit 2; }
	@test ! -e "$(MACOS_SAFARI_SIGNED_APP)" -a ! -e "$(MACOS_SAFARI_STAGING_APP)" || { echo "refusing to overwrite an existing signed or staging bundle" >&2; exit 2; }
	$(MAKE) macos-app-safari-unsigned MACOS_SAFARI_APP="$(MACOS_SAFARI_STAGING_APP)" MACOS_BINARY="$(MACOS_SAFARI_STAGING_APP)-binary" BLUR_APP_GROUP_ID="$(BLUR_APP_GROUP_ID)" BLUR_KEYCHAIN_ACCESS_GROUP="$(BLUR_KEYCHAIN_ACCESS_GROUP)"
	CGO_ENABLED=0 go run ./cmd/blur-sign -app "$(MACOS_SAFARI_STAGING_APP)" -identity "$(MACOS_CODESIGN_IDENTITY)" -app-group "$(BLUR_APP_GROUP_ID)" -keychain-access-group "$(BLUR_KEYCHAIN_ACCESS_GROUP)" -host-profile "$(MACOS_HOST_PROVISIONING_PROFILE)" -extension-profile "$(MACOS_EXTENSION_PROVISIONING_PROFILE)"
	mv "$(MACOS_SAFARI_STAGING_APP)" "$(MACOS_SAFARI_SIGNED_APP)"

run-macos: macos-app
	open -a "$(MACOS_APP)"
