#!/usr/bin/env python3
"""Enumerate the attempt classifier's INPUT SHAPE SPACE and emit it as data.

CHAOS-5380. Six defects in this change lived in cells of this space, and four of them lived
in axes nobody had enumerated at all -- the space was not merely under-covered, it had the
wrong number of dimensions. So the space is generated rather than described, and committed
so a reviewer can read it: an earlier round was pointed at an UNTRACKED copy of this file
and correctly reported it absent, because untracked files do not reach a fresh worktree.

WHAT THIS FILE DOES AND DOES NOT MEASURE.
  * It enumerates the cross-product of status band x failure-object presence x httpStatus
    presence x code, and records, per cell: `failed()`, the class, the OLD engine_failures
    ladder's kind, the derived ladder's kind, and whether they agree.
  * It does NOT report fixture coverage. An earlier version scanned the pin file's AST for
    one helper name and reported five classes as having no fixture -- false, because pins
    also build attempts as raw dicts and in loops. An instrument that under-reports is the
    defect class this whole change is about, so the guess is not shipped. Coverage is a
    question for an execution-traced run, not for a static scan.

THE ENUMERATIONS ARE DIFFERENT SIZES, and conflating them was an error in an earlier
review packet -- so each is stated with the enumeration it belongs to, and none of them is
a coverage figure. This sweep enumerates 364 cells (13 status bands x 28 failure/code
combinations). The executable equivalence pin in test_findings_5380.py enumerates its OWN
space over its own axes and asserts the cell count it actually walked; read the number
from that file, never from this comment. (This paragraph said "247" while the pin walked
300 -- a stale count in a doc contradicting the tests, found by review round 1. The fix is
to stop restating the other file's number here at all.)
"""
import itertools
import json
import sys
from pathlib import Path

CORPUS = Path(__file__).parent
sys.path.insert(0, str(CORPUS))
import attempt_classes as AC  # noqa: E402

STATUS_BANDS = [
    ("absent", None), ("sub200", 0), ("2xx", 200), ("3xx", 302),
    ("400", 400), ("413", 413), ("422", 422), ("4xx_other", 404),
    ("500", 500), ("502", 502), ("504", 504), ("5xx_other", 503),
    ("unknown", 599),
]
UPSTREAM = [("no_failure_obj", "NOFAIL"), ("failure_no_httpStatus", None),
            ("up_200", 200), ("up_400", 400), ("up_413", 413), ("up_422", 422),
            ("up_500", 500), ("up_502", 502), ("up_504", 504), ("up_599", 599)]
CODES = [("no_code", None), ("generic", "provider_error"),
         ("contract_violation", "acr_contract_violation")]


def build(status, upstream, code):
    attempt = {"request": {}, "dt": 1.0, "response": {}}
    if status is not None:
        attempt["status"] = status
    if upstream != "NOFAIL":
        failure = {}
        if code is not None:
            failure["code"] = code
        if upstream is not None:
            failure["httpStatus"] = upstream
        attempt["response"]["failure"] = failure
    return attempt


def old_ladder(attempt):
    """origin/main's `scan` ladder, for the frozen-ness column.

    The pin does NOT use this transcription -- it reads the original module out of git and
    runs it. This is here so the emitted table carries the comparison per cell.
    """
    fail = (attempt.get("response") or {}).get("failure") or {}
    if not fail:
        st = attempt.get("status")
        if isinstance(st, int) and st >= 400:
            return f"UPSTREAM_{st}" if st in (504, 502, 503) else f"OTHER_{st}"
        return None
    up = fail.get("httpStatus")
    if up == 500:
        return "ENGINE_INVALID_RESULT"
    if up == 413:
        return "RIG_CEILING_413"
    if up == 504 or attempt.get("status") == 504:
        return "UPSTREAM_504"
    return f"OTHER_{up}"


def main():
    rows, diverged = [], []
    for (sname, status), (uname, upstream), (cname, code) in itertools.product(
            STATUS_BANDS, UPSTREAM, CODES):
        if upstream == "NOFAIL" and code is not None:
            continue                                   # no failure object => no code
        attempt = build(status, upstream, code)
        old, new = old_ladder(attempt), AC.legacy_engine_failure_kind(attempt)
        row = {"status": sname, "upstream": uname, "code": cname,
               "failed": AC.failed(attempt), "class": AC.classify(attempt),
               "old_kind": old, "new_kind": new, "frozen": old == new,
               "upstream_504_n": AC.is_upstream_504(attempt),
               "overrun_413_n": AC.is_overrun_413(attempt)}
        rows.append(row)
        if not row["frozen"]:
            diverged.append(row)

    out = CORPUS / "shape_space.json"
    out.write_text(json.dumps(rows, indent=1) + "\n")
    print(f"cells={len(rows)}  legacy-ladder divergences={len(diverged)}  -> {out.name}")
    for row in diverged:
        print(f"  DIVERGED {row['status']:10} {row['upstream']:22} {row['code']:20} "
              f"old={row['old_kind']} new={row['new_kind']}")
    return 1 if diverged else 0


if __name__ == "__main__":
    raise SystemExit(main())
