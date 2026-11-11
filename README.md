# Blur

Private, local-first memory of what you work on.

[![CI](https://github.com/hazcod/blur/actions/workflows/ci.yml/badge.svg)](https://github.com/hazcod/blur/actions/workflows/ci.yml)

Blur is an open-source personal memory app for your desktop (macOS now, Windows and Linux in progress). It keeps a searchable record of your day and answers questions about it. The alternative to letting AI read everything: your memory stays encrypted on your Mac, a local planner picks the few items a question needs, and a model only ever sees those.

![Blur's Today page with a recap of the day, next steps and notes](docs/images/today.png)

![Blur's quick ask panel answering a question from memory](docs/images/quick-ask.png)

## What Blur does

Blur reads the text of the app you are focused on through macOS Accessibility, stores it in a local encrypted database, and lets you search it or ask questions about it. Answers say where they came from. It never takes screenshots.

- Today: a recap of your day, next steps and notes.
- Ask: questions answered from your memory. General questions never send captured data.
- Search: find anything you saw or wrote, by exact words.
- Library: facts about people and projects that you review and keep.
- Time: where your time went, per app and per range.
- Quick ask: a panel under the notch for fast questions and notes.
- Safari capture: optional page capture through a Safari extension.

## Why not just let AI read everything?

Most memory tools send your history to a model and let it work things out. Blur works the other way round.

- Nothing leaves your Mac unless you turn on a model. The model is a local Ollama model or an endpoint you configure.
- A local planner reads your question, not a model. It is deterministic. Time questions and questions like "what did you capture from WhatsApp" are answered with no model at all.
- The model only gets a handful of items, selected and size-limited each time, never your whole memory. Blur shows how many items were sent with each answer.
- Daily recaps are built from the short hourly summaries Blur already wrote, so they are a few kilobytes, not hundreds of captures.
- You decide what is captured with allowed and blocked apps and sites, and with timed pauses. You decide how long it is kept, and you can delete all of it.

## Install

Download the latest `Blur.app` from the [releases page](https://github.com/hazcod/blur/releases) and move it to Applications. macOS is the supported target today. Windows and Linux are in progress.

On first launch, grant Blur access in System Settings, Privacy & Security, Accessibility.

## Using Blur

- Capture: in Settings you choose which apps and sites Blur may capture and which it must never capture.
- Quick ask: open the panel by hovering the notch, from the link in the window, or with the keyboard shortcut you choose. Enter asks, Command+Enter saves a note, and Esc clears the field and then closes the panel.
- Today page: your recap, next steps and notes in one place.
- Ask page: ask questions about your memory and see the sources behind each answer.
- Pause: use the menu bar item to pause capture for 15 minutes, 1 hour or until tomorrow.

## Optional AI

AI is optional and off by default. In Settings, AI, use a local Ollama model or an OpenAI-compatible endpoint that you configure.

With a model enabled, you get hourly recaps, a daily recap and Ask answers. Without one, Blur still captures, stores and searches, and shows a model-free "This hour so far" draft. Search, Library and Time do not need a model.

## Sync across Macs

Blur syncs between your own Macs through a folder carried by iCloud Drive or Dropbox. It writes encrypted, append-only segments protected by a passphrase into the folder you pick. The database itself is never synced, and deletions propagate to your other Macs.

## Claude and other MCP clients

Once enabled in Settings, Connections, `blur mcp` exposes memory search locally to Claude and other MCP clients. It is off by default.

## Build from source

```sh
git clone https://github.com/hazcod/blur.git && cd blur
make macos-app
```

The app lands at `~/Downloads/Blur.app`. See [CONTRIBUTING.md](CONTRIBUTING.md) for the development workflow, [docs/releasing.md](docs/releasing.md) for releases and [docs/architecture.md](docs/architecture.md) for the design.

## Status, security and licence

- Status: Blur is in development. See [docs/project-status.md](docs/project-status.md) for platform status and technical detail.
- Security: see [SECURITY.md](SECURITY.md) to report a vulnerability.
- Licence: Apache-2.0, see [LICENSE](LICENSE).
