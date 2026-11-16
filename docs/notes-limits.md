# Pasted notes and processing limits

User-authored notes can contain up to **1 MiB (1,048,576 UTF-8 bytes)**. `notes.MaxNoteBytes` uses `capture.MaxObservationTextBytes`, the authenticated observation storage bound. This is distinct from a capture profile's smaller default collection limit.

An explicit note request, such as `Save a note:` or `Save these meeting notes:`, removes its request prefix and preserves the original body. Confident multiline meeting pastes are recognized locally from independent document signals such as attendees, decisions, action items, or transcript structure. Raw pasted note bytes, including line endings and surrounding whitespace, are preserved. The existing secret redactor still applies before storage.

Enter, Command-S and Command-Return use the same source-preservation contract. Forced saves keep the full draft, or the body after an explicit note request. Today normalizes its derived preview separately, so short notes with a trailing newline can be saved without changing the authoritative source.

Quoted reminders, named speakers' actions, Markdown checkboxes and dated agenda entries remain part of one source note. Extraction may produce source-linked suggestions, but they remain unconfirmed. Saving a paste does not complete tasks, activate reminders, send messages or execute embedded instructions. A new note paste also takes precedence over an unfinished reminder's time question.

The limit applies to the complete source, not the preview. The encrypted source keeps the full accepted note; Today and search previews remain bounded. Notes beyond the limit are rejected before storage. If secret redaction would expand the note beyond its bound, the save is rejected rather than silently truncating the source. Failed note saves retain the complete composer text.

Model processing has a separate budget. A local extraction model may decline a note that was successfully stored, reporting `model_budget_exceeded`; deterministic explicit TODO suggestions can still remain available and unconfirmed. This does not mean that the model processed the full note or that large-paste summarization is implemented.

Leading requests to summarize, extract, translate, rewrite or otherwise analyze pasted content keep their analysis intent. The local paste route admits analysis requests up to the client query bound of **512 bytes**; longer requests receive a clarification explaining that the paste is too large to analyze in one request. They are never silently saved as notes. Short analysis requests use the selected answer provider's normal availability and consent rules. The coverage catalog contains additional model-dependent analysis examples; catalog labels alone do not establish end-to-end support.

Targeted regression checks:

```sh
go test ./internal/client ./internal/notes ./internal/storage/durable -run '^TestMeetingPaste' -count=1
go test ./internal/client -run '^TestQuickPanelMeetingNotesPreserveBytesAcrossSaveGestures$' -count=1
```

The durable-store test writes a synthetic note at the full 1 MiB limit to a temporary encrypted store, reopens it, checks exact source bytes and retrieval of its final decision, and verifies that a real budget-limited local extractor makes no network request. The tests use no user store, user keys, cloud service, or running app.
