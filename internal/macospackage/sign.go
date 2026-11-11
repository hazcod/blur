// Package macospackage signs a staged Blur macOS application and embedded
// Safari extension with matching, profile-authorized shared entitlements.
package macospackage

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/appidentity"
)

const (
	hostBundleID      = appidentity.BundleID
	extensionBundleID = appidentity.SafariExtensionBundleID
	maxProfileBytes   = 4 << 20
)

var (
	ErrInvalidConfig  = errors.New("invalid macOS signing configuration")
	ErrInvalidBundle  = errors.New("invalid Blur app bundle")
	ErrInvalidProfile = errors.New("provisioning profile does not authorize Blur's signing identity or restricted Keychain entitlement")
)

type Config struct {
	AppPath             string
	Identity            string
	AppGroupID          string
	KeychainAccessGroup string
	HostProfile         string
	ExtensionProfile    string
}

type CommandRunner interface {
	Run(context.Context, string, ...string) error
	Output(context.Context, string, ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func (execRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// Sign signs a staged .app in place. The caller should provide a fresh
// staging bundle and publish it only after Sign returns nil. Both provisioning
// profiles are required because the explicit Keychain sharing entitlement is
// restricted and must be authorized for both bundle identifiers and the
// selected signing certificate.
func Sign(ctx context.Context, cfg Config, runner CommandRunner) error {
	if ctx == nil || ctx.Err() != nil || !validConfig(cfg) {
		return ErrInvalidConfig
	}
	if runner == nil {
		runner = execRunner{}
	}
	app, extension, err := validateBundle(cfg.AppPath, cfg.AppGroupID, cfg.KeychainAccessGroup)
	if err != nil {
		return err
	}
	if err := installProfile(cfg.HostProfile, filepath.Join(app, "Contents", "embedded.provisionprofile")); err != nil {
		return fmt.Errorf("install host provisioning profile: %w", err)
	}
	if err := installProfile(cfg.ExtensionProfile, filepath.Join(extension, "Contents", "embedded.provisionprofile")); err != nil {
		return fmt.Errorf("install extension provisioning profile: %w", err)
	}
	hostTeam, hostCertificates, err := decodeAndValidateProfile(ctx, runner, filepath.Join(app, "Contents", "embedded.provisionprofile"), hostBundleID, cfg.KeychainAccessGroup)
	if err != nil {
		return fmt.Errorf("validate host provisioning profile: %w", err)
	}
	extensionTeam, extensionCertificates, err := decodeAndValidateProfile(ctx, runner, filepath.Join(extension, "Contents", "embedded.provisionprofile"), extensionBundleID, cfg.KeychainAccessGroup)
	if err != nil {
		return fmt.Errorf("validate extension provisioning profile: %w", err)
	}
	if hostTeam != extensionTeam {
		return fmt.Errorf("%w: host and extension profiles belong to different teams", ErrInvalidProfile)
	}
	identityHash, err := resolveIdentityHash(ctx, runner, cfg.Identity)
	if err != nil || !containsString(hostCertificates, identityHash) || !containsString(extensionCertificates, identityHash) {
		return fmt.Errorf("%w: selected signing certificate is not authorized by both provisioning profiles", ErrInvalidProfile)
	}

	tempDir, err := os.MkdirTemp("", "blur-sign-entitlements-")
	if err != nil {
		return fmt.Errorf("create temporary signing metadata: %w", err)
	}
	defer func() {
		_ = os.Remove(filepath.Join(tempDir, "host.plist"))
		_ = os.Remove(filepath.Join(tempDir, "extension.plist"))
		_ = os.Remove(tempDir)
	}()
	hostEntitlements := filepath.Join(tempDir, "host.plist")
	extensionEntitlements := filepath.Join(tempDir, "extension.plist")
	if err := os.WriteFile(hostEntitlements, entitlementPlist(cfg.AppGroupID, cfg.KeychainAccessGroup, false), 0o600); err != nil {
		return fmt.Errorf("write host entitlements: %w", err)
	}
	if err := os.WriteFile(extensionEntitlements, entitlementPlist(cfg.AppGroupID, cfg.KeychainAccessGroup, true), 0o600); err != nil {
		return fmt.Errorf("write extension entitlements: %w", err)
	}

	if err := runCodesign(ctx, runner, cfg.Identity, extensionEntitlements, extension); err != nil {
		return fmt.Errorf("sign Safari extension: %w", err)
	}
	if err := runCodesign(ctx, runner, cfg.Identity, hostEntitlements, app); err != nil {
		return fmt.Errorf("sign host app: %w", err)
	}
	if err := runner.Run(ctx, "codesign", "--verify", "--strict", "--verbose=2", extension); err != nil {
		return fmt.Errorf("verify Safari extension signature: %w", err)
	}
	if err := runner.Run(ctx, "codesign", "--verify", "--deep", "--strict", "--verbose=2", app); err != nil {
		return fmt.Errorf("verify host app signature: %w", err)
	}
	return nil
}

func validConfig(cfg Config) bool {
	if cfg.AppPath == "" || !filepath.IsAbs(cfg.AppPath) || filepath.Clean(cfg.AppPath) != cfg.AppPath ||
		cfg.Identity == "" || strings.TrimSpace(cfg.Identity) != cfg.Identity || len(cfg.Identity) > 256 ||
		!validAppGroup(cfg.AppGroupID) || !validKeychainGroup(cfg.KeychainAccessGroup) ||
		cfg.HostProfile == "" || cfg.ExtensionProfile == "" || filepath.Clean(cfg.HostProfile) == filepath.Clean(cfg.ExtensionProfile) {
		return false
	}
	for i := 0; i < len(cfg.Identity); i++ {
		if cfg.Identity[i] < 0x20 || cfg.Identity[i] == 0x7f {
			return false
		}
	}
	return true
}

func validAppGroup(value string) bool {
	if len(value) < 12 || len(value) > 255 || !strings.HasPrefix(value, "group.") || strings.HasSuffix(value, ".") {
		return false
	}
	return validDottedIdentifier(value, 3)
}

func validKeychainGroup(value string) bool {
	return len(value) >= 5 && len(value) <= 255 && validDottedIdentifier(value, 3)
}

func validDottedIdentifier(value string, minSegments int) bool {
	segments, segmentLength := 0, 0
	for i := 0; i <= len(value); i++ {
		if i == len(value) || value[i] == '.' {
			if segmentLength == 0 {
				return false
			}
			segments++
			segmentLength = 0
			continue
		}
		c := value[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}
		segmentLength++
	}
	return segments >= minSegments
}

func validateBundle(path, appGroup, keychainGroup string) (string, string, error) {
	app, err := existingDirectory(path)
	if err != nil || filepath.Ext(app) != ".app" {
		return "", "", ErrInvalidBundle
	}
	for _, dir := range []string{
		filepath.Join(app, "Contents"),
		filepath.Join(app, "Contents", "MacOS"),
		filepath.Join(app, "Contents", "PlugIns"),
	} {
		if _, err := existingDirectory(dir); err != nil {
			return "", "", ErrInvalidBundle
		}
	}
	extension := filepath.Join(app, "Contents", "PlugIns", "BlurSafariCapture.appex")
	if _, err := existingDirectory(extension); err != nil {
		return "", "", ErrInvalidBundle
	}
	for _, dir := range []string{
		filepath.Join(extension, "Contents"),
		filepath.Join(extension, "Contents", "MacOS"),
	} {
		if _, err := existingDirectory(dir); err != nil {
			return "", "", ErrInvalidBundle
		}
	}
	for _, file := range []string{
		filepath.Join(app, "Contents", "MacOS", "blur"),
		filepath.Join(extension, "Contents", "MacOS", "BlurSafariCapture"),
	} {
		if err := existingRegularFile(file); err != nil {
			return "", "", ErrInvalidBundle
		}
	}
	for file, expected := range map[string]string{
		filepath.Join(app, "Contents", "Info.plist"):       hostBundleID,
		filepath.Join(extension, "Contents", "Info.plist"): extensionBundleID,
	} {
		contents, err := os.ReadFile(file)
		if err != nil {
			return "", "", ErrInvalidBundle
		}
		parsed, err := decodePlist(contents)
		if err != nil {
			return "", "", ErrInvalidBundle
		}
		values, ok := parsed.(map[string]any)
		if !ok || values["CFBundleIdentifier"] != expected {
			return "", "", ErrInvalidBundle
		}
		if expected == extensionBundleID && (values["BlurAppGroupIdentifier"] != appGroup || values["BlurKeychainAccessGroup"] != keychainGroup) {
			return "", "", ErrInvalidBundle
		}
	}
	return app, extension, nil
}

func existingDirectory(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", ErrInvalidBundle
	}
	return path, nil
}

func existingRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return ErrInvalidBundle
	}
	return nil
}

