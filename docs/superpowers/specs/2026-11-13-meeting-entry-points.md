# Usable meeting notes

## Required outcome

The user must be able to open meeting notes from Blur's menu bar or a global
keyboard shortcut and actually start/stop local transcription, take notes and
read/search the encrypted result. The earlier native bridge was not connected;
an entry point that only says the feature is coming would not satisfy this.

Use the existing native dashboard styling. The user has delegated implementation
choices and requested parallel work; do not add another design approval flow.

## Experience

- Menu bar: **Meeting notes…**, with the active recording state visible.
- Global **Control–Option–M** opens/focuses Meetings. Opening never starts audio.
  Keep Option–Space for Quick ask. Report shortcut conflicts in the app.
- The existing dashboard gains a Meetings destination. It has a primary
  **Start transcribing** action, explicit microphone/system-audio selection,
  language, manual notes, transcript and a paginated recent-meetings list.
- During recording show actual Starting/Recording/Paused/Stopping/error state;
  Pause/Resume and Stop remain easy to find. Hiding the window does not stop
  recording. Quit and memory deletion stop/drain it before closing the store.
- Request microphone/system permission only from an explicit user action.
  Availability checks run automatically. Missing speech assets have an explicit
  download action, never a silent download from launch or Probe.
- Final text is persisted incrementally in encrypted storage and immediately
  searchable through existing Search/Ask. Volatile previews stay in memory.
  User notes are separate evidence. Do not execute spoken action items.
- History and transcript are paginated; live UI keeps only a bounded tail.
  No frequent full-transcript snapshot/rewrite. Preserve a draft during refresh.
- Summaries use the selected provider's existing policy and bounded evidence;
  transcript saving never requires a summary/model. A partial summary must say
  what coverage was omitted. Retention/deletion invalidate transcript derivatives.

Sketch, within the existing dashboard:

    Meetings                              Control–Option–M
    [Start transcribing]  Microphone + system audio · English
    Notes                       Transcript
    [Type your agenda or notes] [Final words + live preview]
    Recent meetings · previous / next

## Architecture and acceptance

Keep the reviewed native bridge and session engine. Add an encrypted repository
adapter with restart-stable idempotence, source links and deletion fences;
an app controller that drains native finals before engine Stop; and native menu,
shortcut, permissions/assets and signed-library packaging.

Use only synthetic input and temporary encrypted stores during automated tests.
Do not record real audio, request actual permissions, download models or launch
the user's app through tools. Build the canonical signed Downloads app after
review and source checks; live device acceptance remains an explicit remaining
verification limitation until observed.

## Current date-input steering

In parallel, compact relative durations such as `in 5m` must resolve exactly.
Tomorrow/weekday defaults09:00; morning09:00, afternoon14:00, evening18:00,
tonight20:00. Explicit12/24h clock wins. Display dates/times with macOS locale and
clock preferences, retaining the intended local timezone and DST handling.
