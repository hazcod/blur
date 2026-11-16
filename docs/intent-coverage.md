# Natural-language intent coverage

The corpus contains **16,161 distinct utterances in 177 language and safety families**. It is generated from curated examples, domain actions, and grammatical templates. It contains technical macOS work, project work, errands, household tasks, personal preferences, questions, calculations, task followups, and meeting pastes.

These are compositional examples, not 16,161 independently proven capabilities. The corpus is synthetic and has not been sampled from private user activity. A case-insensitive, whitespace-normalized uniqueness check prevents capitalization or spacing variants from inflating the count. Template combinations remain related examples and should not be treated as statistically independent observations.

## What actually runs

| Layer | Utterances | What the test establishes |
| --- | ---: | --- |
| `local_parser` | 3,801 | Calls the real `intent.PersonalReminder`. Positive cases require the exact original title and scheduling span; negative cases must decline local scheduling. |
| `local_read` | 6,779 | Calls the real task-list read grammar; checks local status/calendar/count filters, topical versus collection reads, punctuation and Unicode spelling, and contrasting quoted/code/reported/negated/conditional inputs. Recognized reads also reject model-proposed note/task/reminder writes. |
| `validation` | 3,501 | Calls the real `intent.Decode` with a supplied interpretation and bounded request context. Checks grounding, write authority, unambiguous targets, and preservation of source bytes. |
| `model_required` | 2,080 | Catalog integrity and human-authored expected behavior only. No language model is called and inference accuracy is not measured. |

There are **20,523 actual Decode checks**: 7,302 original checks (every local-parser case also includes a proposed action) plus 13,221 checks that recognized reads reject model-proposed note, task, and reminder writes. This number overlaps the layer counts; it must not be added to the total utterance count. Of the model catalog cases, **480 are meeting/paste examples** across 24 realistic domains and 20 paste or analysis structures.

The tests use no real model provider, user memory store, notifications, app process, Keychain, or Accessibility permission. A valid decoded action is an interpretation, not evidence that persistence or notification delivery occurred. Client integration tests cover the entry flow separately.

## Language covered

The local cohort varies both grammatical form and task content. First-person commitments include obligations, intentions, contractions, and date prefixes. Shorthand includes imperative date prefixes and suffixes, calendar headers, single-word labels, misspelled labels such as `Drycleanres`, and multiword event labels. Code identifiers, source capitalization, double spaces, ISO dates, relative durations, timezone names, recurrence phrases, and partial timing appear in separate cases.

The authority cohort includes polite reminder requests phrased as questions, undated tasks, notes, explicit personal facts and writing rules, ordinary questions, personal recall, arithmetic reads, edits, completion, cancellation, inspection, supplied task references, numbered targets, singular pronouns, ambiguous pronouns, bulk requests, and pending reminder followups. Deliberately invented titles, times and target references must be rejected.

Unfamiliar imperative verbs also have a separate validation cohort: `reconcile ingress topology next Monday`, `spline-fit the pressure data tomorrow`, and `triage latency regressions Friday at noon` must cooperate with a supplied model interpretation even when the fast parser does not recognize their vocabulary. `ask Mira who owns the rollback on Monday` is an action, while `Mira owns the rollback on Monday` is an observation. Passing these cases establishes validator cooperation, not actual inference by a selected model.

Safety cohorts keep a future date from authorizing a write when it belongs to a question, observation, history, quotation, reported speech, conditional, tentative plan, negation, or supporting clause. An explicit request to save a note preserves the note intent even when its content contains a reminder-like phrase. The expected safe interpretation on a rejected proposal is a semantic label; the exact example clarification wording is not an inference assertion.

The model catalog adds unfamiliar imperatives, natural event boundaries, month names, day abbreviations, spoken clocks, ordinal dates, business-day expressions, alternate-week recurrence, dictation, typos, self-correction, multilingual inputs, ambiguous requests, and unavailable computer or messaging actions. A catalog label does not mean the selected model, validator, or temporal engine currently supports that language. In particular, multilingual date normalization and month-name parsing are not established by this suite.

