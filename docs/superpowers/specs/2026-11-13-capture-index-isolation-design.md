# Isolated encrypted search trees for capture parts

## Intent and current evidence

Long captures need searchable ordered parts before they can feed Search/Ask/MCP.
The encrypted capture repository exists, but production still saves observations;
no complete long-page recall or actual Library startup recovery is proved.
The previous goal turn renamed the saved-content model to captures and verified
its unchanged encrypted format. That was concrete progress. The latest startup
trace confirms the existing recovery route; the actual runtime diagnostic is
still pending, so no guessed storage fix is part of this work.

## Approach and alternatives

Reuse the existing bounded encrypted exact posting trees with an immutable
private capture scope. Duplicating the tree would duplicate complex split,
budget and erasure logic. Mixing part IDs into the observation index would make
existing hydration and time scope treat parts as independent observations.
A separate scope lets later capture-aware hydration retain edition identity.
This implementation choice follows the user's autonomous execution instruction.

The default observation scope keeps every existing physical key, HMAC domain,
authenticated record namespace, format version and migration byte unchanged.
Capture search uses physical group 0x41 (observations remain 0x40), distinct HMAC
domains and record namespaces, including state, tree, reverse, pending, proof
and per-source pages. No raw content or unkeyed term appears in physical keys.

A private Store view shares the original storeState pointer, mutex, deletion
gate, codec, manifest keys and lifetime. It never owns or closes a second DB.
Its scope is immutable. Writes use a searchIndexTx facade embedding the original
*Tx, with a separately scoped Store view. Do not copy a Tx, its mutex or manifest,
or temporarily mutate a shared Store/Tx scope. The facade forwards physical
mutations to the same live transaction and manifest. Existing Tx methods remain
observation-compatible wrappers. This permits later capture erasure to remove
parts and their postings atomically inside the existing journal.

Keep one implementation of ranking, tree splits, reverse references, bounded
reads, pending queues and cleanup. Every physical key and encrypted namespace
must follow the scope; clearing, corrupting or rebuilding one scope cannot
alter or validate the other. The shared deletion gate blocks both.

## Proof and delivery

Exercise real encrypted temporary databases: different search results in both
scopes, app/domain filters, over-64-posting tree splits, reverse removal,
independent pending receipts, rollback, reopen, scope clear, ciphertext replay
rejection, concurrent writes and shared deletion/closed state. Existing exact
search and erasure suites must remain green. This is not production capture
indexing: publication queues, live edition checks, repeated-visit time lookup,
bounded part index work/erasure, native ingestion, retrieval and native acceptance
remain required under the full seven-part design. Do not build a prerequisite
app or access the user's store/keys/settings. Keep the goal active.
