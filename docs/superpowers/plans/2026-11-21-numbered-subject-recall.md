# Numbered subject recall

An independently reproduced recall defect treats Route 42's date `23` as a
complete match for Route 23, then ranks it ahead of the actual source by recency.
This occurs on first-turn questions as well as date follow-ups. The fix should
preserve the relationship between a topic word and its number across Ask,
native Search and MCP, including conversational refinement.

## Approach

Use bounded local phrase evidence as a relevance key and match-quality cue.
Extract numbered topic pairs from query word occurrences, excluding actual
date spans and consumed query grammar. Retain them through follow-ups.
Candidate text/title supporting those pairs ranks ahead of independent word
hits; unverified pairs are closest matches. Keep existing app/time/privacy,
hydration and index budgets. No storage migration or model dependency.

This is the implementer's choice within the authorized recall objective.
Mandatory exact phrases would lose useful paraphrases; requiring a model for
the basic distinction would leave model-free recall broken. Phrase support
is evidence for ranking, not proof of full semantic role understanding.

## Work

- [x] Reproduce ranking and match-quality failures through real encrypted Ask,
      native Search and MCP, with generic numbered subjects and date collisions.
- [x] Preserve bounded numbered-topic cues through planning and follow-ups.
- [x] Share candidate ranking/match-quality behavior across retrieval surfaces
      with bounded source reads and clear coverage when incomplete.
- [x] Extend runtime journeys to vary numbered topics and conflicting sources.
- [x] Independently review, run focused/full checks and vet, rebuild the same
      signed Downloads app and verify its exact identity without launching it.

The million-target mutation campaign remains separate execution evidence;
its counter does not establish unique needs or real-app acquisition coverage.

Native RED/GREEN regressions now cover route/invoice/project subjects, bare
and entity fallback, quoted qualifiers, combining accents, reversed order,
multiple pairs and deletion. Review found the late-planning and quote bypasses
before fixes, then independently replayed native Ask/Search/MCP and app/date/
grounded chains. Focused native plus journey seeds pass, as do all assistant
tests and vet. Independent adjacency checks cover title fields, contractions
and intervening words. Clock/version components are not numbered-topic cues.

Final `go test -p=2 -timeout=30m ./...`, `go vet -p=2 ./...` and
`git diff --check` pass. The canonical signed app was rebuilt and strict
signature verification passes; its exact certificate/designated requirement
and unsandboxed entitlements are preserved. No launch/restart was performed.
The final-source mutation campaign is running with a one-million execution
target and 660 retained baseline inputs. Its actual report remains separate
from that target and from semantic use-case diversity.
