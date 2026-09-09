A corpus row's terminal status is not evidence about what happened during the row. A 504
the harness retried into a 200 leaves no trace in `final_http`, which is how a smoke
report printed ZERO deadline failures against five logged 504s. Two scalars
(`attempt_upstream_504_n`, `attempt_overrun_413_n`) closed the 504/413 half; the four
422s and the 400 the regression-diagnosis doc records among eight sequential non-200
attempts were carried by no per-row counter at all.

This is the corpus half of that work. It adds no engine code and touches nothing under
`internal/`; the Go emitters and the reclassification/replay accounting are separate.

**`scripts/corpus/contract.py` — THE contract between the producer and every reader.**
Owns the served status (`SERVED_STATUSES`, 200 only) and the one failure body key
(`ERROR_BODY_KEY`). The producer (`harness.py`) and the classifier both import it, so
they cannot hold different opinions about the same artefact.

**`scripts/corpus/harness.py` — THE PRODUCER.** Writes the artefacts every reader
consumes. `post()` returns a 4-tuple (status, response, dt, body_undecodable): a
completed exchange always keeps its real status, whether or not its body could be read
or decoded, and never falls back to the transport arm's status=0.

**`scripts/corpus/attempt_classes.py` — THE classifier for one attempt artefact.**
Both readers import it, so the two counters that walk these files cannot hold different
opinions about one of them. It carries a closed, TOTAL vocabulary (an attempt that cannot
be placed lands somewhere visible, never in `ok_200`); ONE definition of served —
`is_success_status`, 200 and only 200, which is what the producer accepts; the two FROZEN
counters as INDEPENDENT predicates in the original's own shapes; a legacy kind ladder
DERIVED from the original code path rather than reconstructed from a description of it;
`unreadable` decided by the attempt's OWN status alone; and a new member,
`served_2xx_undecodable_body`, for a completed SERVED exchange whose body could not be
read or decoded.

**`run_shard.attempt_diagnostics` walks the attempts once** and emits the ordered
sequence, per-TURN retry counts, and a class table with every member present at an
explicit zero. It reconciles that walk against the harness's own attempt count, so a row
that lost an artefact reads as UNMEASURED rather than as a smaller, cleaner run than the
one that happened. **`merge_corpus`** refuses to sum rows that were never measured
instead of publishing them as zeros, and asks the shared success predicate rather than
spelling `!= 200` itself; its exact-key-set check is DERIVED from `attempt_classes.CLASSES`,
so a vocabulary change moves it automatically.

## TEST-EVIDENCE

**Local battery of record, unmodified tip `d99fd363bf5353d10908c4ad323eeeabde0c5ea8`:**
51/51 pins in `test_findings_5380.py`; `run_pins.sh` 11/11 pin files; `shape_space.py`
regeneration: 364 cells, 0 legacy-ladder divergences, committed artefact byte-identical.
CI per-sha, all 3 workflows green: `ci` id `34396820678`, `fullstack-acceptance` id
`34396820646`, `PR #487` id `34396816757`.

**Four fixes, each RED-first against an r3-round-3 finding, each independently
re-verified before being trusted:**
- (i) contract guard: retired the AST-literal-walk spelling guard (evadable by a computed
  key, `f"{chr(101)}rror"`) for an exit-path-traced instrument — AST-derives every
  `return` in `harness.post`, line-traces which execute across the full sweep and the
  closed-port cell, asserts the executed set equals the AST set. RED: a `"fatal"` key
  planted on the closed-port arm failed the new pin (43/44); fixed tree 44/44.
- (ii) producer sweep: retired the 9-status hand list (404 absent) for a sweep over the
  full 200-599 status range (less 204/304, which RFC 9110 forbids a body on). RED:
  `or status == 404` at the retry decision failed the new pin (44/45, names 404 exactly);
  fixed tree 45/45.
- (iii) P1-1: a completed exchange with an undecodable body used to fall into the
  transport-failure arm (status 0), indistinguishable from a refused connection. Fixed:
  decode moved outside the transport try; new class `served_2xx_undecodable_body`,
  signalled via `body_undecodable`, gated on `is_success_status(http)` so a non-2xx
  exchange with an undecodable body keeps its real status. RED: the live repro (200 +
  non-JSON body vs a closed port) failed the new pin; fixed tree passes.
- (iv) P1-2: `unreadable` used to require BOTH the attempt's own status and the upstream
  status absent. Fixed: fires on the attempt's own status alone. Measured blast radius:
  exactly 44 of the 1200 enumerated cells change class, all to `unreadable`; the frozen
  legacy ladder does not move (0 occurrences of `classify()` in its body).

