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
from attempt_order import order_attempts  # noqa: E402
from validators import load_attempt  # noqa: E402
from corpus import CORPUS  # noqa: E402
from shard_plan import plan  # noqa: E402

BY_ID = {row["id"]: row for row in CORPUS}


def attempt_files(outdir, qid, rep):
    # r4: THE shared ordering helper. A bare sorted() here put `t10` before `t9`, so
    # last_attempt() -- and therefore the shard summary's terminal result -- named the
    # wrong file. The identical defect was fixed in subject_identity and left here.
    ordered, _unsequenced = order_attempts(
        glob.glob(str(outdir / f"{qid}-rep{rep}-t*-a*.json")), on_unparseable="skip")
    return ordered


def last_attempt_file(outdir, qid, rep):
    files = attempt_files(outdir, qid, rep)
    return files[-1] if files else None


def attempt_diagnostics(outdir, qid, rep):
    """Per-ATTEMPT rig diagnostics, scanned across every attempt of the row.

    NOT part of the field set compared against lane-corpus-sweep-1 — the 09-05
    baseline summary has no counterpart for these, so they are reported for the
    after-arms only and are NEVER differenced against the baseline. They exist
    because the row's terminal status hides them: a 504 that the harness retried
    into a 200 leaves no trace in final_http, and the ramp smoke's reporter
    under-counted deadlines 2->0 and 3->0 for exactly that reason.

    upstream_504_n   attempts whose upstream deadline'd (per the deadline ruling
                     reclassification ruling reads this, not final_http)
    overrun_413_n    attempts ACR rejected with an items/axis overrun; the
                     baseline recorded ZERO 413s, so any non-zero value here is
                     a real change and gets its own row in the deliverable
    """
    n504 = n413 = 0
    overrun = None
    for f in attempt_files(outdir, qid, rep):
        ok, a, _ = load_attempt(f)
        if not ok:
            continue
        failure = (a.get("response") or {}).get("failure") or {}
        up = failure.get("httpStatus")
        if a.get("status") == 504 or up == 504:
            n504 += 1
        if up == 413:
            n413 += 1
            overrun = {
                "axis": (failure.get("narrowerContinuation") or {}).get("axis"),
                "family": (failure.get("narrowerContinuation") or {}).get("family"),
                "overrun": failure.get("overrun"),
                "measured_items": failure.get("measuredItems"),
                "max_items": failure.get("maxItems"),
            }
    return {"attempt_upstream_504_n": n504, "attempt_overrun_413_n": n413,
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
    detail.update(attempt_diagnostics(outdir, qid, rep))
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
        rows.append(detail_for(harness.OUTDIR, qid, row, r, dt, rep))

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
