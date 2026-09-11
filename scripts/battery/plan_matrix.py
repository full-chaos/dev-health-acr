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

# The _SENTINEL arm (run_arm.sh) proves the apply mechanism is inert by
# appending `// mutation-battery sentinel: an inert comment...` to its target
# file. A trailing `//` line is legal Go anywhere after the last top-level
# declaration -- but it is not a comment in JSON or YAML at all, it is
# trailing garbage that breaks every consumer's parse. Hit live: PR #501's
# battery picked a .json mutant's own file as the sentinel target (the old
# default, mutants[0]["file"], with no language check), and the sentinel
# arm went HARNESS_ERROR with 119 named failures -- all of them downstream of
# the same broken JSON parse, none a real finding, voiding a run whose 11
# real mutants had all correctly reported KILLED.
#
# internal/version/version.go is the FIXED fallback for a table with no .go
# mutant at all: a small leaf package (build metadata only), not the subject
# of any mutation battery in this repo, and proven safe by
# test_plan_matrix.py's executed pin (appends the real sentinel text, runs
# `go vet` on it, asserts rc=0).
SENTINEL_FALLBACK_GO_FILE = "internal/version/version.go"

_SENTINEL_UNSAFE_SUFFIXES = (".json", ".yaml", ".yml")


def choose_sentinel_file(mutants, override):
    """Pick the _SENTINEL arm's target file.

    An explicit --sentinel-file override is honoured but never allowed to be
    JSON/YAML -- an appended `//` line breaks JSON/YAML parsing outright, and
    for a byte-identical MIRROR PIN (e.g. the internal/mcp/schemas/ copies a
    sync check compares byte-for-byte against their canonical source) even a
    single appended WHITESPACE line would false-kill the sync check, which is
    exactly the false-positive class this arm exists to rule out.

    With no override: the first .go file named by any mutant in the table (so
    the sentinel exercises the SAME package family a Go-targeting battery is
    actually touching), or SENTINEL_FALLBACK_GO_FILE when the table has no .go
    mutant at all (e.g. a schema-only table like #501's).
    """
    if override:
        if override.endswith(_SENTINEL_UNSAFE_SUFFIXES):
            raise ValueError(
                "--sentinel-file %r is JSON/YAML -- an appended // line is not an "
                "inert edit for a non-Go file (it breaks parsing outright, or for a "
                "byte-identical mirror pin, false-kills on a whitespace-only change)"
                % override
            )
        return override
    go_mutant_files = [m["file"] for m in mutants if m["file"].endswith(".go")]
    if go_mutant_files:
        return go_mutant_files[0]
    return SENTINEL_FALLBACK_GO_FILE


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--table", required=True)
    ap.add_argument("--sentinel-file", default="", help="file the _SENTINEL arm appends to")
    ap.add_argument(
        "--only-arms",
        default="",
        help="comma-separated mutant ids to measure instead of the whole table. "
        "_BASELINE and _SENTINEL always run: they are the validity controls, and a "
        "subset measured without them is uninterpretable.",
    )
    args = ap.parse_args()

    with io.open(args.table, encoding="utf-8") as f:
        mutants = [json.loads(l) for l in f if l.strip()]
    if not mutants:
        print("REFUSING: the table names no mutants", file=sys.stderr)
        return 2

    only = [x.strip() for x in args.only_arms.split(",") if x.strip()]
    if only:
        known = {m["id"] for m in mutants}
        unknown = [o for o in only if o not in known]
        if unknown:
            print(
                "REFUSING: only_arms names %s, which %s not in the table -- a typo would "
                "silently measure fewer arms than asked for"
                % (", ".join(unknown), "is" if len(unknown) == 1 else "are"),
                file=sys.stderr,
            )
            return 2
        chosen = [m for m in mutants if m["id"] in only]
    else:
        chosen = mutants

    # _BASELINE and _SENTINEL run on EVERY invocation, subset or not. They are
    # what makes any verdict interpretable: without a green baseline every arm
    # reads as KILLED, and without a green sentinel the harness may be
    # false-killing. A subset that skipped them would be cheaper and worthless.
    entries = [{"id": "_BASELINE"}, {"id": "_SENTINEL"}]
    entries += [{"id": m["id"]} for m in chosen]

    if len(entries) > MATRIX_CAP:
        print(
            "REFUSING: %d matrix entries exceeds GitHub's cap of %d -- split the table"
            % (len(entries), MATRIX_CAP),
            file=sys.stderr,
        )
        return 2

    try:
        sentinel_file = choose_sentinel_file(mutants, args.sentinel_file)
    except ValueError as exc:
        print("REFUSING: %s" % exc, file=sys.stderr)
        return 2

    print(json.dumps({"include": entries}))
    print("arm_count=%d" % len(chosen), file=sys.stderr)
    print("table_arm_count=%d" % len(mutants), file=sys.stderr)
    print("run_scope=%s" % ("partial" if only else "full"), file=sys.stderr)
    print("matrix_size=%d" % len(entries), file=sys.stderr)
    print("sentinel_file=%s" % sentinel_file, file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
