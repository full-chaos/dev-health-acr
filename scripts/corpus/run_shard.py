#!/usr/bin/env python3
"""run ONE shard of the 36-row corpus against the live rig.

A single shard's artefact is NEVER standalone evidence (run-two-turn-parallel.sh
SCOPE NOTE). The pass/fail signal is merge_corpus.py's verdict; this script only
produces one input to it, and exits non-zero on a genuine driver failure so the
merge refuses to run.

Isolation: each shard runs in its OWN directory with its OWN copy of
corpus.py/harness.py, so harness.OUTDIR (module-level, <dir>/replicate) never
collides between shards. That is this harness's analogue of run-two-turn-
parallel.sh's per-shard DATABASE: the shared, mutable, run-scoped state here is
the per-attempt JSON files, not a Postgres schema.

NOT isolated, and disclosed rather than hidden: every shard talks to the SAME
acr-api over the SAME Postgres, so `acr.context_fabric_structure_prior_pointer`
(org_id PRIMARY KEY, one row per org, all shards run as the same org) is shared
and its interleaving differs between parallel and sequential execution. Answer
reuse keys on (org_id, question_hash) and every corpus row is a distinct
question, so reuse does NOT collide across shards. This is exactly why the
sequential control run is mandatory before parallel numbers are trusted.

Usage: run_shard.py <shard-index> <shard-count> <rep>
Reads its id list from shard_plan.py so the plan is computed by the same code
the plan-only path prints.
"""
import glob
import json
import os
import sys
import time
from pathlib import Path

HERE = Path(__file__).parent
sys.path.insert(0, str(HERE))

import harness  # noqa: E402
import attempt_classes  # noqa: E402
from attempt_order import order_attempts, parse_attempt_name  # noqa: E402
from validators import load_attempt  # noqa: E402
from corpus import CORPUS  # noqa: E402
from shard_plan import plan  # noqa: E402

BY_ID = {row["id"]: row for row in CORPUS}

# filenames the ordering helper could not sequence, per corpus id, surfaced in the shard
# summary rather than dropped.
UNSEQUENCED = {}


def attempt_files(outdir, qid, rep):
    # r4: THE shared ordering helper. A bare sorted() here put `t10` before `t9`, so
    # last_attempt() -- and therefore the shard summary's terminal result -- named the
    # wrong file. The identical defect was fixed in subject_identity and left here.
    ordered, unsequenced = order_attempts(
        glob.glob(str(outdir / f"{qid}-rep{rep}-t*-a*.json")), on_unparseable="skip")
    # r6 (d): never discarded. A file we cannot sequence is evidence that something wrote
    # an artefact we do not understand; dropping it silently is how it stays unnoticed.
    UNSEQUENCED.setdefault(qid, []).extend(Path(p).name for p in unsequenced)
    return ordered


def last_attempt_file(outdir, qid, rep):
    files = attempt_files(outdir, qid, rep)
    return files[-1] if files else None


