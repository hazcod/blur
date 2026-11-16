# Multi-Scale Chronicle Read Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the local MCP assistant read bounded, paginated hourly recaps for a requested date range so it can answer day and week questions from source-linked summaries.

**Architecture:** Reuse the encrypted store's existing `ListChronicles` cursor. Add a read-only local RPC and MCP tool that validate the half-open UTC range, validate each recap and source ID, and return bounded pages with a continuation cursor. Keep the single-hour `get_chronicle` tool unchanged.

**Tech Stack:** Go, authenticated local RPC, MCP SDK, encrypted ChronicleStore.

**Spec:** `docs/goldfish-feature-parity.md`, `docs/goldfish-local-architecture.md`.

## Global Constraints

- Never bypass the authenticated local RPC or read a second store.
- Preserve source IDs and source-deletion memory fences.
- Keep date ranges within the existing 32-day storage bound and pages within RPC/MCP frame bounds.
- The app build target remains the active worktree's `make macos-app-stable` target and canonical signed bundle.

## Review Focus

- Malformed, reversed, or overlong date ranges are rejected before storage access.
- Pagination resumes strictly after the last returned hour and does not skip recaps when output is byte-bounded.
- Invalid summary provenance fails closed.
- Memory deletion/change between read and publication prevents stale recap output.
- A sparse range returns an empty bounded page without fabricating coverage.

### Task 1: Local RPC range reader

**Files:** `internal/localapi/rpc.go`, `internal/localapi/rpc_test.go`, `internal/client/mcp_read.go`, `internal/client/client_test.go`.

- [x] Add a regression test proving an MCP read returns source-linked recap rows and a resumable cursor.
- [x] Confirm the test fails because the range RPC is not implemented.
- [x] Add the `list_chronicles` RPC request and handler with strict UTC range/cursor validation, summary provenance checks, a maximum of eight requested rows, byte-bounded output and memory-epoch publication.
- [x] Run the focused client/localapi tests.

### Task 2: MCP tool and parity documentation

**Files:** `internal/mcpserver/server.go`, `internal/mcpserver/server_test.go`, `docs/goldfish-feature-parity.md`.

- [x] Add a regression test for the public `get_chronicles` tool, its date-range parameters, and source-linked pagination.
- [x] Confirm the test fails because the tool/RPC does not exist.
- [x] Register `get_chronicles` without changing `get_chronicle`; validate the output and update the parity table.
- [x] Run focused and full affected-package tests, inspect the diff, and build/verify the canonical signed macOS app.
