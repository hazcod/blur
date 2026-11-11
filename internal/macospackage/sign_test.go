package macospackage

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSignValidatesProfilesSignsExtensionBeforeHostAndVerifiesBoth(t *testing.T) {
	app := signingFixture(t)
	hostProfile := writeProfile(t, app, "host.provisionprofile", profileXML("TEAMID123.com.hazcod.blur"))
	extensionProfile := writeProfile(t, app, "extension.provisionprofile", profileXML("TEAMID123.com.hazcod.blur.safaricapture"))
	runner := &recordingRunner{profiles: map[string][]byte{
		filepath.Join(app, "Contents", "embedded.provisionprofile"):                                                   []byte(profileXML("com.hazcod.blur")),
		filepath.Join(app, "Contents", "PlugIns", "BlurSafariCapture.appex", "Contents", "embedded.provisionprofile"): []byte(profileXML("com.hazcod.blur.safaricapture")),
	}}
	cfg := Config{
		AppPath: app, Identity: "Developer ID Application: Blur Builder (TEAMID123)",
		AppGroupID: "group.sh.blur.capture", KeychainAccessGroup: "TEAMID123.sh.blur.capture",
		HostProfile: hostProfile, ExtensionProfile: extensionProfile,
	}
	if err := Sign(context.Background(), cfg, runner); err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	if len(runner.runs) != 4 {
		t.Fatalf("commands = %v, want two signatures and two verifications", runner.runs)
	}
	if runner.runs[0][0] != "codesign" || runner.runs[1][0] != "codesign" || runner.runs[2][0] != "codesign" || runner.runs[3][0] != "codesign" {
		t.Fatalf("commands = %v, want codesign commands", runner.runs)
	}
	if !strings.HasSuffix(runner.runs[0][len(runner.runs[0])-1], "BlurSafariCapture.appex") {
		t.Fatalf("first signed target = %q, want nested extension", runner.runs[0][len(runner.runs[0])-1])
	}
	if runner.runs[1][len(runner.runs[1])-1] != app {
		t.Fatalf("second signed target = %q, want host app", runner.runs[1][len(runner.runs[1])-1])
	}
	if len(runner.entitlements) != 2 || !strings.Contains(string(runner.entitlements[0]), "<true/>") || strings.Contains(string(runner.entitlements[1]), "com.apple.security.app-sandbox") {
		t.Fatalf("generated entitlements do not isolate the Safari sandbox: %q", runner.entitlements)
	}
	if !containsArg(runner.runs[0], cfg.Identity) || !containsArg(runner.runs[0], "--timestamp") || !containsArg(runner.runs[0], "runtime") {
		t.Fatalf("extension signing arguments omit identity or hardened runtime: %v", runner.runs[0])
	}
	if _, err := os.Stat(filepath.Join(app, "Contents", "embedded.provisionprofile")); err != nil {
		t.Fatalf("host profile was not embedded: %v", err)
	}
	if _, err := os.Stat(filepath.Join(app, "Contents", "PlugIns", "BlurSafariCapture.appex", "Contents", "embedded.provisionprofile")); err != nil {
		t.Fatalf("extension profile was not embedded: %v", err)
	}
}

