// Package buildinfo provides deterministic public build metadata.
package buildinfo

// Version is set at link time for release builds. Development builds use the
// safe, stable default rather than deriving identity from the host environment.
var Version = "dev"

// UserAgent returns the concise identity sent by Blur when identifying itself
// to a configured provider or service.
func UserAgent() string {
	return "Blur/" + Version
}
