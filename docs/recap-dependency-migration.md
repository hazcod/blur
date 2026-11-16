# Recap dependency cleanup

Older generated hourly recaps recorded their displayed citations, which could
be a subset of the sources supplied to the model. The remaining input identities
cannot be recovered reliably from the recap text or its input digest. Source
deletion therefore cannot safely preserve those old generated recaps.

On the first open of an existing encrypted store without the completed cleanup
marker, Blur removes generated recaps lacking `InputSourceIDs` before returning
an open store. A second pass removes verified generated Today questions left by
old recap replacement or deletion, including questions whose recap is already
absent. Questions belonging to a current recap are retained only when their
exact generated identity and complete input dependencies still match. Other
Today rows, original captured text, user-authored notes and authored answers are
preserved, including their identities, text and capture timestamps.

Normal recap recovery can generate replacements from the retained raw sources
when automatic recaps, the selected local model and background policy permit it.
Cleanup itself performs no inference, network requests or capture. It does not
guarantee that every removed recap will be regenerated.

## Bounds and restart behavior

The marker and stage cursors are encrypted and authenticated through the
existing storage metadata mechanism. This adds no schema version, metadata
record or alternate store. Fresh stores initialize the version-2 marker as
complete. A version-1 completion marker still requires the question pass once.

The startup pass loads the bounded day directory once, reads at most 24 hour IDs
per day and processes one summary per write transaction. Each removal and cursor
advance commit atomically. The question pass scans up to 32 physical extension
records or 2 MiB per transaction. Namespaces are hidden by authenticated keys,
so it must inspect all extension records once; only authenticated Today records
are interpreted. An individually larger extension is read alone under an 8 MiB
scan bound. Each changed snapshot and physical cursor commit together. The
existing transaction operation and byte limits remain unchanged.

A failed open returns no store; reopening resumes the committed stage and
prefix, even when cleanup removed the hour cursor's entire day. Completion is
committed only after both passes. Malformed migration metadata fails closed.

The pass runs synchronously before the store is published. Its memory and each
transaction are bounded; total one-time startup work depends on recap history
and the number and size of extension records. After completion, ordinary reopen
checks the marker without scanning history or rewriting the database.