**Four further P1s found by two chris-granted post-fix rounds (astra, medium effort),
each independently reproduced against the unfixed tip before being trusted, each with its
own RED-first pin:**
- r4: `e.read()` on the HTTPError arm was unprotected against a truncated body
  (`IncompleteRead` propagated out of `post()` and crashed the caller); the
  "body did not decode" signal was a key INSIDE the response dict, so a validly-decoded
  response carrying that exact key was indistinguishable from a genuine decode failure;
  the new class check ran without a status restriction, so a non-2xx exchange with an
  undecodable body entered the 2xx-only class; the (then-present) branch-coverage trace's
  region locator matched only one specific `if` statement by its call signature and
  missed a branch a mutant inserted before it. All four fixed in one tip; each repro
  independently reproduced against the tip immediately preceding the fix before trusting
  the round's report.
- r5: every decode/read-failure pin drove `harness.post()` directly (or a test helper
  wrapping it) and built its OWN attempt envelope, so none of them exercised
  `run_replicate`'s `json.dump(...)` call — the one place that serializes
  `body_undecodable` to a real artefact. A mutant hard-coding the written field to
  `False` passed all 50 pins. Fixed: a new pin drives the real `run_replicate`, reads back
  the FILE IT WROTE (`run_shard.attempt_files` + `validators.load_attempt`, never a
  hand-built dict), and requires the on-disk artefact to carry the real signal.

**Mutant table, re-derived at the reviewed tip, run in a throwaway clone (never the
lane's live worktree):** 46 arms (the original 42-arm table plus 4 new/re-aimed arms for
this round's fixes), all 46 KILLED, 0 SURVIVED, 0 HARNESS_ERROR.

## RISK-NOTES

**A closed vocabulary grew by one member (chris's ruling: "add the member").**
`attempt_classes.CLASSES` gained `served_2xx_undecodable_body`. Nothing is published yet,
so the bill is bounded, but `merge_corpus.py`'s exact-key-set check
(`set(counts) == set(CLASSES)`) means every row written by a PRIOR build of this
instrument becomes unmeasured the moment `CLASSES` grows — expected and priced, not a
merge-time surprise.

**Branch coverage of the retry/terminal decision is NOT asserted, and a prior attempt at
it was removed.** A `sys.settrace`-based line-coverage guard over `run_replicate`'s retry
decision was added, then found evadable: Python's line tracer fires a 'line' event for a
single-line compound statement (`if cond: consequent`) the moment the condition is
evaluated, whether or not the same-line consequent ever runs, so a status-corrupting
mutation written as one line read as "covered" on every call where it never fired (the
equivalent multiline form WAS caught — the guard's real coverage depended on source
formatting, not behaviour). Removed rather than patched further: line-identity tracing
cannot provide branch coverage, only statement/line coverage. What remains — the
exhaustive 200-599 status sweep — guarantees exhaustive STATUS INPUT to the real
producer, not branch coverage of its retry decision.

**FROZEN MEANS FROZEN, and it leaves a known gap.** `attempt_upstream_504_n` and
`attempt_overrun_413_n` key a published artefact. A bare 413 — the attempt's own status
with no parsed failure object — counts ZERO in the frozen counter, exactly as the
original counted it, even though the closed vocabulary classifies that attempt
`overrun_413` and `attempt_class_n` carries it. Widening a frozen key changes what past
numbers meant and is indistinguishable, to anyone reading a trend, from a rise in ceiling
rejections. The gap is ticketed separately, not absorbed here.

**`unreadable` names two facts.** An artefact that did not parse and one that parsed but
carried no `status` both classify `unreadable`. Both fail CLOSED, which is the property
that matters, and the pin says so; distinguishing them changes a published vocabulary and
is deliberately not taken in this change. `unreadable_reason` rides beside the class with
a closed pair of values, present on every outcome record including as `None`.

**Every refused row is counted AND named.** A row with no corpus id is named
`<no corpus_id>` in both the unavailable-id list and the unreconciled detail, and a pin
asserts the list and the count are the same length.

**The AST sharing pin is structural only, and says so.** It proves the calls EXIST, not
that their values feed the counters. The equivalence and totality pins are what hold that
property; the AST pin exists to catch a second ladder growing back.

**Not this change.** The engine-side attempt telemetry, and the reclassification/replay
accounting are separate cuts. `reclassify_deadlines.py` is untouched here, and it reads
`attempt_upstream_504_n` — which is precisely why that counter is restored to the
original's independent predicate shape in this cut rather than left derived from the
exclusive class.

**Formatting.** `ruff format` is not this repo's gate for these scripts: there is no
`pyproject.toml`, `ruff.toml`, lefthook or pre-commit config, no workflow references
`scripts/corpus`, and on the pristine merge base every corpus file would be reformatted.
Running it here would produce a large unrelated diff, so it is not run, and that is
stated rather than passed over. `py_compile` is rc=0 on every changed module.