func installProfile(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxProfileBytes {
		return ErrInvalidProfile
	}
	input, err := os.Open(source)
	if err != nil {
		return ErrInvalidProfile
	}
	openedInfo, statErr := input.Stat()
	if statErr != nil || !openedInfo.Mode().IsRegular() {
		_ = input.Close()
		return ErrInvalidProfile
	}
	contents, err := io.ReadAll(io.LimitReader(input, maxProfileBytes+1))
	closeErr := input.Close()
	if err != nil || closeErr != nil || len(contents) == 0 || len(contents) > maxProfileBytes {
		clear(contents)
		return ErrInvalidProfile
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		clear(contents)
		return ErrInvalidProfile
	}
	_, writeErr := output.Write(contents)
	closeErr = output.Close()
	clear(contents)
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(destination)
		return ErrInvalidProfile
	}
	return nil
}

func decodeAndValidateProfile(ctx context.Context, runner CommandRunner, path, bundleID, keychainGroup string) (string, []string, error) {
	raw, err := runner.Output(ctx, "security", "cms", "-D", "-i", path)
	if err != nil || len(raw) == 0 || len(raw) > maxProfileBytes*2 {
		clear(raw)
		return "", nil, ErrInvalidProfile
	}
	defer clear(raw)
	decoded, err := decodePlist(raw)
	if err != nil {
		return "", nil, ErrInvalidProfile
	}
	profile, ok := decoded.(map[string]any)
	if !ok {
		return "", nil, ErrInvalidProfile
	}
	expiresText, ok := profile["ExpirationDate"].(string)
	expiresAt, parseErr := time.Parse(time.RFC3339, expiresText)
	if !ok || parseErr != nil || !time.Now().Before(expiresAt) {
		return "", nil, ErrInvalidProfile
	}
	certificates, ok := profile["DeveloperCertificates"].([]any)
	if !ok || len(certificates) == 0 {
		return "", nil, ErrInvalidProfile
	}
	fingerprints := make([]string, 0, len(certificates))
	for _, value := range certificates {
		encoded, ok := value.(string)
		if !ok {
			return "", nil, ErrInvalidProfile
		}
		certificate, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(encoded), ""))
		if err != nil || len(certificate) == 0 {
			clear(certificate)
			return "", nil, ErrInvalidProfile
		}
		fingerprint := sha1.Sum(certificate)
		clear(certificate)
		fingerprints = append(fingerprints, fmt.Sprintf("%X", fingerprint))
	}
	entitlements, ok := profile["Entitlements"].(map[string]any)
	if !ok || !containsEntitlement(entitlements["keychain-access-groups"], keychainGroup) {
		return "", nil, ErrInvalidProfile
	}
	applicationID, ok := entitlements["com.apple.application-identifier"].(string)
	if !ok {
		applicationID, ok = entitlements["application-identifier"].(string)
	}
	team, bundlePattern, hasTeam := strings.Cut(applicationID, ".")
	if !ok || !hasTeam || team == "" || !matchesProfilePattern(bundlePattern, bundleID) {
		return "", nil, ErrInvalidProfile
	}
	if !strings.HasPrefix(keychainGroup, team+".") {
		return "", nil, ErrInvalidProfile
	}
	return team, fingerprints, nil
}

