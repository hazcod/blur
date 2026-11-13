# Getting started with Blur

Blur is a cross-platform desktop app in development. macOS is the primary development platform. Windows and Linux have native capture implementations and portable packages, but still need live desktop validation. Use the [platform status](project-status.md) to check the current limitations before relying on Blur.

## Choose a build

Published versions, when available, are listed on the [releases page](https://github.com/hazcod/blur/releases). If there is no release yet, build from source or try a Windows or Linux CI artifact:

1. Open the [CI workflow](https://github.com/hazcod/blur/actions/workflows/ci.yml) and select a recent run for the commit you want to try.
2. Check that the **Verify** job completed its portable packaging step. Review the platform test results too; Windows tests are currently advisory.
3. While signed in to GitHub, download the **blur-portable** artifact from that run and extract it. Artifacts expire after 14 days; a failed packaging step will not produce one.

The artifact contains `blur-windows-amd64.zip`, `blur-linux-amd64.tar.gz` and `blur-linux-arm64.tar.gz`. These are unsigned development builds. CI does not upload a macOS app through this artifact.

To build from source, install the Go version specified in [go.mod](../go.mod), clone the repository and use `make macos-app` on macOS or `make portable-packages` for Windows and Linux archives. See [Contributing](../CONTRIBUTING.md) for verification commands.

## macOS

The app bundle is universal: Apple silicon and Intel use the same `Blur.app`. A source build writes it to `~/Downloads/Blur.app`; a published macOS archive contains the app bundle. Move it to Applications when you are ready to use it.

Grant Blur access in **System Settings → Privacy & Security → Accessibility**. Blur reads accessible text from the focused app; it does not need Screen Recording permission. Choose the apps and sites it may capture in Settings, then start capture explicitly.

The database key is stored in macOS Keychain. The normal app uses App Sandbox. When replacing an existing signed build, preserve its signing identity and sandbox configuration so macOS can continue to recognize it.

The notch panel is a macOS integration. Safari page capture is optional, off by default, and requires a build with the separately signed extension; it is absent from the default app.

## Windows

Extract `blur-windows-amd64.zip` and start `Blur.exe`. Keep `blur-mcp.exe` alongside it if you plan to use the optional MCP connection; that companion provides the console transport for MCP clients.

Windows capture uses UI Automation and protects the database key with DPAPI for the current user. Capture follows the app's explicit start and pause controls. Secure desktops, elevated windows and apps that do not expose accessible text may be unavailable to capture. Live Windows runtime validation is still pending.

For troubleshooting, the GUI writes rotating logs under `%APPDATA%\Blur\Logs`. Review and redact logs before sharing them in an issue.

## Linux

Choose the archive for your machine: `amd64` for x86-64 or `arm64` for ARM64. Blur currently needs an **X11 desktop session**, a working session **D-Bus**, **AT-SPI2** accessibility services, and an unlocked **Secret Service** keyring such as GNOME Keyring or KWallet with Secret Service support. Wayland capture is not currently supported.

Extract the archive and run `./blur` from its directory. For a user-local installation, put the executable on your `PATH`, such as in `~/.local/bin`. CI portable archives also include a desktop launcher and SVG icon for the standard per-user XDG application and icon directories.

Blur uses AT-SPI2 to read accessible text from the focused app and Secret Service to protect its database key. It does not unlock a locked keyring for you. If capture or startup is unavailable, check the session, accessibility services and keyring first. Live Linux desktop validation is still pending on both architectures.

## Start with local memory

1. Review the capture settings and exclude sensitive apps and sites before starting capture.
2. Start capture, then use Search and Time to explore what Blur has recorded. You can pause capture at any time.
3. Keep AI off, or configure a local Ollama model in **Settings → AI** for summaries and answers. Search, Library and Time work without a model.
4. If you configure a remote OpenAI-compatible endpoint, memory evidence requires a separate opt-in. The provider may retain requests.

Folder sync and MCP are optional connections. Sync writes encrypted change files to a folder carried by your chosen provider; the provider can still see file sizes and timing. MCP shares selected search results with the client you enable, and a hosted client may pass them to its model provider. See [project status](project-status.md) for setup and boundaries.

## Get help

Report reproducible problems through the [bug report form](https://github.com/hazcod/blur/issues/new?template=bug_report.yml). Include the Blur version or commit, OS version, architecture and, on Linux, desktop and session type. Use synthetic examples and redact diagnostics; do not attach your memory database, captured text, credentials or keyring data. Report vulnerabilities through the [security policy](../SECURITY.md).