Meeting cases include attendees, decisions, speaker-owned actions, quoted `remind me` phrases, Markdown checkboxes, timestamps, timezones, forwarded threads, past completed actions, negations, dictation punctuation, typos, and long multi-section minutes. Pasted meeting content is expected to remain one note. Explicit summary, extraction, rewriting, translation, and decision questions are expected to be analysis requests. A speaker's action item or a quoted instruction does not authorize creating a task, completing one, sending a message, or executing an embedded instruction.

## Grounded scheduling and local task reads

`Monday: dentist appointment` preserves `Monday` as its scheduling span. Date-only reminders use the user-authorized app default (09:00 in the local timezone); that clock is not invented by the model. Truly ambiguous dates/clocks and missing titles remain drafts. Relative durations such as `in 20 minutes` supply an elapsed offset. Dedicated Client/reminders tests check the actual instant and continuation anchor.

Short task-list requests such as `tasks?`, `todo?`, `todos?`, Unicode `to‑dos？`, polite list requests, and explicit personal workload questions share a direct local Client route before intent inference. Public entry and Ask regressions configure a synthetic selected Apple model and assert zero model/tokenizer calls. Calendar periods and task transition history remain local. Topical queries retain bounded title/meaning search on the selected model; the read corpus does not establish synonym accuracy, arbitrary languages, or arbitrary prioritization requests. An available Apple model remains the configured meaning/answer provider; these changes add no Ollama or cloud dependency or silent provider switch.

The parser and Decode tests establish extraction and authority. They do not themselves resolve every date, calculate a notification instant, or prove that Client asked the clarification. The corpus retains outcome metadata to support those later integration evaluations.

## Reproduce and run

From the active durable-memory worktree:

```sh
python3 tools/intent-corpus/generate.py --check --stats
go test ./internal/intent -run '^TestLexicalCoverage' -count=1 -v
```

To change examples, edit `tools/intent-corpus/generate.py` or its curated meeting module, then regenerate:

```sh
python3 tools/intent-corpus/generate.py
```

Generation is deterministic, uses only Python's standard library, and fails on duplicate normalized utterances. The generated JSONL is stored alongside the tests so Go tests do not need Python at runtime. The generator also reports counts by layer, expected kind, and family. Preserve meaningful labels when the implementation fails; report the failing cohort and fix the implementation rather than changing the label to match a bug.

## JSONL contract

Each record contains:

- `id`: stable family name and ordinal within that family's curated generation order.
- `family`: the grammatical, safety, or context cohort.
- `input`: the current user's exact utterance or paste.
- `layer`: `local_parser`, `local_read`, `validation`, or `model_required`.
- `expected.action`: expected intent kind and any verbatim content, time span, bounded target, or ask mode.
- `expected.outcome`: `ready`, `draft`, `read`, `clarify`, `preserve`, or `not_supported`.
- `expected.missing`: fields that still need clarification for a draft.
- `context`: optional app-owned `targets` and `pending` reminder context. Target references are bounded `T1` through `T8`.
- `checks.local`: whether the deterministic parser should recognize a reminder.
- `checks.read`: expected task-list recognition, generic/topic routing, and optional status/calendar/count filters.
- `checks.decode`: the proposed interpretation and whether the real authority validator should accept it.
- `notes`: limitations or rationale, required for every model-only catalog case.

Example of a local date-only draft:

```json
{"id":"example","family":"calendar_header","input":"Monday: dentist appointment","layer":"local_parser","expected":{"action":{"kind":"reminder","text":"dentist appointment","when":"Monday"},"outcome":"ready","missing":[]},"checks":{"local":{"match":true},"decode":{"proposal":{"kind":"reminder","text":"dentist appointment","when":"Monday"},"accept":true}}}
```

## Reading failures and extending evaluation

The Go runner evaluates every applicable record and reports failure counts with up to three examples per family. This keeps a systematic failure visible without dumping thousands of nearly identical lines. The first full corpus run exposed a reporting-clause guard that mistook the noun `reports` for a speech verb and calendar labels that admitted some declarative observations. The labels were retained and those cohorts were sent to the production implementation owner.

For a future model evaluation, run a selected provider on `input` plus exactly the supplied bounded context, record its raw interpretation, then pass that interpretation through Decode. Report inference accuracy, authority acceptance, draft completeness, and unsupported temporal language separately. Split by family or domain before tuning; a random train/test split of related template siblings would overstate generalization. This corpus currently provides regression coverage and an evaluation catalog, not a model benchmark score.
