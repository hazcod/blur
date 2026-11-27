# Dutch memory questions

The goal includes natural-language memory across the user's workflows. Existing
request grammar is English-only even though native speech preferences support
Dutch. Add deterministic Dutch personal recall and collection-read grammar,
retaining original question/topic text and the existing evidence, scope, bounded
search and mutation-authority contracts.

Implementation choice: recognize explicit Dutch recall forms alongside the
existing planner and reuse its date-window calculation with a grammar-only
view. Do not translate source content or replace the user's question. Preserve
Dutch event dates as content cues; observation clauses get their own windows.
Short Dutch app/date continuations retain the original topic. A separate
read-only task-collection adapter shares the existing local filters; it must
never authorize a task mutation. Model-first translation would add latency and
cannot establish authority; indiscriminate word translation would damage topic
names and literal Search. Broader multilingual semantics and Dutch task/reminder
creation remain separate acceptance requirements.

- [x] Reproduce Dutch recall, event/observation dates, follow-ups and task reads
      with independent planner and real encrypted native Client regressions.
- [x] Add bounded Dutch grammar while preserving literals and general requests.
- [x] Extend runtime journeys with Dutch event/observation questions and local
      task reads, retaining existing minimized-input decoding and state oracles.
- [x] Independently review, run full checks/vet, build and strictly verify the
      same signed Downloads app. No launch without separately requested approval.

Final evidence: full repository tests and vet pass. Native Dutch Entry, task
reads, event/observation/date continuations and deletion with prior answer prose
pass; all saved journey seeds replay successfully. The current-source runtime
run passed 39 executions with 11 newly interesting inputs in a separate retained
cache. Independent review found and verified fixes for domain dates, explicit
web requests, activity adjectives, bare weekdays and reads falling through to
note saving; its final review has no material findings. The canonical Downloads
app was rebuilt with the original signing identity and strictly verified,
without launching it. These are bounded Dutch grammar checks; the broader
personal-memory/app-acceptance goal remains active.
