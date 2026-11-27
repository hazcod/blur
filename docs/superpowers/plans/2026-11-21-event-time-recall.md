# Event dates and observation dates

A synthetic mail fixture confirms a real recall defect: an appointment on
Monday, observed Thursday, disappears when a time question applies the past
Monday capture window. Retain source capture provenance, but use event dates
as content cues for clear event-time questions. Activity questions still name
observation windows. A subordinate read/capture clause can explicitly supply
that window independently of the event date. Search literals remain protected.

Use grammatical time-question intent rather than an appointment-name allowlist.
Keep event requests from inheriting unrelated observation windows. Retrieval
and source access remain read-only; no calendar/reminder writes are inferred.
The answer continues to use original evidence, including an absent end time.
This does not provide a calendar database or prove arbitrary date extraction.

- [x] Reproduce through planner and native Ask, with activity/mixed-date controls.
- [x] Separate event content dates from capture-time scope and inheritance.
- [x] Extend runtime journeys with independently varied event and capture dates.
- [x] Review, run regressions/vet/full checks and rebuild the canonical signed app.

Focused planner/native tests and all journey seeds pass. Review additionally
found future-verb/nominal-event false capture clauses and event-date follow-ups
becoming capture recaps. Permanent regressions reproduced these before fixes;
the reviewer's independent native probes now pass too. Continuation expansion
also has a reproduced/fixed query-term bound regression. Final `go test -p=2
-timeout=30m ./...`, `go vet -p=2 ./...`, and `git diff --check` pass on the
reviewed source. `make macos-app-stable` rebuilt the canonical Downloads app;
the exact-certificate/designated-requirement gate and strict signature check
pass. Review also reproduced/fixed token-valued date
removal losing a matching topic number, stale relative-date modifiers and an
app-refinement turn losing the original topic/date distinction. The native
app → date → shorten → date regression checks citation, original capture time
and absence of the replaced weekday. Independent review reports no remaining
material continuation findings. Numeric topic/date role ambiguity remains an
explicit lexical-search limitation.

The earlier million-target campaign was deliberately interrupted after source
changes: 986 reported executions and 169 newly interesting inputs, exit 130.
Those are stress/coverage counts, not distinct user needs. Retained discoveries
are reused by the final-source campaign.

The final-source campaign is running with a one-million execution target and
588 retained baseline inputs. Its achieved count remains separate from that
target; execution counts do not establish unique user needs or app acceptance.
