# Apple Foundation Models helper

This helper keeps the Go application free of cgo. It accepts one JSON request on
stdin, writes one JSON response on stdout, and exits. It never accepts prompts as
arguments, writes prompt files, or logs native errors. Bundle and sign its binary
with the application; a linker ad hoc signature is only suitable for local checks.

Build with the macOS 27 SDK or newer containing FoundationModels (tested with
Swift 6.4.2). The compiled helper retains the runtime/deployment floors below:

```sh
platform/macos/FoundationModels/build.sh /path/to/blur-foundation-models
```

This builds both arm64 and x86_64. An optional second argument selects one
architecture. The build uses the shared [native cache](../BUILDING.md); signing
remains part of the application packaging step.

The executable has a macOS 13 deployment target and weak links FoundationModels.
All framework use is guarded by macOS 26 availability. Earlier systems return
`unsupported_os`. Native generation requires eligible hardware, Apple
Intelligence enabled, and the system model ready. The helper uses the concrete
on-device `SystemLanguageModel` with default guardrails, no tools, and a fresh
session per request. It does not select cloud compute or a remote provider.

## Protocol version 1

All fields below except `schema` are required, including for `status`:

```json
{"version":1,"operation":"status","system":"","user":"","format":"","max_output_tokens":0}
```

`operation` is `status`, `count`, or `generate`. `format` is empty or `json`.
`schema`, when present, must be a JSON object and requires `format: "json"`.
Unknown request fields, malformed JSON, and trailing JSON are rejected.

Request limits are 256 KiB overall, 32 KiB combined UTF-8 system and user text,
and 16 KiB for the compact JSON schema. Reading uses one additional sentinel byte
to detect an oversized stream. Generation requires `max_output_tokens` in
1...1536; status and count also accept zero. Response text is limited to 12 KiB
UTF-8. Oversized output is rejected rather than truncated.

Responses always contain `version`, `available`, and `reason`. Successful status
also includes `context_tokens` and `identity`; count and generation include
`input_tokens`; successful generation includes `text`. Optional fields can be
absent. Error codes are restricted to `invalid_request`, `unavailable`,
`context_budget`, `refused`, `generation_failed`, and `unsupported_os`.
Availability reasons are `available`, `device_not_eligible`,
`apple_intelligence_not_enabled`, `model_not_ready`, `unavailable`, and
`unsupported_os`. Invalid requests have an empty availability reason.

`identity` names the Apple system provider, OS version/build, and effective
context size. Apple exposes no immutable model asset digest; this value is not a
claim about exact installed weights, and assets can change without the OS build.

## Context and JSON generation

On macOS 26.4 and later, token counts come from the native tokenizer for the
instructions, user prompt, and guided schema. Older supported systems use their
UTF-8 byte lengths, including the actual encoded guided schema. Context size
comes from the framework where available, with a 4096 fallback and an 8192 cap.
Generation reserves 512 template tokens plus the complete requested output
budget and rejects overflow before creating a session. Sampling is greedy.

For JSON requests, the helper adds JSON-only instructions and validates that the
result is a single JSON object. Explicit schemas use guided dynamic generation
when every constraint can be represented: closed objects, required/optional
properties, arrays with bounds up to 64 items, strings/string enums, integers,
numbers, and booleans. Compilation has fixed depth and node limits.

Unsupported JSON Schema constructs, including arbitrary `oneOf` branches, use
the complete schema as JSON-only prompt guidance. Blur's Apple intent adapter
uses closed kind classification and, when needed, a second closed schema for
only that kind's fields; its authoritative action validator remains unchanged. The caller must
retain strict JSON decoding and semantic/evidence validation. These paths do
not gain a native schema guarantee. Requests without a schema likewise use JSON
instructions. The common `answer`/`source_ids`/`insufficient_evidence`,
`proposals`, `suggestions`, and `chronicle` envelopes can use guided generation
when the caller supplies explicit schemas with their exact field names; the
helper does not guess an output type from prompt text.

Guidance ensures structure, not correct classification. In a synthetic proposal
check, the model classified an explicitly requested task about asking Mira as a
`question`. Preserve semantic checks and user review for proposed changes.

Runtime verification used Swift 6.4.2 and macOS 27.2.1. The native provider was
available with 8192 tokens. Synthetic guided evidence answered Mira/4317 with
source S1; unknown evidence returned `insufficient_evidence: true` and no source
IDs; native guardrails returned `refused`. macOS 13 through 26.3 runtime behavior
requires verification on those systems; current-host inspection confirms the
13.0 deployment target and weak framework load command.
