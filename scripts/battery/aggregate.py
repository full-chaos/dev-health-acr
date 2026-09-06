#!/usr/bin/env python3
"""Merge per-arm verdicts into the ONE verdict for a battery.

THE MERGE STEP IS THE SINGLE VERDICT. No individual arm's exit code is ever the
battery's exit code: one arm's `go test` rc is not evidence about the battery,
exactly as one shard's rc is not evidence about a corpus. This script reads the
per-arm JSON files that run_arm.sh wrote and computes the run's rc from the
totals alone.

The rc is 0 only when ALL of:
  * the _BASELINE arm passed          (an already-red package reports every
                                       mutant KILLED, so a red baseline makes
                                       every other number meaningless)
  * the _SENTINEL arm passed          (a red sentinel means the harness
                                       false-kills)
  * every mutant in the table has a verdict file  (a missing arm is a
                                       HARNESS_ERROR, never a quiet skip -- a
                                       battery that drops two arms reports a
                                       smaller number that looks like the same
                                       number)
  * harness_error == 0
  * unexpected_survivors == 0

EXPECTED_SURVIVORS is a list of mutant IDS. Empty (and unset) means NONE -- not
"all" and not "unchecked": with it empty, any survivor at all fails the run. It
is never a count and never a boolean. An id that is not in the table REFUSES,
because a typo'd expected survivor silently admits a real one. An expected
survivor is still COUNTED in `survived`; it is excluded only from
`unexpected_survivors`, which is what the rc reads.
"""

import argparse
import glob
import io
import json
import os
import sys


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--table", required=True, help="normalised JSONL table")
    ap.add_argument("--arms-dir", required=True, help="dir of per-arm JSON verdicts")
    ap.add_argument("--expected-survivors", default="", help="space-separated ids; empty = none")
    ap.add_argument("--execution-shape", default="hosted")
    ap.add_argument("--packages", default="")
    ap.add_argument("--tip", default="")
    ap.add_argument("--floor", default="0")
    ap.add_argument("--summary-out", default="-")
    args = ap.parse_args()

    table = []
    for line in io.open(args.table, encoding="utf-8"):
        if line.strip():
            table.append(json.loads(line))
    ids = [m["id"] for m in table]
    kinds = {m["id"]: m.get("kind", "delete") for m in table}

    expected = [x for x in args.expected_survivors.split() if x]
    unknown = [e for e in expected if e not in ids]
    if unknown:
        print(
            "REFUSING: EXPECTED_SURVIVORS names %s, which %s not in the table -- a typo'd "
            "expected survivor silently admits a real one"
            % (", ".join(unknown), "is" if len(unknown) == 1 else "are"),
            file=sys.stderr,
        )
        return 2

    verdicts = {}
    for path in sorted(glob.glob(os.path.join(args.arms_dir, "**", "*.json"), recursive=True)):
        try:
            v = json.loads(io.open(path, encoding="utf-8").read())
        except Exception as exc:  # noqa: BLE001
            print("skipping unreadable verdict %s: %s" % (path, exc), file=sys.stderr)
            continue
        if isinstance(v, dict) and "id" in v:
            verdicts[v["id"]] = v

    def state_of(arm):
        v = verdicts.get(arm)
        if v is None:
            return None
        return v.get("state")

    baseline = state_of("_BASELINE")
    sentinel = state_of("_SENTINEL")

    killed = survived = harness = killed_replacement = 0
    unexpected = []
    rows = []
    for mid in ids:
        v = verdicts.get(mid)
        if v is None:
            state, detail = "HARNESS_ERROR", "NO VERDICT ARTIFACT -- the arm never reported"
            ran = named = 0
        else:
            state = v.get("state", "HARNESS_ERROR")
            detail = v.get("detail", "")
            ran = v.get("ran", 0)
            named = v.get("named_failures", 0)
        if state == "KILLED":
            killed += 1
            if kinds.get(mid) == "replace":
                killed_replacement += 1
        elif state == "SURVIVED":
            survived += 1
            if mid in expected:
                detail += " [EXPECTED]"
            else:
                unexpected.append(mid)
        else:
            state = "HARNESS_ERROR"
            harness += 1
        rows.append((mid, kinds.get(mid, "delete"), state, ran, named, detail))

    lines = []
    lines.append("mutation battery summary")
    lines.append("execution_shape=%s" % args.execution_shape)
    lines.append("tip=%s" % args.tip)
    lines.append("packages=%s" % args.packages)
    lines.append("floor=%s (SUM over the package list)" % args.floor)
    lines.append("arms_in_table=%d arms_reported=%d" % (len(ids), sum(1 for i in ids if i in verdicts)))
    lines.append("baseline=%s" % (baseline or "MISSING"))
    lines.append("sentinel=%s" % (sentinel or "MISSING"))
    lines.append("expected_survivors=%s" % (" ".join(expected) if expected else "none"))
    lines.append("unexpected_survivors=%s" % (" ".join(unexpected) if unexpected else "none"))
    lines.append("")
    lines.append(
        "killed=%d (of which replacement=%d) survived=%d harness_error=%d"
        % (killed, killed_replacement, survived, harness)
    )
    lines.append("")
    lines.append("%-34s %-8s %-14s %-7s %-7s %s" % ("ID", "KIND", "STATE", "RAN", "NAMED", "DETAIL"))
    for mid, kind, state, ran, named, detail in rows:
        lines.append("%-34s %-8s %-14s %-7s %-7s %s" % (mid, kind, state, ran, named, detail[:160]))

    problems = []
    if baseline != "PASS":
        problems.append("BASELINE is %s -- every mutant verdict in this run is void" % (baseline or "MISSING"))
    if sentinel != "PASS":
        problems.append("SENTINEL is %s -- the harness may be false-killing" % (sentinel or "MISSING"))
    if harness:
        problems.append("%d harness error(s)" % harness)
    if unexpected:
        problems.append("%d unexpected survivor(s): %s" % (len(unexpected), " ".join(unexpected)))
    if problems:
        lines.append("")
        for p in problems:
            lines.append("FAIL: %s" % p)

    text = "\n".join(lines) + "\n"
    if args.summary_out == "-":
        sys.stdout.write(text)
    else:
        io.open(args.summary_out, "w", encoding="utf-8").write(text)
        sys.stdout.write(text)

    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
