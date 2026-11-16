# Blur

Private, local-first memory for you.

[![CI](https://github.com/hazcod/blur/actions/workflows/ci.yml/badge.svg)](https://github.com/hazcod/blur/actions/workflows/ci.yml)

Blur is an open-source, cross-platform personal memory app. It keeps a searchable record of your day and answers questions about it. Your memory stays encrypted on your device, and a local planner selects the few items a question needs before a model sees them.

[Website](https://blur.hazcod.com) · [Getting started](docs/getting-started.md) · [Contributing](CONTRIBUTING.md) · [Platform status](docs/project-status.md)

Blur is development software. Windows and Linux have native capture implementations and portable builds, with live desktop validation still in progress.

| Platform | Architectures | Native text capture | Status |
| --- | --- | --- | --- |
| macOS | Apple silicon and Intel | Accessibility | Native desktop app |
| Windows | x64 | UI Automation | Experimental; live runtime validation pending |
| Linux | x64 and arm64 | AT-SPI2 on X11 | Experimental; live desktop validation pending |

![Blur's Today page with a recap of the day, next steps and notes](docs/images/today.png)

![Blur's quick ask panel answering a question from memory](docs/images/quick-ask.png)

## What Blur does

Blur reads text from the focused app through your operating system's accessibility APIs, stores it in a local encrypted database, and lets you search it or ask questions about it. Answers say where they came from. It never takes screenshots.

- Today: a recap of your day, next steps and notes.
- Ask: questions answered from your memory. General questions never send captured data.
- Correlate: bring related notes, conversations and decisions together in an answer, with sources from across your apps.
- Reminders and follow-ups: optional AI turns your notes into suggested next steps; you review and confirm the ones you want to keep.
- Search: find saved content by words or meaning.
- Library: facts about people and projects that you review and keep.
- Time: where your time went, per app and per range.
- Quick ask: a panel under the notch for fast questions and notes.
- Browser capture: active page text through macOS Accessibility, with an optional Safari extension for additional page capture.

## Why not just let AI read everything?

Most memory tools send your history to a model and let it work things out. Blur works the other way round.

- AI is optional and off by default. Use a local Ollama model or an endpoint you configure. Sending memory evidence to a remote endpoint requires a separate opt-in; that provider may retain requests.
- A local planner reads your question, not a model. It is deterministic. Time questions and questions like "what did you capture from WhatsApp" are answered with no model at all.
- The model only gets a handful of items, selected and size-limited each time, never your whole memory. Blur shows how many items were sent with each answer.
- Daily recaps are built from the short hourly summaries Blur already wrote, so they are a few kilobytes, not hundreds of captures.
- You decide what is captured with allowed and blocked apps and sites, and with timed pauses. You decide how long it is kept, and you can delete all of it.
- Optional folder sync writes encrypted change files to a provider you choose. Enabling MCP lets your client read selected memory results; a hosted client may send those results to its model provider.

## Install

Start with the [getting started guide](docs/getting-started.md) for platform requirements, development builds and first-run setup.

Download builds from [GitHub Releases](https://github.com/hazcod/blur/releases). Windows and Linux archives are unsigned development builds. For a build of a specific commit, the [CI workflow](https://github.com/hazcod/blur/actions/workflows/ci.yml) also uploads portable archives as the `blur-portable` artifact after packaging succeeds; CI artifacts expire after 14 days and require a GitHub sign-in.

## Using Blur

- Capture: in Settings you choose which apps and sites Blur may capture and which it must never capture.
- Capture coverage: Today and Capture settings show the last readable content and any permission, page, domain, timeout or storage problem. On macOS, a periodic refresh catches content that loads after focus; capture rests after three minutes without input.
- Quick ask: open the panel by hovering the notch, from the link in the window, or with Option+Space (the default, configurable in Settings). Launching Blur leaves the panel closed. Enter submits your request, Command+Enter saves a note, and Esc closes the panel. With an empty composer, Up/Down selects an answer and Command+C copies it. The menu icon reflects capture On/Paused; its menu separately reports when the current app is not being captured.
- Today page: your recap, next steps and notes in one place.
- Ask page: ask questions about your memory and see the sources behind each answer. Follow-ups retain the subject and check current evidence; rewriting an answer does not save a note.
- Day and week recaps distribute evidence across the requested period, with dated summaries and captures from gaps. Model context is shared across those sources; the answer shows when selection or excerpts limit coverage.
- Storage errors: startup failures show a recovery reason and copyable diagnostics for storage-folder, database, and Keychain access.
- Pause: use the menu bar item to pause capture for 15 minutes, 1 hour or until tomorrow.

## Optional AI

New, unconfigured macOS installs enable Apple’s on-device system model and automatic Library by default. Existing saved AI choices remain intact. Settings → AI detects Apple Intelligence availability and installed Ollama models, separating answer-capable models from embedding models. If Apple Intelligence is unavailable, Settings explains its state; no cloud provider is selected automatically. Apple’s system model stays on-device and is managed by macOS; Blur does not claim a selectable AFM variant.

Library searches names, aliases, facts, and open loops across the complete encrypted directory, with category filters and 20-entry pages. Preferences provides global text/context search, type/status filters, paging, editing, pause, and deletion. Background indexing preserves Library browsing state. Natural commitments such as “I need to go to the drycleaners on Monday” prepare a reminder and ask for a missing time before scheduling it.

Ollama processing verifies that the selected model is installed locally before sending notes, questions, or captured evidence. Ollama cloud models are refused; remote processing uses the separately configured provider and consent controls.

With an answer model enabled, you get Ask answers and optional local hourly recaps. Without one, Blur still captures, stores and searches exact terms, and shows a model-free "This hour so far" draft. Library and Time do not need an answer model.

Hourly recaps can use bounded excerpts from large saved sources. They disclose excerpted, partial, or omitted evidence. This does not imply that Accessibility capture has read an entire page beyond its capture limit.

**Search by meaning** is enabled by default for new settings. It uses the installed local Ollama embedding model `embeddinggemma`; if needed, install it with `ollama pull embeddinggemma`. An existing choice to disable semantic indexing is preserved. Apple Foundation Models can write answers while EmbeddingGemma retrieves their evidence. Blur backfills saved captures and notes, indexes new content automatically, and combines semantic matches with exact terms in Search, Ask retrieval, and the MCP bridge. Current tasks and reminders are also ranked by meaning, using their live titles, state, schedules, and revision-bound action references. The settings show coverage and paused/unavailable states; an unavailable semantic provider is disclosed rather than treated as proof that no memory was saved. Vectors and source metadata are encrypted in the memory database; the search graph stays in RAM. Cloud embedding models and remote embedding endpoints are refused. Turning it off stops semantic processing and keeps exact search available; deleting sources also deletes their vectors.

### Cloud connections

Settings → AI supports the official **Continue with ChatGPT** account flow and a **Claude API** key connection. Connecting an account does not select it for inference. Choose an account and an available model, save the choice, and separately allow sending saved memory or processing notes. Changing the account, model, destination, workspace, or API key clears those permissions. Background recaps and automatic Library processing remain local.

ChatGPT uses its documented public Sign in with ChatGPT and Responses APIs, with credentials in the OS secret store. The current plan API does not offer a per-request hard output-token cap; Blur limits accepted output bytes and request duration. Claude uses Anthropic’s native Messages API and a hard output-token limit. A Claude consumer subscription is not a generic third-party API login. Search embeddings remain local for every answer provider.

## Sync across Macs

Blur syncs between your own Macs through a folder carried by iCloud Drive or Dropbox. It writes encrypted, append-only segments protected by a passphrase into the folder you pick. The database itself is never synced, and deletions propagate to your other Macs.

## Claude and other MCP clients

Once enabled in Settings, Connections, `blur mcp` exposes memory search locally to Claude and other MCP clients. It is off by default.

## Build from source

```sh
git clone https://github.com/hazcod/blur.git && cd blur
make macos-app-stable
```

The macOS app lands at `~/Downloads/Blur.app`. On Windows and Linux, `make portable-packages` creates the portable archives under `dist/packages/`. Use the Go toolchain version in [go.mod](go.mod). See [CONTRIBUTING.md](CONTRIBUTING.md) for the development workflow, [docs/releasing.md](docs/releasing.md) for releases and [docs/architecture.md](docs/architecture.md) for the design.

## Status, security and licence

- Status: Blur is in development. See [docs/project-status.md](docs/project-status.md) for platform status and technical detail.
- Security: see [SECURITY.md](SECURITY.md) to report a vulnerability.
- Licence: Apache-2.0, see [LICENSE](LICENSE).
