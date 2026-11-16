# Goldfish-style retrieval for Blur

Date: 2026-11-15

## Evidence and limits

The installed Goldfish daemon (0.1.70) exposes independent full-text indexes for raw captures, topic entries, chronicle summaries, Wiki pages and facts, plus a local `potion-base-32M` model2vec/HNSW path. It also contains a source assessment prompt that labels observed user actions separately from visible page content, and grades claims as core/detail/noise. Its content path distinguishes original capture text from normalized `body` text and maintains surface-scoped boilerplate statistics. Static inspection did not recover the complete runtime query router or ranking weights, so this design copies verified architectural ideas rather than claiming source-level parity.

## Goal

Make Ask retrieve useful personal history from natural language quickly, across paraphrases and generic questions, while keeping original encrypted captures authoritative and every answer grounded in source provenance.

## Design

1. **Question understanding:** Keep the deterministic planner as the zero-latency first pass for time, app, entity and task intent. On a lexical miss, the configured on-device text model produces a bounded retrieval query plan (short topic phrases, corrected spellings and likely aliases). This call never receives capture content. A separate relevance pass checks every candidate passage before it can be returned as Search or Ask evidence. The final answer model then writes only from this filtered evidence.
2. **Retrieval:** Search the encrypted exact index using the original terms and bounded model-generated variants. Merge/deduplicate by source ID, preserve app/time filters, relax a failed multi-term AND query to a majority-term pass, rank candidates, and fetch only a bounded number of candidate excerpts. Existing source-linked semantic retrieval remains an optional internal index, not a second user-selected answer model.
3. **Content quality:** Keep raw capture evidence immutable. Existing authentication-page exclusion remains a targeted guard. Future background normalization/assessment must be versioned, reversible, surface-scoped, and source-linked; it must distinguish what the user did from what was merely visible and must never delete the original evidence.
4. **Answering:** One final selected-model call receives bounded candidate evidence. The app attaches canonical source IDs and rejects unsupported answers. Absence remains provisional when indexing or optional retrieval coverage is incomplete.
5. **Privacy and lifecycle:** Query plans contain only the user's query and are ephemeral. Search remains within the local encrypted source store. Deletion, memory-read epochs and current-source checks continue to fence candidates.

## Why not call Apple first on every question?

A separate Apple Foundation Models parse call before every search would add model latency even for obvious exact matches. Goldfish's inspected architecture uses indexed retrieval layers and a compact embedding model; available evidence does not show that it asks a generative model to parse every query first. Blur should keep Apple off the exact-match fast path and invoke its constrained query reformulator only on a miss. The on-device query rewrite remains miss-only. Ask then makes one final generation that both evaluates the retrieved passages and answers from the ones it can support; only cited source IDs are returned to the UI. Search keeps a dedicated relevance pass because it has no answer generation to perform that selection.

## Acceptance criteria

- Exact matches remain immediate and cause no planning/ranking model call.
- On a miss, one constrained model call can generate bounded query variants; variants retrieve candidates through the existing encrypted index.
- Ask misses use final-answer relevance selection and expose only cited sources; exact hits retain the dedicated relevance gate.
- Search and Ask never treat generated query text as evidence.
- Empty results still expose indexing/coverage limits.
- Tests cover paraphrase variants, misspellings, noisy login captures, duplicate candidates, filters, bounds, cancellation, and model failure.
