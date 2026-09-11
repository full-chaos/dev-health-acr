#!/usr/bin/env python3
"""the tracked issue — operator ruling, applied to a PARALLEL run only.

"Any row that reads `deadline_exceeded` in a PARALLEL run is re-run once sequentially before
classification and the artefact marks it `reclassified-after-load` — instrument load is never
counted as a regression."

Rationale, so nobody later mistakes this for score-fixing: the 2026-09-05 baseline established
that acr's synthesis stage dies at exactly duration_ms=60001 — the request's own 60 s budget —
and that this happens even under SEQUENTIAL load. Concurrency can only make it likelier. A row
that times out under load and then serves cleanly on a quiet re-run was never a behaviour change;
counting it as one would report our own instrument as a product regression. The reverse is
protected too: a row that times out AGAIN sequentially keeps its error classification, and the
artefact says so.

This rewrites the shard artefacts in place (adding the mark), so the merge downstream sees the
corrected rows and the verdict carries the provenance. It NEVER silently upgrades a row: every
touched id is listed, with its before and after outcome.

Usage: reclassify_deadlines.py <shards-dir> <rep>
"""
import json
import os
import subprocess
import sys

# r1 #11. The identity scan must know where a re-run's raw attempts land. This name is
# the contract between the two modules; subject_identity.EXTRA_ATTEMPT_GLOBS reads it.
REPLAY_DIRNAME = "reclassify"
import time
from pathlib import Path

HERE = Path(__file__).parent
sys.path.insert(0, str(HERE))

# CHAOS-5562 r2: same guard as run_shard.py -- this is another caller of
# harness.run_replicate, found by the r2 reviewer's own caller sweep (it does not
# contain the literal substring "harness.py", which is why the r1 caller sweep
# missed it). Refuse before `import harness` below, which imports the external
# `corpus` module for a reason unrelated to CORPUS_BASE. Gated on
# `__name__ == "__main__"` so `import reclassify_deadlines as RD` (test_findings_5380.py
# does this without CORPUS_BASE set) is unaffected.
if __name__ == "__main__" and not os.environ.get("CORPUS_BASE"):
    sys.exit(
        "CORPUS_BASE is not set -- refusing to start. There is no default rig "
        "leg; set CORPUS_BASE to the investigations endpoint you own (see "
        "scripts/corpus/README.md)."
    )

import harness  # noqa: E402
from corpus import CORPUS  # noqa: E402
from run_shard import detail_for  # noqa: E402
import merge_corpus  # noqa: E402  — single bucket authority; recovery must agree with the verdict

BY_ID = {r["id"]: r for r in CORPUS}
DEADLINE_CODES = {"acr_timeout", "deadline_exceeded"}


def is_deadline(row):
    """Did this row read a deadline DURING the run — not just at the end of it?

    THIRD SIGHTING of one bug class in this instrument (after smoke_report.py and
    the merge's own diagnostics): reading only the TERMINAL fields hides a 504 the
    harness retried into some other outcome. Every row that the parallel run
    turned from served into something else had exactly that shape — an attempt at
    the 60 s budget, then a retry that landed on no_match or a 400 — so a
    terminal-only test would have selected ZERO rows here and reported "no
    reclassification needed" while silently counting load as a regression, which
    is the precise outcome the 03:23Z ruling exists to prevent.

    attempt_upstream_504_n is recorded per row by run_shard.detail_for.
    """
    return ((row.get("failure_code") or "") in DEADLINE_CODES
            or row.get("final_http") == 504
            or (row.get("attempt_upstream_504_n") or 0) > 0)


