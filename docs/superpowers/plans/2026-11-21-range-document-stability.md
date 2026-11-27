# Range-only document stability

A real native CF/range fixture reproduces mixed content receiving a complete
document receipt when the provider edits text without changing its character
count. Existing window/path/sensitivity/count checks cannot detect that edit.
AXValue-backed text already uses an immutable CFString; the gap is range-only
providers read over several rounds.

Use a bounded second pass over the same range before reporting completion.
Hash the first pass as pieces enter the existing redactor/staging path, then
compare a fresh streamed pass. Changed or unavailable validation aborts the
unpublished document. Keep both passes' character/sensitivity/path checks,
bounded native requests and byte/node/time/cancellation budgets. Verification
work must count as real forward progress without being counted as emitted text.
Only fixed-size hashes and one piece are retained; no raw disk buffer, storage
change, whole-page allocation or total document-size ceiling is introduced.

This is the implementer's choice within the existing streaming-capture design.
A whole-range native snapshot may be unavailable or expensive for these same
providers; marking mixed data partial alone does not fix consistency. A matching
second pass establishes repeatable observed content, not an atomic snapshot of
arbitrary dynamic application state or a substitute for native app acceptance.

- [x] Reproduce the same-length edit through the actual native range conversion
      and document cursor, with stable window/path/count/roles.
- [x] Add bounded verification and preserve cancellation/no-progress behavior.
- [x] Cover stable Unicode/large ranges, edits, unsupported verification,
      private transitions and progress across rounds; extend runtime mutation.
- [x] Independently review, run full checks/vet and build/verify the same signed
      Downloads app. Live app acceptance remains a separate gate.

Final verification: all packages and `go vet` pass on the final source. The
focused complete capture suite passes, and the stricter native runtime campaign
passes 145,461 executions with five newly interesting inputs after replaying
161 retained inputs. Independent review found no material issue. The canonical
signed Downloads app was rebuilt and strictly verified without launching it.
The memory journey's one-million target remains a separate running campaign;
no count here establishes distinct user needs or real-app capture support.

Non-interactive standalone native capability currently reports
`permission_required`; no TCC change was attempted. The signed app's grant has
not been tested. Its launch requires the separately requested authorization
under AGENTS.md. CUA inventory works, Herdr has no inspectable CG window, and
Terminal inspection was rejected by automatic approval review for safety.
