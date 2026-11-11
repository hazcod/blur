# Dependency forks

Blur builds from `go.mod` with no `vendor/` directory. Two dependencies are
replaced by forks until the upstream pull requests merge; the `replace`
lines in `go.mod` point at a commit on each fork.

## github.com/golang-gui/goui

Fork: `github.com/hazcod/goui`, branch `linux-arm64-pipe-poll` on top of
v0.7.0. Upstream v0.7.0 calls `syscall.SYS_PIPE` and `syscall.SYS_POLL`,
which do not exist on linux/arm64, so the X11 backend does not compile
there. The fix uses `golang.org/x/sys/unix` `Pipe` and `Poll` and reads
`PollFd.Revents` in the event loop.
Upstream PR: https://github.com/golang-gui/goui/pull/111

## github.com/lpintes/walk

Fork: `github.com/hazcod/walk`, branch `blur-uia-bounded` on top of the
pinned commit. Blur uses only `walk/uia`. The fork sets a 200 ms UI
Automation transaction timeout during MTA initialisation (the default is
20 s, far beyond Blur's 750 ms capture deadline) and fails closed when it
cannot be set, adds `Element.TextBounded` so UIA bounds returned text before
allocation, and adds the nonblocking `Element.ReleaseLater` queue so a
stalled provider cannot block pause or shutdown.
Upstream PR: https://github.com/lpintes/walk/pull/31

## Dropping a fork

When an upstream PR merges and a release contains it, remove the `replace`
line, run `go get <module>@<version>` and `go mod tidy`, and build for
linux/arm64 and windows/amd64 with `make cross-build`.
