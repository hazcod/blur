# Contributing

Contributions should preserve the clean-room, local-first, privacy-first design
described in the project specification. Do not copy source code, prompts,
branding, icons, or other creative assets from another product.

Before submitting a change:

1. Keep changes focused and document user-visible behavior.
2. Add or update tests for behavior changes.
3. Run `make test`, `make race`, and `make vet` with Go 1.23 or newer.
4. Avoid new dependencies unless the design and security tradeoffs are clear.

Please include the platforms and Go version used to verify platform-sensitive
changes. Accessibility capture and data handling changes should explain their
privacy boundaries and failure behavior.