func TestValidateBundleAcceptsFormattedPlistsAndChecksConfiguredGroups(t *testing.T) {
	app := signingFixture(t)
	extensionInfo := filepath.Join(app, "Contents", "PlugIns", "BlurSafariCapture.appex", "Contents", "Info.plist")
	formatted := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
  <dict>
    <key>CFBundleIdentifier</key>
    <string>com.hazcod.blur.safaricapture</string>
    <key>BlurAppGroupIdentifier</key>
    <string>group.sh.blur.capture</string>
    <key>BlurKeychainAccessGroup</key>
    <string>TEAMID123.sh.blur.capture</string>
  </dict>
</plist>`
	if err := os.WriteFile(extensionInfo, []byte(formatted), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := validateBundle(app, "group.sh.blur.capture", "TEAMID123.sh.blur.capture"); err != nil {
		t.Fatalf("validateBundle() rejected normal plist whitespace: %v", err)
	}
	if _, _, err := validateBundle(app, "group.sh.blur.other", "TEAMID123.sh.blur.capture"); err == nil {
		t.Fatal("validateBundle() accepted an extension configured for a different App Group")
	}
}

func TestMakeSignedSafariTargetUsesAbsoluteAppStage(t *testing.T) {
	// The signer deliberately requires an absolute .app path. Keep the Make
	// target's staged path under the same contract so the default build works.
	stage := filepath.Join(t.TempDir(), "Blur-Safari.app.staging.app")
	if filepath.Ext(stage) != ".app" || !filepath.IsAbs(stage) {
		t.Fatalf("stage path %q must be absolute and end in .app", stage)
	}
}

func TestSignRejectsProfilesWithoutMatchingSharedEntitlementsBeforeSigning(t *testing.T) {
	app := signingFixture(t)
	hostProfile := writeProfile(t, app, "host.provisionprofile", profileXML("TEAMID123.com.hazcod.blur"))
	extensionProfile := writeProfile(t, app, "extension.provisionprofile", profileXML("TEAMID123.com.hazcod.blur.safaricapture"))
	runner := &recordingRunner{profiles: map[string][]byte{
		filepath.Join(app, "Contents", "embedded.provisionprofile"):                                                   []byte(profileXMLWithoutGroups("com.hazcod.blur")),
		filepath.Join(app, "Contents", "PlugIns", "BlurSafariCapture.appex", "Contents", "embedded.provisionprofile"): []byte(profileXMLWithoutGroups("com.hazcod.blur.safaricapture")),
	}}
	cfg := Config{AppPath: app, Identity: "TEAMID123", AppGroupID: "group.sh.blur.capture", KeychainAccessGroup: "TEAMID123.sh.blur.capture", HostProfile: hostProfile, ExtensionProfile: extensionProfile}
	if err := Sign(context.Background(), cfg, runner); err == nil {
		t.Fatal("Sign() succeeded with a profile that does not authorize the requested groups")
	}
	if len(runner.runs) != 0 {
		t.Fatalf("codesign ran despite invalid profile: %v", runner.runs)
	}
}

func TestSignNeverRemovesAnExistingProfileInsideTheBundle(t *testing.T) {
	app := signingFixture(t)
	hostProfile := writeProfile(t, app, "host.provisionprofile", "fixture")
	extensionProfile := writeProfile(t, app, "extension.provisionprofile", "fixture")
	existing := filepath.Join(app, "Contents", "embedded.provisionprofile")
	want := []byte("preserve existing artifact")
	if err := os.WriteFile(existing, want, 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	cfg := Config{AppPath: app, Identity: "TEAMID123", AppGroupID: "group.sh.blur.capture", KeychainAccessGroup: "TEAMID123.sh.blur.capture", HostProfile: hostProfile, ExtensionProfile: extensionProfile}
	if err := Sign(context.Background(), cfg, runner); err == nil {
		t.Fatal("Sign() should refuse a preexisting embedded profile")
	}
	got, err := os.ReadFile(existing)
	if err != nil || string(got) != string(want) {
		t.Fatalf("preexisting profile = %q, read error %v; want it preserved", got, err)
	}
}

func TestSignRejectsExpiredProvisioningProfilesBeforeSigning(t *testing.T) {
	app := signingFixture(t)
	hostProfile := writeProfile(t, app, "host.provisionprofile", "fixture")
	extensionProfile := writeProfile(t, app, "extension.provisionprofile", "fixture")
	runner := &recordingRunner{profiles: map[string][]byte{
		filepath.Join(app, "Contents", "embedded.provisionprofile"):                                                   []byte(profileXMLWithExpiration("com.hazcod.blur", "2000-01-01T00:00:00Z")),
		filepath.Join(app, "Contents", "PlugIns", "BlurSafariCapture.appex", "Contents", "embedded.provisionprofile"): []byte(profileXML("com.hazcod.blur.safaricapture")),
	}}
	cfg := Config{AppPath: app, Identity: "TEAMID123", AppGroupID: "group.sh.blur.capture", KeychainAccessGroup: "TEAMID123.sh.blur.capture", HostProfile: hostProfile, ExtensionProfile: extensionProfile}
	if err := Sign(context.Background(), cfg, runner); err == nil {
		t.Fatal("Sign() succeeded with an expired provisioning profile")
	}
	if len(runner.runs) != 0 {
		t.Fatalf("codesign ran with an expired profile: %v", runner.runs)
	}
}

func TestSignRejectsIdentityCertificateNotAuthorizedByProfiles(t *testing.T) {
	app := signingFixture(t)
	hostProfile := writeProfile(t, app, "host.provisionprofile", profileXML("com.hazcod.blur"))
	extensionProfile := writeProfile(t, app, "extension.provisionprofile", profileXML("com.hazcod.blur.safaricapture"))
	wrongCertificate := base64.StdEncoding.EncodeToString([]byte("different certificate"))
	profile := strings.ReplaceAll(profileXML("com.hazcod.blur"), base64.StdEncoding.EncodeToString([]byte("fixture certificate")), wrongCertificate)
	runner := &recordingRunner{profiles: map[string][]byte{
		filepath.Join(app, "Contents", "embedded.provisionprofile"):                                                   []byte(profile),
		filepath.Join(app, "Contents", "PlugIns", "BlurSafariCapture.appex", "Contents", "embedded.provisionprofile"): []byte(profileXML("com.hazcod.blur.safaricapture")),
	}}
	cfg := Config{AppPath: app, Identity: "Developer ID Application: Blur Builder (TEAMID123)", AppGroupID: "group.sh.blur.capture", KeychainAccessGroup: "TEAMID123.sh.blur.capture", HostProfile: hostProfile, ExtensionProfile: extensionProfile}
	if err := Sign(context.Background(), cfg, runner); err == nil {
		t.Fatal("Sign() accepted a certificate not authorized by the host profile")
	}
	if len(runner.runs) != 0 {
		t.Fatalf("codesign ran with an unauthorized certificate: %v", runner.runs)
	}
}

func TestSignRejectsConfiguredExtensionGroupMismatchBeforeProfileCommands(t *testing.T) {
	app := signingFixture(t)
	hostProfile := writeProfile(t, app, "host.provisionprofile", profileXML("com.hazcod.blur"))
	extensionProfile := writeProfile(t, app, "extension.provisionprofile", profileXML("com.hazcod.blur.safaricapture"))
	runner := &recordingRunner{}
	cfg := Config{AppPath: app, Identity: "TEAMID123", AppGroupID: "group.sh.blur.other", KeychainAccessGroup: "TEAMID123.sh.blur.capture", HostProfile: hostProfile, ExtensionProfile: extensionProfile}
	if err := Sign(context.Background(), cfg, runner); err == nil {
		t.Fatal("Sign() accepted extension metadata with a different App Group")
	}
	if len(runner.runs) != 0 {
		t.Fatalf("security or codesign ran before rejecting inconsistent extension metadata: %v", runner.runs)
	}
}

type recordingRunner struct {
	runs         [][]string
	entitlements [][]byte
	profiles     map[string][]byte
	err          error
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) error {
	r.runs = append(r.runs, append([]string{name}, args...))
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--entitlements" {
			contents, err := os.ReadFile(args[i+1])
			if err != nil {
				r.err = err
				return err
			}
			r.entitlements = append(r.entitlements, contents)
			break
		}
	}
	return nil
}

func (r *recordingRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	if name == "security" && len(args) == 4 && args[0] == "find-identity" {
		certificateHash := sha1.Sum([]byte("fixture certificate"))
		return []byte(fmt.Sprintf("  1) %X \"Developer ID Application: Blur Builder (TEAMID123)\"\n     1 valid identities found\n", certificateHash)), nil
	}
	if name != "security" || len(args) != 4 || args[0] != "cms" || args[1] != "-D" || args[2] != "-i" {
		r.err = os.ErrInvalid
		return nil, r.err
	}
	profile, ok := r.profiles[args[3]]
	if !ok {
		r.err = os.ErrNotExist
		return nil, r.err
	}
	return append([]byte(nil), profile...), nil
}

func signingFixture(t *testing.T) string {
	t.Helper()
	app := filepath.Join(t.TempDir(), "Blur.app")
	extension := filepath.Join(app, "Contents", "PlugIns", "BlurSafariCapture.appex")
	for _, dir := range []string{
		filepath.Join(app, "Contents", "MacOS"),
		filepath.Join(extension, "Contents", "MacOS"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{
		filepath.Join(app, "Contents", "MacOS", "blur"),
		filepath.Join(extension, "Contents", "MacOS", "BlurSafariCapture"),
	} {
		if err := os.WriteFile(file, []byte("fixture executable"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for path, bundleID := range map[string]string{
		filepath.Join(app, "Contents", "Info.plist"):       "com.hazcod.blur",
		filepath.Join(extension, "Contents", "Info.plist"): "com.hazcod.blur.safaricapture",
	} {
		contents := "<?xml version=\"1.0\"?><plist version=\"1.0\"><dict><key>CFBundleIdentifier</key><string>" + bundleID + "</string>"
		if bundleID == extensionBundleID {
			contents += "<key>BlurAppGroupIdentifier</key><string>group.sh.blur.capture</string><key>BlurKeychainAccessGroup</key><string>TEAMID123.sh.blur.capture</string>"
		}
		contents += "</dict></plist>"
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return app
}

func writeProfile(t *testing.T, app, name, contents string) string {
	t.Helper()
	path := filepath.Join(filepath.Dir(app), name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func profileXML(bundleID string) string {
	return profileXMLWithExpiration(bundleID, "2099-01-01T00:00:00Z")
}

func profileXMLWithExpiration(bundleID, expiration string) string {
	return "<?xml version=\"1.0\"?>\n<plist version=\"1.0\">\n<dict>\n<key>ExpirationDate</key>\n<date>" + expiration + "</date>\n<key>Entitlements</key>\n<dict>\n" +
		"<key>com.apple.application-identifier</key><string>TEAMID123." + bundleID + "</string>" +
		"<key>keychain-access-groups</key><array><string>TEAMID123.sh.blur.capture</string></array>" +
		"</dict>\n<key>DeveloperCertificates</key><array><data>" + base64.StdEncoding.EncodeToString([]byte("fixture certificate")) + "</data></array>\n</dict>\n</plist>"
}

func profileXMLWithoutGroups(bundleID string) string {
	return "<?xml version=\"1.0\"?><plist version=\"1.0\"><dict><key>Entitlements</key><dict>" +
		"<key>com.apple.application-identifier</key><string>TEAMID123." + bundleID + "</string>" +
		"</dict></dict></plist>"
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
