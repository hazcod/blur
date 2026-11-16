# Working-Memory Read Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the memory agent a fast, bounded read of the user's newest saved captures for vague/current questions.

**Architecture:** Add a read-only authenticated RPC backed by the encrypted observation-window reader, and expose it through MCP. Return newest-first UTF-8 excerpts with source IDs, capture times, explicit truncation/indexing status, and a strict six-hour maximum; exclude Blur's own interface.

**Tech Stack:** Go, encrypted ObservationWindowReader, authenticated local RPC, MCP SDK.

**Spec:** `docs/goldfish-feature-parity.md`, `docs/goldfish-local-architecture.md`.

## Global Constraints

- Read only from the already-open encrypted store through authenticated local RPC.
- Preserve source IDs and ask the host to fetch full captures before exact claims.
- Never include Blur's own interface captures.
- Bound lookback to six hours, results to eight, and excerpts/output to the existing MCP response budgets.
- Preserve cancellation and memory-epoch publication fences.
- Build only the canonical signed app from the active worktree; do not launch it.

## Review Focus

- Empty recent windows return an honest empty result.
- A long multipart page returns UTF-8-safe head/tail excerpting and a resolvable source ID.
- Out-of-window and Blur UI captures are excluded.
- Invalid hours/limits, malformed source IDs, and stale memory epochs fail closed.
- Indexing-pending and page truncation remain visible to the agent.

### Task 1: Bounded local working-memory read

**Files:** `internal/localapi/rpc.go`, `internal/client/mcp_read.go`, `internal/client/client_test.go`, `internal/localapi/rpc_test.go`.

- [x] Add regression cases for bounded time scope, Blur exclusion, and invalid limits.
- [x] Confirm tests fail because the RPC is not implemented.
- [x] Add the RPC and client reader with newest-first excerpts, source IDs, truncation state, and memory epoch validation.
- [x] Run focused tests.

### Task 2: MCP surface and parity record

**Files:** `internal/mcpserver/server.go`, `internal/mcpserver/server_test.go`, `docs/goldfish-feature-parity.md`.

- [x] Add MCP tool discovery and source-linked result regression coverage.
- [x] Register and validate the read-only `get_working_memory` tool.
- [x] Update the parity record and run affected package tests.
- [x] Rebuild and verify the canonical signed macOS app.
