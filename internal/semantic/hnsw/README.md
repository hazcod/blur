# In-memory HNSW core

`graph.go` and `heap/heap.go` are adapted from [coder/hnsw](https://github.com/coder/hnsw/tree/36cab6028fed4adc9c3edf2323a06f0a95c1f030), commit `36cab6028fed4adc9c3edf2323a06f0a95c1f030` (2026-06-22), under CC0-1.0. The complete upstream license is in `LICENSE`.

Blur includes only the graph and heap. The upstream package's file persistence imports renameio, which does not compile on Windows. There is deliberately no graph export, import, or plaintext file API here. Encrypted source-linked vector records are the durable authority.

Changes: the heap import is local; `maps.Keys` uses the standard-library iterator plus `slices.Collect`; cosine distance is a portable Go implementation without native assembly dependencies. The selected upstream revision includes fixes for empty layers after deletion, heap maxima, and search exploration/ordering. Blur's `semantic.Index` supplies synchronization, source provenance, input limits, cancellation, and cache invalidation.
