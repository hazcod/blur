# Blur replication folder (simple sync) — design

Date: 2026-11-11. Status: approved in chat ("keep it simple"). Scope: phase one.

## Intent

Let one person's Blur memory follow them across their own Macs by continuously
exporting sealed, append-only change segments to a folder that any file-sync
service carries (iCloud Drive by default), and importing other devices'
segments on launch and on a timer. This also provides an encrypted backup.

## Why not sync the database or a WAL

The store is a single encrypted bbolt file with an exclusive lock and memory
mapping; syncing it produces conflict copies and corruption, and the device
key never leaves the Keychain. SQLite's WAL (and bbolt's equivalent) is a
local crash-recovery log that is checkpointed and truncated, not a replication
feed. Blur therefore writes its own log.

## Goals / non-goals

- Goals: no conflicts by construction; captured text never in plaintext
  outside the store; content-free file names; idempotent import; deletions
  propagate; works offline; provider-neutral folder.
- Non-goals (phase one): real-time sync, merge of derived views, CloudKit,
  physical erasure on the provider, multi-user sharing.

## Layout

```
<folder>/
  blur-replica.json              # format version, created-by device id
  <device-id>/                   # one subfolder per writer, UUID v4
    000000000001.seg             # sequence-numbered sealed segments
    000000000002.seg
```

A device only ever writes inside its own subfolder. Segment names carry only
a sequence number. Files are written to a temp name and renamed into place.
Files whose name starts with `.` (iCloud placeholders such as `.x.seg.icloud`)
are skipped.

## Segment contents

Plaintext is a versioned, length-prefixed list of records: observation
records exactly as the durable store's record codec encodes them (already
sanitized and redacted), user notes, and tombstones (`source_id`, deleted
at). Plaintext is padded to the next 64 KiB class, then sealed with
AES-256-GCM (stdlib) using a random 96-bit nonce; the header
(`version`, `device-id`, `seq`, `nonce`) is authenticated as additional
data. Segment plaintext is capped at 16 MiB.

## Key

The sync key is derived from a user passphrase with PBKDF2-HMAC-SHA256
(600,000 iterations, 16-byte random salt stored in `blur-replica.json`).
The derived key is kept in the device Keychain so the passphrase is typed
once per device. It is never the device store key. Wrong passphrase or any
tampering fails closed (GCM tag mismatch) and is logged content-free.

## Writer

A periodic job (every 10 minutes while capture runs, plus at pause and quit)
scans the store for observations, notes, and tombstones with a capture or
change time after the last exported watermark, writes one segment, then
advances the watermark (persisted in preferences). Nothing is exported while
memory is changing (existing fence). Bounds: one segment per flush, 16 MiB.

## Importer

On launch and every 10 minutes, list other devices' subfolders, import
segments with a sequence above the per-device watermark (at most 200 per
run), verify and decrypt, dedupe observations by content ID, write through the
normal persistence path so indexes, activity, and derived views update, and
apply tombstones through the deletion service. Import is idempotent.

## Privacy UI

Privacy page, group "Sync across your Macs": on/off, "Choose folder…" (a
system folder picker; the app is sandboxed, so the chosen folder is kept as a
security-scoped bookmark and the picker opens on iCloud Drive by default),
passphrase (set once), status line ("Last backup 10:42. 2 other devices."),
flat "Import now". Writer and importer state (device id, watermarks,
bookmark, enabled flag) lives in `replica/state.json` inside the app's data
directory, not in preferences.

## Threat notes

iCloud Drive is not end-to-end encrypted without Advanced Data Protection;
sealing is client-side. File sizes and cadence leak coarse activity rhythm;
padding and a fixed flush cadence blunt it. Deleting a segment on the
provider does not guarantee erasure. The provider sees device count.

## Verification

Round trip two in-memory stores through a temp folder: observations, a note
and a deletion propagate; replay is idempotent; tampered and wrong-key
segments are rejected; placeholder files are skipped; padding classes hold.
