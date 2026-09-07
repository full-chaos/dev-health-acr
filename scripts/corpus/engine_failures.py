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

def scan(root):
    out = []
    for f in sorted(Path(root).rglob("replicate/*.json")):
        try:
            a = json.loads(f.read_text())
        except (ValueError, OSError) as e:      # a truncated artefact is reported, never skipped silently
            out.append({"corpus_id": f.stem, "kind": "UNREADABLE_ARTEFACT", "detail": str(e), "file": str(f)})
            continue
        # the SINGLE attempt classifier, shared with identity and the merge, so the
        # three cannot disagree about the same file (round 3 ruling).
        import subject_identity as _si
        _cls, _ = _si.classify_attempt(a)
        fail = (a.get("response") or {}).get("failure") or {}
        if not fail:
            # r1 #10. A gateway 504 often carries a non-JSON body, so there is no parsed
            # `failure` object at all. Skipping on that dropped the attempt entirely --
            # run_shard.attempt_diagnostics() counted it while this scanner reported
            # nothing, so the two counters disagreed and the request evidence was lost.
            # An attempt whose own HTTP status is a failure is classified from that.
            st = a.get("status")
            if isinstance(st, int) and st >= 400:
                out.append({"corpus_id": f.stem.split("-rep")[0],
                            "kind": f"UPSTREAM_{st}" if st in (504, 502, 503) else f"OTHER_{st}",
                            "attempt_http": st, "upstream_http": st,
                            "request_id": (a.get("response") or {}).get("request_id")
                                          if isinstance(a.get("response"), dict) else None,
                            "detail": "attempt carried no parsed failure object",
                            "file": str(f)})
            continue
        qid = f.stem.split("-rep")[0]
        up = fail.get("httpStatus")
        rec = {"corpus_id": qid, "attempt_http": a.get("status"), "upstream_http": up,
               "upstream_code": fail.get("upstreamCode"), "code": fail.get("code"),
               "request_id": fail.get("upstreamRequestId"), "dt": a.get("dt"), "file": str(f)}
        if up == 500:
            rec["kind"] = "ENGINE_INVALID_RESULT"
        elif up == 413:
            rec["kind"] = "RIG_CEILING_413"
            rec["measured_items"] = fail.get("measuredItems")
            rec["max_items"] = fail.get("maxItems")
            rec["axis"] = (fail.get("narrowerContinuation") or {}).get("axis")
        elif up == 504 or a.get("status") == 504:
            rec["kind"] = "UPSTREAM_504"
        else:
            rec["kind"] = f"OTHER_{up}"
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
