package nocgoaudit

import (
	"strings"
	"testing"
)

func TestAuditPackagesRejectsCgoFilesFromEnabledGraph(t *testing.T) {
	err := AuditPackages([]Package{{
		ImportPath: "example.org/driver", Dir: "/deps/driver",
		CgoFiles: []string{"bridge.go"},
	}}, func(path string) ([]byte, error) {
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "example.org/driver") || !strings.Contains(err.Error(), "CgoFiles") {
		t.Fatalf("AuditPackages() error = %v, want CgoFiles rejection for dependency", err)
	}
}

func TestAuditPackagesRejectsGroupedAndAliasedImportC(t *testing.T) {
	for _, source := range []string{
		"package probe\nimport (\n\t\"fmt\"\n\t\"C\"\n)\n",
		"package probe\nimport (\n\tc \"C\"\n)\n",
	} {
		err := AuditPackages([]Package{{
			ImportPath: "example.org/probe", Dir: "/deps/probe",
			GoFiles: []string{"probe.go"},
		}}, func(string) ([]byte, error) { return []byte(source), nil })
		if err == nil || !strings.Contains(err.Error(), "example.org/probe") || !strings.Contains(err.Error(), "import C") {
			t.Fatalf("AuditPackages() error = %v, want import C rejection for source %q", err, source)
		}
	}
}

func TestAuditPackagesChecksIgnoredSourcesButExemptsStandardLibrary(t *testing.T) {
	err := AuditPackages([]Package{
		{
			ImportPath: "example.org/optional", Dir: "/deps/optional",
			IgnoredGoFiles: []string{"optional.go"},
		},
		{
			ImportPath: "runtime/cgo", Standard: true, Dir: "/go/src/runtime/cgo",
			CgoFiles: []string{"gcc.go"}, GoFiles: []string{"runtime.go"},
		},
	}, func(path string) ([]byte, error) {
		if strings.HasSuffix(path, "optional.go") {
			return []byte("package optional\nimport (\n\t\"C\"\n)\n"), nil
		}
		return []byte("package cgo\nimport \"C\"\n"), nil
	})
	if err == nil || !strings.Contains(err.Error(), "example.org/optional") || !strings.Contains(err.Error(), "import C") {
		t.Fatalf("AuditPackages() error = %v, want ignored dependency source rejection", err)
	}
}

func TestAuditPackagesAllowsPureGoThirdPartyDependency(t *testing.T) {
	err := AuditPackages([]Package{{
		ImportPath: "example.org/pure", Dir: "/deps/pure",
		GoFiles: []string{"pure.go"},
	}}, func(string) ([]byte, error) { return []byte("package pure\n"), nil })
	if err != nil {
		t.Fatalf("AuditPackages() error = %v, want nil", err)
	}
}

func TestAuditPackagesExemptsStandardLibraryCgo(t *testing.T) {
	err := AuditPackages([]Package{{
		ImportPath: "runtime/cgo", Standard: true, Dir: "/go/src/runtime/cgo",
		CgoFiles: []string{"gcc.go"}, GoFiles: []string{"runtime.go"},
	}}, func(string) ([]byte, error) { return []byte("package cgo\nimport \"C\"\n"), nil })
	if err != nil {
		t.Fatalf("AuditPackages() error = %v, want standard library exemption", err)
	}
}
