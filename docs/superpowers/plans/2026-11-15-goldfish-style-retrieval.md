# Implementation plan: Goldfish-style retrieval

1. Trace current planner, exact search, optional semantic index, source filters and answer citation validation.
2. Add a focused test that exposes serial query-rewrite plus passage-rank calls on a lexical miss.
3. Keep the bounded on-device query-planning call miss-only, add a majority-term retry for short topic queries, and require the explicit relevance pass for all expanded Search and Ask candidates before final answer generation.
4. Preserve immediate exact-match behavior, time/app scope, memory epochs, deletion checks and incomplete-coverage reporting.
5. Add regression coverage for phrase variants, unrelated passages, login chrome, malformed plans, duplicates, and query/model failures.
6. Run focused and package tests, inspect diff for scope/privacy regressions, build the canonical signed macOS app from the active worktree, and verify bundle/signature without launching it.
