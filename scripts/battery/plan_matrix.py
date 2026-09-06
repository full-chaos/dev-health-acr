#!/usr/bin/env python3
"""Emit the GitHub Actions matrix for a battery, from a normalised table.

One matrix entry per mutant, plus the two harness arms (_BASELINE, _SENTINEL)
which run as ordinary matrix entries so they parallelise with everything else.

The layout is printed BEFORE anything is provisioned, so a dispatch can be read
and argued with before it spends fifty runners. A plan that the run is free to
contradict is worse than no plan, so the matrix this prints IS the set of jobs
that run -- there is no dynamic re-assignment afterwards.
"""

import argparse
import io
import json
import sys

# GitHub caps a matrix at 256 jobs. Refusing at the cap is better than having
# the platform silently truncate the run into a smaller battery that reports a
# number looking exactly like the full one.
MATRIX_CAP = 256


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--table", required=True)
    ap.add_argument("--sentinel-file", default="", help="file the _SENTINEL arm appends to")
    args = ap.parse_args()

    mutants = [json.loads(l) for l in io.open(args.table, encoding="utf-8") if l.strip()]
    if not mutants:
        print("REFUSING: the table names no mutants", file=sys.stderr)
        return 2

    entries = [{"id": "_BASELINE"}, {"id": "_SENTINEL"}]
    entries += [{"id": m["id"]} for m in mutants]

    if len(entries) > MATRIX_CAP:
        print(
            "REFUSING: %d matrix entries exceeds GitHub's cap of %d -- split the table"
            % (len(entries), MATRIX_CAP),
            file=sys.stderr,
        )
        return 2

    sentinel_file = args.sentinel_file or mutants[0]["file"]
    print(json.dumps({"include": entries}))
    print("arm_count=%d" % len(mutants), file=sys.stderr)
    print("matrix_size=%d" % len(entries), file=sys.stderr)
    print("sentinel_file=%s" % sentinel_file, file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
