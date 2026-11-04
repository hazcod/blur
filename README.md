# Blur

Blur is a local-first, privacy-focused memory and writing assistant for desktop
computers. Its design prioritizes local storage and search, bounded accessible
text capture, explicit AI routing, and user control over retention and deletion.
See the [design](docs/superpowers/specs/2026-11-04-blur-design.md) for the
current product and architecture direction.

This repository is an early clean-room implementation. It does not include the
desktop application or capture adapters yet. The Go module path
`github.com/nielsgroen/blur` is a temporary OSS identity and may change before
the first public release.

## Development

Requires Go 1.27 or newer. The current packages use only the Go standard
library.

```sh
make test
make race
make vet
make build
```

Build metadata uses stable development defaults. Release tooling may set the
version at link time with `-X github.com/nielsgroen/blur/internal/buildinfo.Version=...`.
The user-agent includes only the product name and version; it does not include
the machine, account, commit date, or runtime environment.

## Supported direction

The initial platform targets are macOS 13+, Windows 11, and tested GNOME/KDE
sessions on Linux. Platform features will report unavailable capabilities
honestly rather than silently falling back to screenshots, OCR, or remote AI.

## License

Apache-2.0. See [LICENSE](LICENSE).
