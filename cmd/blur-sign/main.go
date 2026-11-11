// Command blur-sign signs a staged Blur macOS app bundle and its Safari
// extension. It never builds, launches, uploads, or notarizes the app.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"

	"github.com/hazcod/blur/internal/macospackage"
)

func main() {
	var config macospackage.Config
	flag.StringVar(&config.AppPath, "app", "", "staged Blur .app bundle to sign")
	flag.StringVar(&config.Identity, "identity", "", "explicit codesign identity name or SHA-1")
	flag.StringVar(&config.AppGroupID, "app-group", "", "registered shared App Group identifier")
	flag.StringVar(&config.KeychainAccessGroup, "keychain-access-group", "", "shared Keychain access group")
	flag.StringVar(&config.HostProfile, "host-profile", "", "host app provisioning profile")
	flag.StringVar(&config.ExtensionProfile, "extension-profile", "", "Safari extension provisioning profile")
	flag.Parse()
	if flag.NArg() != 0 {
		fail("unexpected positional arguments")
	}
	if runtime.GOOS != "darwin" {
		fail("signing Safari app bundles is supported only on macOS")
	}
	if err := macospackage.Sign(context.Background(), config, nil); err != nil {
		if errors.Is(err, macospackage.ErrInvalidConfig) {
			fail("provide a staged app, signing identity, matching shared group identifiers, and both provisioning profiles")
		}
		if errors.Is(err, macospackage.ErrInvalidBundle) {
			fail("the input is not a valid Blur app bundle with its Safari extension")
		}
		if errors.Is(err, macospackage.ErrInvalidProfile) {
			fail("a provisioning profile is invalid or does not authorize this app's bundle ID and shared entitlements")
		}
		fail(err.Error())
	}
	fmt.Fprintln(os.Stdout, "Blur app and Safari extension signatures verified.")
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, "blur-sign:", message)
	os.Exit(2)
}
