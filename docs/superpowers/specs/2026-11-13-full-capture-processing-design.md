# Full capture and processing

## Intent

Blur must turn content acquired from the user's macOS apps into usable memory:
complete acquired text, encrypted storage, indexing, Ask retrieval, Library and
recaps. Successful storage that is invisible to downstream readers is a defect.
The user's request is to implement this end to end and keep working without
another planning approval round.

## Architecture decisions

- Keep the existing cross-app Accessibility path and add bounded document
  continuation using the already implemented multipart capture primitives.
- Use a trusted `capture.Evidence` value for either a legacy observation or an
  authenticated published capture part. Preserve each part's actual source ID,
  parent, offsets and capture time. Do not forge a legacy observation.
- Expose both source kinds through optional generic storage readers and a
  bounded source directory. Connect the existing exact-search, Ask, Library,
  recap and semantic consumers, including pending-index work after restart.
- Acquire pixels only after verifying the target and capture region. Browser
  fallback must sample the page area before acquisition and exclude chrome.
- Use Apple's documented source-transformation mode for existing-text
  processing. Keep strict response/provenance validation and default policy for
  general questions. A refusal cannot stop indexing or later sources.

## Alternatives considered

Increasing one text/timeout limit leaves truncation, downstream invisibility and
blocking reads in place. Requiring a browser extension leaves unsupported app
classes. Changing labels to partial coverage alone does not repair ingestion.
The selected design connects the existing bounded capture and storage work to
the actual production consumers.

## Invariants and completion evidence

- Preserve capture consent, exclusions, private/secure-context checks and
  authority at acquisition and publication. Do not capture excluded fields.
- No source, screenshot, prompt or native error body in diagnostics.
- Stage incomplete captures privately; authenticate membership before making
  their parts visible. Readers verify live publication and erasure fences.
- Maintain bounded per-round work, cancellation, foreground responsiveness and
  restart recovery. Source size must not become one giant model request.
- Deletion/retention must remove part projections, indexes and payloads.
- Prove synthetic app classes and large-tail retrieval end to end. Record
  actual live app checks separately; fixtures do not establish live coverage.
- Package only `~/Downloads/Blur.app`, preserve the exact certificate and
  designated requirements. App lifecycle actions require separate authorization
  under AGENTS.md; no real recording or lifecycle operation is implicit here.
