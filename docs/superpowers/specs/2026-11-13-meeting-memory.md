# Meeting memory

## Intent

Make meetings part of Blur's searchable personal memory. A user can write an
agenda, start transcription, take notes while listening, stop, and return to
what was said. Typed notes, finalized transcript and derived summaries have
distinct provenance. Starting Blur or ordinary screen capture never starts audio.

The user requested parallel implementation and delegated routine architecture
choices. This document records implementation choices, not a claim of live
recording acceptance.

## Evidence and approaches

- [Yogurt source](https://github.com/FrigadeHQ/yogurt/tree/41c936ae83697b2894f52746dc9c24cb16bab474)
  separates microphone and system audio, streams partial transcription, and keeps
  partial results out of saved transcripts. It uses whisper.cpp for saved text
  and Apple SpeechAnalyzer for live words on supported Macs. Study its bounded
  chunking and device-change recovery; do not copy its plaintext recording store.
- [Notion's documented meeting flow](https://www.notion.com/help/ai-meeting-notes)
  places manual notes beside transcription and uses both for a summary after
  Stop. It offers a recording action when a call starts. Its documented
  transcription is remote, and its internal capture implementation is unknown.
  Adopt the note/transcript relationship and explicit controls for Blur.
- Installed Goldfish's signed public main executable links Core Audio input/output
  APIs. This establishes audio capability, not its speech model or transcription
  architecture. No private Goldfish store was inspected.
- [Apple SpeechAnalyzer](https://developer.apple.com/documentation/speech/speechanalyzer.md)
  supports local streaming transcription on macOS 26+. Apple Foundation Models is
  a separate text-generation API, not an audio transcriber.

For the current macOS app, prefer Apple SpeechAnalyzer with separate microphone
and system-audio tracks. Keep a provider interface for a future local whisper.cpp
fallback. A remote ASR default would add a new data-disclosure setting; a large
Whisper dependency/download is unnecessary for the first supported-Mac path.

## Contract

- Start, Pause, Resume and Stop are explicit session operations. Display the
  actual state; errors must not look like recording success. Permission checks
  are read-only until an explicit user action requests access.
- Use bounded audio queues and release PCM after consumption. No raw recording
  files or audio uploads by default. No active recording during tests.
- Native events identify session, track, segment, time range, revision and final
  status. Separate You/Others tracks do not establish the names of all speakers.
- Volatile text can change; only finals become durable evidence. Duplicate finals
  are idempotent. Conflicting revisions of finalized evidence fail visibly.
- Apple does not guarantee a volatile result will be emitted again with its
  `isFinal` flag set. Use the documented final-only companion module for durable
  results, with preview identities separate from immutable final segment IDs.
  Share the bounded per-track analyzer/input pipeline; do not assume shared
  inference cost without measurement. Preserve native time ranges. A preview is
  not constrained to the engine's optional stable-span partial-event contract.
  See [Apple's finalization contract](https://developer.apple.com/documentation/speech/speechmoduleresult/resultsfinalizationtime.md).
- Pause/Stop invalidate input epochs so late native callbacks cannot add text.
  Stop joins capture and drains valid final results before completing.
- Persistence uses an explicit encrypted sink. Never store plaintext transcripts
  in a sidecar, log, crash report or model prompt cache owned by Blur.
- Persist bounded final batches incrementally; never rewrite the full transcript
  for every partial. Preserve exact source/time references for derived summaries.
- Manual notes remain separate user-authored evidence. Meeting prose is data;
  action-item extraction creates proposals, not reminders or completed tasks.
- Model work is optional and follows the selected model's existing local/cloud
  policy. Summaries receive bounded new material; absence of a model cannot
  prevent saving transcript and notes.
- Text-only pasted meeting notes are already supported independently, up to 1 MiB.
  Their storage bound does not imply equally large one-shot analysis support.

## First implementation slice

Build and test a provider-neutral meeting session engine and a separately gated
native macOS audio/transcription bridge. Do not expose nonfunctional recording UI
or silently enable audio. Native availability, asset readiness, bounded event
delivery and lifecycle need acceptance before connecting the app's controls.

Follow with encrypted durable adapters, app lifecycle/controls, source-linked
summaries and Ask integration. Live device/permission acceptance is still required;
synthetic state-machine tests do not prove real call capture.