func resolveIdentityHash(ctx context.Context, runner CommandRunner, identity string) (string, error) {
	if len(identity) == 40 {
		for _, c := range identity {
			if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
				return "", ErrInvalidConfig
			}
		}
		return strings.ToUpper(identity), nil
	}
	output, err := runner.Output(ctx, "security", "find-identity", "-v", "-p", "codesigning")
	if err != nil || len(output) > 1<<20 {
		clear(output)
		return "", ErrInvalidProfile
	}
	defer clear(output)
	var match string
	for _, line := range strings.Split(string(output), "\n") {
		close := strings.Index(line, ") ")
		if close < 0 {
			continue
		}
		rest := strings.TrimSpace(line[close+2:])
		space := strings.IndexByte(rest, ' ')
		if space != 40 || len(rest) < space+2 || rest[space+1] != '"' {
			continue
		}
		name, err := strconv.Unquote(strings.TrimSpace(rest[space+1:]))
		if err == nil && name == identity {
			if match != "" {
				return "", ErrInvalidProfile
			}
			match = strings.ToUpper(rest[:space])
		}
	}
	if match == "" {
		return "", ErrInvalidProfile
	}
	return match, nil
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func matchesProfilePattern(pattern, expected string) bool {
	if pattern == expected || pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(expected, strings.TrimSuffix(pattern, "*"))
	}
	if strings.HasPrefix(expected, "*.") {
		return strings.HasSuffix(pattern, strings.TrimPrefix(expected, "*"))
	}
	return false
}

