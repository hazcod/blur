# Folded Library name lookup

This continues the recall parity plan and the recorded Library lookup gap.
The full personal-memory objective remains active.

The inferred Library currently hashes lowercase names, so querying `jose`
cannot discover an entry stored as `José`. Keep those identity keys unchanged
and add a secondary index over accent-folded names. Each posting identifies
one inferred entity; matching spellings never merge identities or provenance.
Use authenticated, keyed prefixes and encrypted values, bounded reads, and
one-entity migration transactions. Maintain postings atomically with entity
writes/deletion. A restart-safe cursor backfills existing entries through the
local indexing worker even when AI is disabled; Ask's existing indexing status
includes this work. Avoid rewriting sources or blocking store open on a scan.

Changing the primary identity normalization would invalidate existing IDs and
correction receipts. Scanning all Library bodies for each question would make
lookup cost grow with memory. The secondary index avoids both.

- [x] Reproduce unaccented and decomposed-name failures through real retrieval.
- [x] Add the secondary index, write/delete maintenance and bounded backfill.
- [x] Verify legacy-store reopening, restart, context collisions, removal,
  output limits, and encrypted storage.
- [x] Connect the local indexing worker and pending-coverage reporting.
- [x] Run focused regressions, full Go tests/vet, and review the diff.
- [x] Update parity evidence and build `make macos-app-stable`; verify the
  canonical signature and preserve the existing requirement/certificate.

Independent review additionally found and verified fixes for posting-read
budget headroom, empty-Library pending status, and app/domain filtering before
the eligible-result limit (32 same-name contexts, target correction last).

User steering added a feedback-guided stateful journey runner with a target of
one million executions. It exercises actual storage and Client flows, saves
minimized failures and reports observed counts; see `docs/dynamic-memory-scenarios.md`.
An initial sighting-event oracle error was corrected; a later minimized sequence
found a real Find-after-delete routing gap. Recognized retrieval commands now
remain model-free lookups, while explicit general mode and ordinary factual
probes stay separate. Targeted regressions and current journey seeds pass.
The folded-name/routing full suite and vet passed. A later minimized journey
found a stored-record location variant of the routing gap; that and quoted
Search topics now have passing regressions. The long mutation campaign remains
an unfinished target. Final full verification after the additional capture-token
migration is running; neither the million target nor full objective is complete.

Boundaries: inferred Library names are the known failing path in this change.
The explicit Library's exact-name API retains its identity semantics. Universal
app acquisition, arbitrary query support and live native acceptance still need
separate evidence; this change cannot prove those requirements.
