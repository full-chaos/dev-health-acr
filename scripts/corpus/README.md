# Corpus instrument

An HTTP-driven evaluation harness for the Context Fabric investigation path, plus
the scorer that turns a run into a verdict. It drives `ask-dev` (which signs and
forwards to `acr-api`), records every attempt, and merges the attempts into one
artefact.

## Why the scoring is not just five buckets

An earlier version bucketed each row as served-with-data / served-degraded /
unserved / clarification / error and reported the totals. On a real comparison that
table said a run had **improved** — more served rows, fewer errors — while the run
had in fact stopped doing what a third of the corpus exists to test:

* rows whose declared expectation is to **refuse** stopped refusing, and one of
  them started serving;
* a negative-control row naming an entity that does not exist was answered by
  committing a *different, real* subject and claiming facts about it.

Both movements score as *improvements* on bucket rank, because bucket rank is not
a quality ordering for a corpus where roughly a third of the rows are negative or
refuse-expected controls and **serving is the failure**. Two mechanisms close that
gap, and they are the reason this directory exists.

### 1. Subject identity (`subject_identity.py`)

Compares the subject the engine **committed** against the entity the row names.

* **R1** a row whose note declares the named entity *nonexistent* has nothing
  correct to commit to, so any committed subject is a substitution.
* **R2** a row declaring `anchor=<NAME>/<kind>` must commit a subject
  corresponding to `NAME`.
* **R3** committed kind vs the row's `requested_kind` is **recorded, not scored** —
  `requested_kind` is the *member* kind on cohort rows, so a mismatch is not
  automatically wrong.

A confirmed substitution forces `bucket_v2` to its own failure bucket and is a
disagreement unconditionally. It reads the **raw per-attempt JSON**, not the shard
summaries, so it applies retroactively to runs already completed.

This is deliberately *not* the harness's `wrong_subject_flag`, which is a
turn-by-turn receipt-selection signal and read `False` on the substituted row.

### 2. Expectation-aware scoring (`expectations.py`)

Rows whose `note` declares an expected outcome are scored by **agreement** with it,
in their own table beside the five buckets. A disagreement is a regression
*regardless of bucket rank*.

The verdict is three-valued: `agree`, `agree_weak`, `disagree`. `agree_weak` exists
because two values cannot separate "declined cleanly" from "never fabricated but
never terminated either" — and that distinction was the whole finding on the run
that motivated this work, where eight rows stopped terminating and looped to
`MAX_TURNS`. **`agree_weak` is never counted as agreement.**

## Supplying a corpus

**The real corpus is not in this repository, on purpose.** Its rows carry the
evaluation questions verbatim; publishing them would let the questions be optimised
against, and the corpus stops measuring the engine the moment it can be trained on.

Supply it at run time as a module named `corpus` on `sys.path`. The contract — the
required fields, and the phrases in `note` that the scorer keys off — is documented
in `corpus_example.py`, which is a **synthetic** stand-in used by the tests.
Numbers produced from the example are meaningless by construction.

Row **ids** are safe to print. **Question text must never be copied into a report,
record, ticket or commit** — rows by id only.

## Running

```bash
# sequential: the shape a measurement of record uses
CORPUS_BASE=http://127.0.0.1:3040/api/investigations ./run_corpus_sequential.sh 1

# parallel: bounded fan-out. Validate against a sequential control on the SAME
# corpus and code before trusting its numbers -- see the caveat below.
CORPUS_MAX_CONCURRENT_SHARDS=2 ./run_corpus_parallel.sh 12 1
```

`CORPUS_BASE` defaults to `http://127.0.0.1:3040/api/investigations`.
`CORPUS_TICKET` (optional) is stamped into the verdict's `ticket` field.

### Parallel is a load probe, not a faster measurement

Every shard's synthesis stage is one model call against one credential under the
engine's own per-request budget. Raising concurrency converts served rows into
deadline rows, which reads as a regression that is really an instrument artefact.
Measured on a 36-row corpus at 12 shards / concurrency 2 against its own sequential
control: **8 of 36 rows landed in a different bucket**, four expectation verdicts
moved, one upstream deadline appeared, and a result-validation failure surfaced that
the sequential run had not shown. Wall time did fall (about 1.8x), and total engine
work rose. Keep the default low, and treat parallel as a way to *find* load defects
rather than a way to measure faster.

`reclassify_deadlines.py` re-runs a deadlined row once, sequentially, and marks it —
instrument load is never counted as a regression. It rewrites shard artefacts **in
place**, so snapshot the shard directory first. Judge any reproduction claim on the
**pre-reclassify** artefacts: reclassification legitimately drives the deadline
count to zero, which would make a load failure read as a clean run.

## Counting rules

* **Attempt-level, not terminal.** A row's terminal status hides everything the
  harness retried — a deadline retried into a 200 leaves no trace in `final_http`.
  Every deadline/selection predicate reads attempt counters.
* **Selection and recovery are different predicates.** Selection is attempt-level;
  recovery is the terminal bucket via the same `classify()` the verdict uses, so a
  label can never disagree with the number beside it.
* **Stage matching is exact.** Compare a record's `stage` field by equality, never
  by substring: `kind_offer` is a different stage from `kind_offer_withheld`.
* **An unreadable artefact is its own state**, never a silent pass.

## Layout

| file | role |
|---|---|
| `harness.py` | turn chain, retries, per-attempt JSON |
| `corpus_example.py` | synthetic corpus + the contract a real one must meet |
| `shard_plan.py` | family-balanced, deterministic layout; reaches nothing live |
| `run_shard.py` | one shard into its own directory |
| `merge_corpus.py` | **the single verdict**; `classify()` owns the buckets |
| `expectations.py` | declared expectation + agreement scoring |
| `subject_identity.py` | committed-subject check, from raw attempts |
| `engine_failures.py` | post-hoc attempt classes with request ids |
| `reclassify_deadlines.py` | deadline re-run, marked in the artefact |
| `test_shard_plan.py`, `test_instrument.py` | contract tests; no rig needed |

A single shard's artefact is never standalone evidence. The merge is the verdict.