func containsEntitlement(value any, expected string) bool {
	values, ok := value.([]any)
	if !ok {
		return false
	}
	for _, candidate := range values {
		pattern, ok := candidate.(string)
		if ok && matchesProfilePattern(pattern, expected) {
			return true
		}
	}
	return false
}

func entitlementPlist(appGroup, keychainGroup string, sandbox bool) []byte {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?><plist version=\"1.0\"><dict>")
	if sandbox {
		b.WriteString("<key>com.apple.security.app-sandbox</key><true/>")
	}
	b.WriteString("<key>com.apple.security.application-groups</key><array><string>")
	b.WriteString(xmlValue(appGroup))
	b.WriteString("</string></array><key>keychain-access-groups</key><array><string>")
	b.WriteString(xmlValue(keychainGroup))
	b.WriteString("</string></array></dict></plist>")
	return []byte(b.String())
}

func xmlValue(value string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}

func runCodesign(ctx context.Context, runner CommandRunner, identity, entitlements, target string) error {
	return runner.Run(ctx, "codesign", "--force", "--timestamp", "--options", "runtime", "--sign", identity,
		"--entitlements", entitlements, target)
}

func decodePlist(raw []byte) (any, error) {
	decoder := xml.NewDecoder(strings.NewReader(string(raw)))
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		start, ok := token.(xml.StartElement)
		if ok && start.Name.Local == "plist" {
			for {
				token, err = decoder.Token()
				if err != nil {
					return nil, err
				}
				if valueStart, ok := token.(xml.StartElement); ok {
					value, err := decodePlistValue(decoder, valueStart)
					if err != nil {
						return nil, err
					}
					for {
						token, err := decoder.Token()
						if err != nil {
							return nil, err
						}
						switch trailing := token.(type) {
						case xml.EndElement:
							if trailing.Name.Local != "plist" {
								return nil, ErrInvalidProfile
							}
							return value, nil
						case xml.CharData:
							if strings.TrimSpace(string(trailing)) != "" {
								return nil, ErrInvalidProfile
							}
						default:
							return nil, ErrInvalidProfile
						}
					}
				}
			}
		}
	}
}

func decodePlistValue(decoder *xml.Decoder, start xml.StartElement) (any, error) {
	switch start.Name.Local {
	case "dict":
		result := make(map[string]any)
		for {
			token, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			switch value := token.(type) {
			case xml.EndElement:
				if value.Name.Local == "dict" {
					return result, nil
				}
			case xml.CharData:
				if strings.TrimSpace(string(value)) != "" {
					return nil, ErrInvalidProfile
				}
			case xml.StartElement:
				if value.Name.Local != "key" {
					return nil, ErrInvalidProfile
				}
				key, err := decodeXMLText(decoder, value)
				if err != nil || key == "" {
					return nil, ErrInvalidProfile
				}
				next, err := nextNonWhitespaceToken(decoder)
				if err != nil {
					return nil, err
				}
				valueStart, ok := next.(xml.StartElement)
				if !ok {
					return nil, ErrInvalidProfile
				}
				if _, duplicate := result[key]; duplicate {
					return nil, ErrInvalidProfile
				}
				parsed, err := decodePlistValue(decoder, valueStart)
				if err != nil {
					return nil, err
				}
				result[key] = parsed
			}
		}
	case "array":
		result := make([]any, 0)
		for {
			token, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			switch value := token.(type) {
			case xml.EndElement:
				if value.Name.Local == "array" {
					return result, nil
				}
			case xml.StartElement:
				parsed, err := decodePlistValue(decoder, value)
				if err != nil {
					return nil, err
				}
				result = append(result, parsed)
			case xml.CharData:
				if strings.TrimSpace(string(value)) != "" {
					return nil, ErrInvalidProfile
				}
			}
		}
	case "string", "date", "data", "integer", "real":
		return decodeXMLText(decoder, start)
	case "true", "false":
		if err := decoder.Skip(); err != nil {
			return nil, err
		}
		return start.Name.Local == "true", nil
	default:
		return nil, ErrInvalidProfile
	}
}

func nextNonWhitespaceToken(decoder *xml.Decoder) (xml.Token, error) {
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		if text, ok := token.(xml.CharData); ok {
			if strings.TrimSpace(string(text)) != "" {
				return nil, ErrInvalidProfile
			}
			continue
		}
		return token, nil
	}
}

func decodeXMLText(decoder *xml.Decoder, start xml.StartElement) (string, error) {
	var text string
	if err := decoder.DecodeElement(&text, &start); err != nil {
		return "", err
	}
	return text, nil
}
