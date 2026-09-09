#!/usr/bin/env python3
"""post-hoc reader for per-attempt rig failures. READ-ONLY.

Deliberately NOT part of run_shard/merge: the instrument that produces the
numbers is frozen between the sequential control and the parallel run, so only
the execution SHAPE differs between them. This reads the attempt JSONs both runs
already wrote and reports the failures a row's terminal status hides.

Feeds two things the deliverable owes:
  * the engine-defect ticket ("driver IDs must be unique"): row id + ACR request
    id per occurrence, so the ticket carries a count and not an anecdote;
  * the 30-item ceiling disclosure: which rows 413'd and what they measured.

Usage: engine_failures.py <run-dir> [<run-dir> ...]
       (a run dir is anything with */replicate/*.json under it, e.g. seq/ or shards/)
"""
import json, sys
from pathlib import Path
from collections import Counter
import attempt_classes
from validators import load_attempt

def scan(root):
    out = []
    for f in sorted(Path(root).rglob("replicate/*.json")):
        # r5: THE loader. This module used to decode and trust the artefact itself,
        # which is how a malformed envelope reached a field access and crashed. It now
        # shares one decoder with identity and the shard writer, so the three cannot
        # hold different opinions about the same file.
        ok, a, reason = load_attempt(f)
        qid = f.stem.split("-rep")[0]
        if not ok:
            out.append({"corpus_id": qid, "kind": "UNREADABLE_ARTEFACT",
                        "detail": reason, "file": str(f)})
            continue
        # CHAOS-5380: the kind ladder that used to live here is now
        # attempt_classes.legacy_engine_failure_kind, shared with
        # run_shard.attempt_diagnostics. Two modules counting attempts with two
        # ladders agreed only by accident, and every instrument defect on this seam
        # has been two counters disagreeing about one artefact. The STRINGS are
        # unchanged on purpose -- they key post_hoc_attempt_classes in the merged
        # verdict, which is an artefact of record -- and a pin holds every branch,
        # including the old ladder's own 502/503 inconsistency.
        #
        # r1 #10 is preserved inside that helper: a gateway 504 often carries a
        # non-JSON body, so there is no parsed `failure` object at all. Skipping on
        # that dropped the attempt entirely -- run_shard counted it while this
        # scanner reported nothing, so the two counters disagreed and the request
        # evidence was lost.
        kind = attempt_classes.legacy_engine_failure_kind(a)
        if kind is None:
            continue
        resp = (a.get("response") or {})
        fail = resp.get("failure") or {}
        rec = {"corpus_id": qid, "kind": kind,
               "attempt_http": a.get("status"),
               "upstream_http": fail.get("httpStatus") if fail else a.get("status"),
               "upstream_code": fail.get("upstreamCode"), "code": fail.get("code"),
               "request_id": (fail.get("upstreamRequestId") if fail
                              else (resp.get("request_id") if isinstance(resp, dict) else None)),
               "dt": a.get("dt"), "file": str(f)}
        if not fail:
            rec["detail"] = "attempt carried no parsed failure object"
        if kind == "RIG_CEILING_413":
            rec["measured_items"] = fail.get("measuredItems")
            rec["max_items"] = fail.get("maxItems")
            rec["axis"] = (fail.get("narrowerContinuation") or {}).get("axis")
        out.append(rec)
    return out


def main():
    if len(sys.argv) < 2:
        sys.exit(__doc__)
    rows = [r for d in sys.argv[1:] for r in scan(d)]
    if not rows:
        print("no per-attempt failures found")
        return
    print(f"{len(rows)} failing attempts across {len({r['corpus_id'] for r in rows})} rows\n")
    for kind, n in Counter(r["kind"] for r in rows).most_common():
        print(f"  {kind:24s} {n:3d} attempts over "
              f"{len({r['corpus_id'] for r in rows if r['kind'] == kind}):2d} rows")
    for kind in ("ENGINE_INVALID_RESULT", "RIG_CEILING_413", "UNREADABLE_ARTEFACT"):
        sel = [r for r in rows if r["kind"] == kind]
        if not sel:
            continue
        print(f"\n=== {kind} — one line per occurrence (ids only, never question text) ===")
        for r in sel:
            extra = (f" measured={r.get('measured_items')}/{r.get('max_items')} axis={r.get('axis')}"
                     if kind == "RIG_CEILING_413" else "")
            print(f"  {r['corpus_id']:38s} req={r.get('request_id')}{extra}")

if __name__ == "__main__":
    main()