def attempt_diagnostics(outdir, qid, rep, harness_attempts=None):
    """Per-ATTEMPT diagnostics, walked ONCE across every attempt of the row.

    NOT part of the field set compared against lane-corpus-sweep-1 — the 09-05
    baseline summary has no counterpart for these, so they are reported for the
    after-arms only and are NEVER differenced against the baseline. They exist
    because the row's terminal status hides them: a 504 that the harness retried
    into a 200 leaves no trace in final_http, and the ramp smoke's reporter
    under-counted deadlines 2->0 and 3->0 for exactly that reason.

    CHAOS-5380 widens this from two scalars to the SEQUENCE plus a total class
    counter, because counting one class fixed one class. §6 of the
    regression-diagnosis doc records eight sequential non-200 attempts comprising
    four 422s, one 504, two 413s and one 400 — and until now a row said nothing
    about the 422s or the 400 at all. The classification itself lives in
    attempt_classes, shared with engine_failures, so the two counters that read
    these artefacts cannot hold different opinions about one file.

    attempt_outcomes  ordered per-attempt list: turn, attempt, class, http,
                      upstream_http, code, dt_s. Ordered by RECORDED SEQUENCE via
                      attempt_order, never by path.
    attempts_total    len(attempt_outcomes)
    attempts_retried  attempts beyond the first WITHIN EACH TURN, EXPLICIT zero when
                      nothing was retried. NOT len-1: a row spans several TURNS and a new
                      turn is a follow-up, not a retry. A live replicate caught the naive
                      form calling 32 of 36 rows retried (69 reported against 6 real) --
                      every pin fixture had put its attempts under t1, so the pins agreed
                      with the bug
    attempt_class_n   every member of the closed vocabulary, always present, so an
                      absent measurement and a measured zero never look alike
    upstream_504_n    unchanged name and meaning (the deadline reclassification
                      selector and the merge diagnostics read it), now DERIVED from
                      the same walk so it cannot drift from the class counter
    overrun_413_n     likewise; overrun_detail keeps the LAST 413's continuation
    """
    outcomes = []
    files_seen = 0
    counts = attempt_classes.zero_counts()
    n504 = n413 = 0
    # attempts per TURN, so a retry can be told from a follow-up turn.
    per_turn = {}
    overrun = None
    sequenced_files = attempt_files(outdir, qid, rep)
    files_seen = len(sequenced_files) + len(sorted(set(UNSEQUENCED.get(qid) or [])))
    for f in sequenced_files:
        parsed, seq = parse_attempt_name(f)
        turn, index = (seq[1], seq[2]) if parsed else (None, len(outcomes) + 1)
        per_turn[turn] = per_turn.get(turn, 0) + 1
        ok, a, reason = load_attempt(f)
        if not ok:
            # r6 (d), restated: an artefact we cannot read is REPORTED, never
            # skipped. A scanner that drops what it cannot parse describes a
            # smaller, cleaner run than the one that happened.
            outcomes.append(attempt_classes.unreadable_outcome(turn, index, reason))
            counts["unreadable"] += 1
            continue
        # classify() is read DIRECTLY rather than off the record outcome() returns.
        # Both go through the one classifier, so they cannot disagree -- and the
        # CHAOS-5430 consumer sweep, which correctly refuses to guess what a call it
        # cannot follow returned, has no unresolved read to report. Subscripting the
        # returned record here was exactly the shape it exists to catch.
        cls = attempt_classes.classify(a)
        outcomes.append(attempt_classes.outcome(a, turn, index))
        counts[cls] += 1
        # The two FROZEN counters are INDEPENDENT tests, not readings of the exclusive
        # class (codex r4 P1). An attempt may count as both a 504 and a 413; deriving them
        # from `cls` silently dropped the deadline on every 504 the engine answered with an
        # inner 422 or 413, and the deadline-reclassification selector reads that counter.
        if attempt_classes.is_upstream_504(a):
            n504 += 1
        if attempt_classes.is_overrun_413(a):
            n413 += 1
            failure = (a.get("response") or {}).get("failure") or {}
            overrun = {
                "axis": (failure.get("narrowerContinuation") or {}).get("axis"),
                "family": (failure.get("narrowerContinuation") or {}).get("family"),
                "overrun": failure.get("overrun"),
                "measured_items": failure.get("measuredItems"),
                "max_items": failure.get("maxItems"),
            }
    # RECONCILIATION. The harness counts its own attempts as it makes them; this walk
    # counts the artefacts they left. Those two numbers are produced independently and
    # must agree, and when they do not the row is NOT MEASURED -- it is a row we are
    # missing evidence about, and it says so instead of publishing a smaller, cleaner
    # run than the one that happened.
    #
    # Review round 2 found the shape: `attempt_files` silently drops a filename that
    # matches the glob but cannot be sequenced (it lands in UNSEQUENCED, which nothing
    # downstream read), so a row published attempts_total=1 against the harness's 2 and
    # the 504 that second attempt carried simply vanished -- while the class table stayed
    # structurally complete, so the merge's exact-key guard passed it. Every defect on
    # this seam has been caught by two counters disagreeing; this makes them disagree
    # OUT LOUD rather than quietly.
    unsequenced = sorted(set(UNSEQUENCED.get(qid) or []))
    # THREE conditions, all required (review round 2's ruling). This compared the
    # harness's count against the SEQUENCED outcomes ONLY, so a row that visibly dropped
    # an artefact still reconciled and the merge published its totals -- the
    # dropped-artefact defect walking back in through the door built to stop it.
    #   1. the harness's own count equals what the walk sequenced;
    #   2. nothing was left unsequenced -- a file we know we did not read is missing
    #      evidence whatever the counts say;
    #   3. every file the glob matched is accounted for in one of those two piles.
    # A MISSING harness count is NOT reconciled either: a row nobody can reconcile has not
    # been reconciled, and calling it measured is the same false-zero move one level up.
    accounted = len(outcomes) + len(unsequenced)
    reconciled = (harness_attempts == len(outcomes)
                  and not unsequenced
                  and accounted == files_seen)
    return {"attempt_outcomes": outcomes,
            "attempts_total": len(outcomes),
            "attempts_reconciled": reconciled,
            "harness_attempts": harness_attempts,
            "unsequenced_files": unsequenced,
            # Per TURN. `sum(n - 1)` over the turns, never len(outcomes) - 1.
            "attempts_retried": sum(n - 1 for n in per_turn.values()),
            "attempt_class_n": counts,
            # INDEPENDENT counts, same walk, ORIGINAL predicate shapes -- never
            # counts["upstream_504"] / counts["overrun_413"], which are exclusive.
            "attempt_upstream_504_n": n504,
            "attempt_overrun_413_n": n413,
            "overrun_detail": overrun}


