# Retokenize retained published captures

Older published capture parts retain format-4 tokens, so accent-folded queries
can miss saved large-page content indefinitely. Opening should authenticate and
queue a durable cursor, then return without scanning or tokenizing payloads.
The background worker will reset one edition's derived progress, remove each
old part's posting proofs in existing bounded batches, and rebuild that part
from its authenticated retained bytes. Add content-format fields to the
backfill, edition progress and part mapping; preserve their metadata versions,
source IDs, publication IDs, payload ciphertext and visit provenance.

Deletion must remain safe before the cursor, during proof removal and during
rebuilding. Reopening must resume, and current editions/new visits must avoid
unnecessary rewriting. Pending coverage stays visible until the cursor and
queue finish. Schema 7 and the original signing identity remain unchanged.

- [x] Reproduce genuine old-token misses with authenticated fixture proofs.
- [x] Implement bounded, restart-safe migration and deletion maintenance.
- [x] Verify dense and partially indexed parts, source preservation, restart,
  cursor deletion, new arrivals and stable reopening.
- [x] Review, run relevant regressions/full Go checks, and build the canonical
  signed app with `make macos-app-stable`.

Focused migration/publication/deletion/ready-reopen regressions pass. Review
found an intermediate mapping gap: a retained citation could become unreadable
and deletion through its part ID could leave the source intact. The permanent
regression reproduced both failures. Atomic replacement with an empty current
reverse root and preserved membership fixes it; raw evidence, sighting times
and resolution are checked after every work quantum. The independent replay
and final focused suite pass. No remaining material review findings.

The canonical signed app was rebuilt and passed deep/strict verification;
the Makefile preserved the exact existing certificate and designated
requirement. No app was launched or restarted. The final full suite passed (including durable storage, 527.688 seconds).
The later event-time recall change has its own verification plan.

The dynamic journey campaign remains a separate, measured mutation campaign;
its million-execution target is not a claim of a million unique user needs.
