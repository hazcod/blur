package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureConfig(t *testing.T) (string, string) {
	t.Helper()
	nonce := strings.Repeat("a", 32)
	directory := filepath.Join(t.TempDir(), "blur-acceptance-fixture")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "acceptance.json"), []byte(`{"version":1,"nonce":"`+nonce+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return directory, nonce
}
func TestAcceptanceConfigBindsPrivateDirectoryAndFixtureNamespace(t *testing.T) {
	directory, nonce := fixtureConfig(t)
	config, err := loadAt(directory, nonce)
	if err != nil || !strings.HasSuffix(config.DataDirectory(), "/Data") || !strings.Contains(config.Domain, nonce) || !strings.Contains(config.SecretService, nonce) || config.SecretService == "sh.blur.desktop" {
		t.Fatal(config, err)
	}
	if !Enabled {
		t.Setenv("BLUR_ACCEPTANCE_DIR", directory)
		t.Setenv("BLUR_UI_SNAPSHOT_DIR", directory)
		if _, err := Load(); err == nil {
			t.Fatal("production binary accepted fixture environment")
		}
	}
}
func TestAcceptanceConfigRejectsWrongMarkerOrUnsafeDirectory(t *testing.T) {
	for _, kind := range []string{"nonce", "marker", "permissions", "symlink", "marker_symlink", "trailing", "data_symlink", "logs_symlink", "data_permissions", "logs_permissions"} {
		t.Run(kind, func(t *testing.T) {
			directory, nonce := fixtureConfig(t)
			switch kind {
			case "data_symlink", "logs_symlink", "data_permissions", "logs_permissions":
				name := "Data"
				if strings.HasPrefix(kind, "logs_") {
					name = "Logs"
				}
				path := filepath.Join(directory, name)
				if strings.HasSuffix(kind, "_symlink") {
					if err := os.Symlink(t.TempDir(), path); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Mkdir(path, 0o700); err != nil {
						t.Fatal(err)
					}
					// Mkdir applies the caller's umask. Set the rejected mode explicitly.
					if err := os.Chmod(path, 0o755); err != nil {
						t.Fatal(err)
					}
				}
			case "nonce":
				nonce = strings.Repeat("b", 32)
			case "trailing":
				file := filepath.Join(directory, "acceptance.json")
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, append(data, []byte("BROKEN")...), 0o600); err != nil {
					t.Fatal(err)
				}
			case "marker":
				if err := os.WriteFile(filepath.Join(directory, "acceptance.json"), []byte(`{"version":1,"nonce":"bad"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if err := os.Chmod(directory, 0o755); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				link := filepath.Join(filepath.Dir(directory), "blur-acceptance-link")
				if err := os.Symlink(directory, link); err != nil {
					t.Fatal(err)
				}
				directory = link
			case "marker_symlink":
				marker := filepath.Join(directory, "acceptance.json")
				target := filepath.Join(directory, "marker.json")
				if err := os.Rename(marker, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, marker); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := loadAt(directory, nonce); err == nil {
				t.Fatal("unsafe fixture configuration accepted")
			}
		})
	}
}