def build_reclassified_row(before, after):
    """The ORIGINAL row (`before`) and the sequential replay's own row (`after`),
    merged into the row `main()` publishes in place of `before`. Pure and
    I/O-free so it is testable without a real run_replicate call.

    r5 P1-2 (reproduced red, this lane): the caller used to publish `after`
    verbatim, which REPLACES the original row wholesale -- the replay's own
    (possibly smaller) attempt count, its own attempt_upstream_504_n, its own
    attempt_outcomes. A row whose ORIGINAL parallel run genuinely hit an
    upstream 504 (that is why is_deadline() selected it in the first place)
    could read attempt_upstream_504_n=0 after reclassification simply because
    the quiet single-attempt re-run never repeated it -- the original deadline
    evidence was not wrong, it was erased, and nothing downstream could tell
    "recovered" from "we forgot".

    `original_attempt_evidence` is the fix: the ORIGINAL row's attempt-level
    fields, carried BESIDE the replay's under their own key, never folded into
    or overwriting the replay's own counters. `parallel_outcome_before_
    reclassification` (four terminal scalars) is kept alongside it, unchanged,
    for anything already reading it.
    """
    after = dict(after)
    after["reclassified-after-load"] = True
    after["parallel_outcome_before_reclassification"] = {
        "final_http": before.get("final_http"),
        "final_payload_status": before.get("final_payload_status"),
        "failure_code": before.get("failure_code"),
        "wall_seconds": before.get("wall_seconds"),
    }
    # The raw attempt files from the ORIGINAL run are never deleted (they sit
    # under the shard's own replicate/ dir; the replay writes to a SEPARATE
    # reclassify/<qid>/replicate/ dir -- see REPLAY_DIRNAME above), so a
    # file-level scanner will still find them. merge_corpus.reconcile_attempt_
    # totals reads exactly this field to know a reclassified row's expected
    # file-scan total is the ORIGINAL's evidence plus the replay's own, never
    # the replay's alone.
    after["original_attempt_evidence"] = {
        "attempts": before.get("attempts"),
        "attempt_outcomes": before.get("attempt_outcomes"),
        "attempt_class_n": before.get("attempt_class_n"),
        "attempt_upstream_504_n": before.get("attempt_upstream_504_n"),
        "attempt_overrun_413_n": before.get("attempt_overrun_413_n"),
        "attempts_reconciled": before.get("attempts_reconciled"),
    }
    # SELECTION and RECOVERY are deliberately DIFFERENT predicates, and
    # conflating them is a real bug this file had:
    #   selection  = "did the row read a deadline during the PARALLEL run"
    #                -> attempt-level, because a retried 504 leaves no terminal trace;
    #   recovery   = "did the quiet re-run end in a SERVED state"
    #                -> terminal-only, because a re-run that hit a 504 on one
    #                   attempt and then served DID recover.
    # Using is_deadline() for both labelled qa-grouped-clean
    # "still_deadline_sequentially" when its re-run in fact reached `degraded`
    # — i.e. it reported instrument load as a persistent failure, the exact
    # thing the 03:23Z ruling forbids. Recovery is judged by the SAME bucket
    # authority the verdict uses, so the label can never disagree with the
    # number beside it.
    recovered = merge_corpus.classify(after) in ("served_with_data", "served_degraded")
    after["reclassification_verdict"] = (
        "served_when_not_under_load" if recovered else "still_failing_sequentially"
    )
    after["reclassification_recovery_basis"] = (
        f"terminal bucket {merge_corpus.classify(after)}; "
        f"re-run attempt-level 504s = {after.get('attempt_upstream_504_n')}")
    return after


def main():
    # Defense in depth, same as run_shard.py's own main(): the module-level guard
    # above only fires for a direct `python3 reclassify_deadlines.py` run; a caller
    # that imports this module and calls main() itself still gets a clean refusal.
    try:
        harness.require_base()
    except harness.MissingCorpusBase as e:
        sys.exit(str(e))
    shards_dir = Path(sys.argv[1])
    rep = int(sys.argv[2]) if len(sys.argv) > 2 else 1
    summaries = sorted(shards_dir.glob("*/shard-summary.json"))
    if not summaries:
        sys.exit(f"no shard summaries under {shards_dir}")

    hits = []
    for s in summaries:
        data = json.loads(s.read_text())
        for r in data["rows"]:
            if is_deadline(r):
                hits.append((s, r["corpus_id"]))

    if not hits:
        print("no deadline_exceeded rows in the parallel run; nothing to reclassify")
        return

    print(f"{len(hits)} row(s) hit a deadline under load; re-running each ONCE, sequentially:")
    # r2 #11. This used to be `HERE / REPLAY_DIRNAME` -- the SCRIPT directory, a sibling
    # of the shard tree. The identity scan globs relative to the root it is given (the
    # shards dir), so replayed attempts were invisible to it: the summary described the
    # re-run while identity still scored the pre-reclassification attempts. One path
    # convention -- the replay lives under the shards root the scanner reads.
    outroot = shards_dir / REPLAY_DIRNAME
    outroot.mkdir(parents=True, exist_ok=True)
    changed = []

    for summary_path, qid in hits:
        data = json.loads(summary_path.read_text())
        before = next(r for r in data["rows"] if r["corpus_id"] == qid)
        d = outroot / qid
        (d / "replicate").mkdir(parents=True, exist_ok=True)
        harness.OUTDIR = d / "replicate"
        t0 = time.time()
        try:
            r = harness.run_replicate(qid, BY_ID[qid]["text"], rep)
        except harness.ServedBuildMismatch as e:
            sys.exit(str(e))
        after = detail_for(harness.OUTDIR, qid, BY_ID[qid], r, time.time() - t0, rep)
        after = build_reclassified_row(before, after)
        data["rows"] = [after if x["corpus_id"] == qid else x for x in data["rows"]]
        data.setdefault("reclassified_ids", []).append(qid)
        summary_path.write_text(json.dumps(data, indent=2))
        changed.append((qid, before.get("failure_code") or before.get("final_http"),
                        after.get("final_payload_status") or after.get("failure_code"),
                        after["reclassification_verdict"]))
        print(f"  {qid}: {changed[-1][1]} -> {changed[-1][2]}  [{changed[-1][3]}]")

    (outroot / "reclassification-log.json").write_text(json.dumps({
        "ruling": "deadline re-run ruling",
        "rows": [{"corpus_id": c[0], "parallel": str(c[1]), "sequential": str(c[2]),
                  "verdict": c[3]} for c in changed],
    }, indent=2))
    print(f"-> {outroot}/reclassification-log.json")


if __name__ == "__main__":
    main()
