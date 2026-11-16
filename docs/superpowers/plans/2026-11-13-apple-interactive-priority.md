# Apple Interactive Priority Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Prevent optional Apple background inference from blocking an explicit question for its full model deadline, preserving queued work.

**Architecture:** A context annotation marks optional work; default inference is interactive. A process-wide scheduler cancels a background helper when an interactive request arrives, retains its slot until `cmd.Run` retires, and admits interactive waiters before more background work. A distinct preemption error lets bounded workers defer the same job through existing policy, source, consent and memory fences.

**Tech Stack:** Go context cancellation, mutex/change-channel admission, existing signed Apple helper subprocess and synthetic Go child-process tests.

**Spec:** Parent-approved scoped design in the task handoff (2026-11-13); this document records it without introducing provider or inference changes.

## Global Constraints

- One native Apple inference process at a time; no slot release before process retirement.
- Other providers ignore the annotation and retain current behavior.
- No automatic retries for real model, cancellation, policy, deletion or close errors.
- Every deferred worker attempt revalidates source, consent and epochs; queue bounds and joins remain intact.
- No model/user data/device/network calls in verification; synthetic subprocess and encrypted temporary fixtures only.

## Review Focus

- Interactive arrivals during background retirement cannot overlap native processes.
- A canceled interactive waiter cannot launch or leave a stuck priority count.
- Preempted note/recap work must not be removed or marked completed.
- Deletion, reconfiguration and Close must prevent a deferred job from resuming.
- Library cursors advance only after validated successful persistence.

## Tasks

### 1. Priority admission and process retirement

Files: new `internal/model/apple_priority.go`, tests; `internal/model/apple.go` and existing gate test.

- [x] Write/run RED tests for interactive preemption, retirement barrier, waiter cancellation and priority.
- [x] Add `WithBackgroundInference(context.Context) context.Context`, `ErrBackgroundPreempted` and bounded single-flight scheduler.
- [x] Wire Apple generation only; keep original caller error precedence and await subprocess retirement.
- [x] Run focused model tests/races.

### 2. Saved-note deferral

Files: `internal/notes/{service,saved}.go`, `internal/client/note_processing.go`, tests.

- [x] Write/run RED saved error propagation and queued-note retention/retirement regressions.
- [x] Preserve the sentinel through extraction and requeue the same source/epochs after current-policy validation.
- [x] Keep local source Save independent, bounded membership/dedup, and Close cancellation/join.
- [x] Run focused note/client tests/races.

### 3. Library and recap deferral

Files: `internal/knowledge/enrich/service.go`, `internal/chronicle/worker.go`, tests.

- [x] Write/run RED retained-job and unchanged-cursor regressions, including no resume after cancellation/policy/close.
- [x] Annotate optional worker calls; retry only preemption under existing budgets and fences.
- [x] Run focused package tests/races.

### 4. Final verification and review

- [x] Run scoped lint/vet and all affected package races plus targeted client tests.
- [x] Report exact tested behavior and remaining latency limits; hand source to independent reviewer (independent review in progress).

## Verification record

Observed RED→GREEN for priority admission and actual synthetic helper retirement,
saved-note error propagation, note/recap job retention, and Library cursor/status
yield. Retry regressions cover deletion, policy, Close, actual provider errors,
source timestamp replacement and later visit time. The production Chronicle
repository adapter forwards source clocks and fences reads with memory epochs.
Legacy repositories without a source reader cannot retry deferred packed text.

Scoped race tests, lint and vet use synthetic subprocesses, fake generation and
temporary encrypted fixtures; no actual Apple generation or user/device data.
The existing 45-second request deadline still includes admission. Sustained
interactive traffic can cause a visible normal admission timeout; it is not
silently retried as model preemption. Same-class admission order is unspecified;
interactive waiters take precedence over background work.
