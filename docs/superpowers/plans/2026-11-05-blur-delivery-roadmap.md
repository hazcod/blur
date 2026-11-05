# Blur Cross-Platform Delivery Roadmap

> **Status:** Active product delivery order. This supersedes the macOS-first
> ordering and fixed SQLite/Wails choices in the older full-reimplementation
> backlog. The approved foundation at commit `40476c7` remains the contract.

**Goal:** Deliver a secure, cgo-free, cross-platform personal memory assistant
whose useful capture, search, Today recap, knowledge library, time accounting,
and local-AI paths work without an account or cloud dependency.

## Invariants across every phase

- All shipped Go binaries and their selected dependency graphs build with
  `CGO_ENABLED=0`; the repository audit must remain green.
- Raw accessibility values exist only inside the platform-host capture stage.
  Storage, IPC, UI, MCP, and AI accept validated sanitized observations only.
- Capture, exact retrieval, Today, knowledge browsing, time accounting,
  lifecycle, and export work without AI. Capture never invokes a model.
- Model output is untrusted, source-linked, reversible, and cannot change
  policy, grants, retention, provider routing, or external state.
- No screenshot, OCR, clipboard, keystroke, synthetic-input, or cloud fallback
  is silent. Unsupported capabilities report structured degradation.
- Every data-bearing file is authenticated and encrypted or demonstrably
  contains no personal content. Logs use a closed, content-free `log/slog`
  vocabulary.
- Each phase uses test-driven implementation, task-level independent review,
  a whole-phase security review, race tests, cross-builds, and adversarial
  sentinel scans before the next trust boundary opens.

## Delivery phases

1. **Cross-platform foundation — complete.** Portable capability contracts,
   staged capture/redaction, scoped proofs, storage/secret contracts, bounded
   offline search, OS skeletons, no-cgo gates, architecture and threat model.
2. **Durable memory core — active.** Select and gate a small pure-Go physical
   store; add authenticated record encryption, persistent observation and
   provenance repositories, private exact indexes, activity/time ledgers,
   instant Today snapshots, and the browsable People/Projects/Topics/Places/
   Workflows library. See `2026-11-05-blur-durable-memory-core.md`.
3. **Secure host and local transport.** Implement OS-protected data-key
   wrapping, private paths, single-instance coordination, AF_UNIX/named-pipe
   transport with peer identity and bounded admission queues, then fixture-
   driven AXUIElement, UI Automation, and AT-SPI2 capture adapters. Capability
   parity is required; unsupported desktop mechanics degrade honestly.
4. **Retrieval and provider-neutral AI.** Add deterministic timeline/session/
   entity retrieval, rank fusion and evidence packing before optional local
   embeddings. Implement separate Generator/Embedder/Transcriber/Reranker/
   TokenCounter ports, hard token admission, Strict Local Mode, Ollama and
   OpenAI-compatible local endpoints first, then explicitly enabled remote
   providers. Hover/Today remains model-free.
5. **Pure-Go desktop product.** Gate Wails 3 pure-Go mode behind a shell facade
   and signed fixture tests; ship memory bar hover, cited daily view, search,
   chat, dashboard, knowledge browser, source inspection, pause/exclusions,
   time reports, settings, diagnostics, and token/egress previews. Provide a
   simpler native/web fallback shell if the experimental backend fails gates.
6. **Guarded writing and dictation.** Add hotkeys, target leases, selection/
   draft/edit workflows, local transcription, and focus/original-value
   revalidation before mutation. Never silently fall back to clipboard or
   synthetic input; platforms without safe injection remain read-only.
7. **Proactive memory and interoperability.** Add hierarchical chronicles,
   editable facts/identity/style, open loops, reminders, schedules, workflows,
   reduced read-only-by-default MCP over stdio, authenticated export/import,
   retention, targeted deletion, delete-all, rekey, and backup/restore.
8. **Release hardening.** 100k/1M scale benchmarks, fuzz/property suites,
   crash/tamper/key-loss tests, accessibility fixture matrix, SBOM and license
   checks, signed/notarized macOS and Windows packages, reproducible Linux
   packages, upgrade/rollback/uninstall tests, and an external security review.

## Definition of product-complete

Product completion requires every phase above to pass its review gates on all
tier-one operating systems. A compiled placeholder, design document, or green
unit suite on one host does not count as feature completion.