def detail_for(outdir, qid, row, r, dt, rep):
    """Identical field set to lane-corpus-sweep-1's sweep_runner.py, so the
    before/after table compares like for like. Do not add or rename fields
    without restating the baseline."""
    detail = {
        "corpus_id": qid,
        "section_note": row.get("note", ""),
        "family": row.get("family"),
        "final_http": r["final_http"],
        "final_payload_status": r["final_payload_status"],
        "chain": r["chain"],
        "attempts": r["attempts"],
        "wrong_kind_flag": r["wrong_kind_flag"],
        "wrong_subject_flag": r["wrong_subject_flag"],
        "subject_kind_mismatch_flag": r["subject_kind_mismatch_flag"],
        "wall_seconds": round(dt, 2),
    }
    f = last_attempt_file(outdir, qid, rep)
    if f:
        ok, last, _ = load_attempt(f)
        if not ok:
            last = {}
        resp = (last or {}).get("response") or {}
        result = resp.get("result") or {}
        failure = resp.get("failure") or {}
        detail["last_request_id"] = result.get("request_id") or resp.get("request_id")
        detail["last_result_id"] = result.get("result_id")
        detail["service_version"] = (result.get("versions") or {}).get("service_version")
        comp = result.get("completeness") or {}
        detail["completeness_state"] = comp.get("state")
        detail["claimed_facts_n"] = len(result.get("claimed_facts") or [])
        cov = result.get("coverage") or {}
        detail["coverage_sources_n"] = len(cov.get("sources") or [])
        detail["coverage_partial"] = cov.get("partial")
        detail["limitations_n"] = len(result.get("limitations") or [])
        detail["last_turn_dt_s"] = last.get("dt")
        detail["failure_code"] = failure.get("code")
        detail["last_http"] = last.get("status")
    # r["attempts"] is the harness's OWN count, passed in so the walk can reconcile
    # against it rather than be the only witness to what happened.
    detail.update(attempt_diagnostics(outdir, qid, rep, harness_attempts=r.get("attempts")))
    return detail


def main():
    if len(sys.argv) != 4:
        sys.exit("usage: run_shard.py <shard-index> <shard-count> <rep>")
    idx, count, rep = int(sys.argv[1]), int(sys.argv[2]), int(sys.argv[3])

    layout = plan(count)
    ids = layout["shards"][idx]["ids"]

    outdir = Path(os.environ.get("CORPUS_SHARD_DIR") or (HERE / "shards" / f"shard-{idx:02d}"))
    (outdir / "replicate").mkdir(parents=True, exist_ok=True)
    # Point the harness's module-level OUTDIR at THIS shard's directory. The
    # harness writes one JSON per attempt and nothing else; redirecting the
    # directory is the whole of the per-shard isolation it needs.
    harness.OUTDIR = outdir / "replicate"

    rows = []
    t_start = time.time()
    for i, qid in enumerate(ids, 1):
        row = BY_ID[qid]
        print(f"=== shard{idx} [{i}/{len(ids)}] {qid} ===", flush=True)
        t0 = time.time()
        r = harness.run_replicate(qid, row["text"], rep)
        dt = time.time() - t0
        print(f"  -> attempts={r['attempts']} dt={dt:.1f}s chain={r['chain']}", flush=True)
        d = detail_for(harness.OUTDIR, qid, row, r, dt, rep)
        if UNSEQUENCED.get(qid):
            d["unsequenced_files"] = sorted(set(UNSEQUENCED[qid]))
        rows.append(d)

    total = time.time() - t_start
    out = {
        "shard": idx,
        "shard_count": count,
        "rep": rep,
        "planned_ids": ids,
        "rows": rows,
        "total_wall_seconds": round(total, 1),
        "started_unix": round(t_start, 3),
        "finished_unix": round(time.time(), 3),
    }
    with open(outdir / "shard-summary.json", "w") as f:
        json.dump(out, f, indent=2)

    # A shard that did not produce a row per planned id is a driver failure, not
    # a measurement: fail loudly so the merge refuses the run.
    got = {r["corpus_id"] for r in rows}
    missing = [q for q in ids if q not in got]
    if missing:
        print(f"SHARD {idx} INCOMPLETE, missing {len(missing)} ids", file=sys.stderr, flush=True)
        sys.exit(2)
    print(f"DONE shard{idx} {len(rows)} rows in {total:.1f}s -> {outdir}/shard-summary.json")


if __name__ == "__main__":
    main()
