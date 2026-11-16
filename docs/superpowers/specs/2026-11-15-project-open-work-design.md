# Source-Backed Project Open Work

## Intent

When someone asks what remains for a named project, Blur should surface explicit unresolved follow-ups found in captured conversations, even when background processing has not created a separate task. The answer must distinguish a saved task from an inferred candidate and show the source that supports it.

## Design

Use the existing Library proposal pipeline. A local model may classify a source-linked fact as open work only when the source explicitly indicates an unfinished request, an awaited response, or an unresolved next step. Blur verifies that the cited evidence contains a clear unresolved-work cue, with later resolution cues taking precedence over older pending mentions, then adds the exact prefix `Possible open item (inferred, not confirmed): ` itself. These remain ordinary proposed Library facts, so existing correction and dismissal controls apply. Ask will include only such prefixed proposed facts in its structured project open-work answer; it will never promote arbitrary raw captures or credential pages to open work.

## Constraints

- Keep extraction local and source-linked.
- Do not create or schedule a task automatically.
- Do not treat plans, generic prose, login pages, or model guesses as confirmed work.
- Keep existing confirmed tasks and open-loop behavior unchanged.
- Version the enrichment identity so existing captures can be reconsidered under the new extraction prompt.
- Treat Apple model output as untrusted text: keep its source-transform path permissive, validate a narrow syntax repair, and strictly validate the resulting structure and citation.

## Acceptance

- A proposed, prefixed Library fact appears under “Possible, not confirmed” for its project and cites its original source.
- An ordinary proposed fact does not appear as open work.
- Existing tasks and confirmed open loops remain deterministic.
- A credential-form capture cannot be surfaced as a project follow-up.
