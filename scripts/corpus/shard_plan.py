#!/usr/bin/env python3
"""the corpus programme shard layout — PLAN ONLY. Reaches no rig, no DB, no credential.

Mirrors run-two-turn-parallel.sh's ACR_TRIAL_SHARD_PLAN_ONLY knob: the layout is
the one piece of the parallel path with real logic in it, so it is made
inspectable and testable on its own rather than only observable through a live
run. `test_shard_plan.py` is its unit test (the sibling of acr's
scripts/trial/test-shard-plan.sh).

LAYOUT RULE: family-balanced round-robin. The 36 rows are grouped by question
family, families are walked in a stable order, and each family's ids are dealt
one at a time across the shards. This matters because families are NOT
equal-cost: grouped_cohort_status carries both known synthesis-timeout rows
(60,001 ms each) while subject_investigation rows finish in single-digit
seconds. A naive contiguous split puts every expensive row in one shard and
wall time becomes that shard.

Usage:
  python3 shard_plan.py <shard-count>        # prints the layout as JSON
  python3 shard_plan.py <shard-count> --ids  # prints "<shard>\t<id>" lines
"""
import json
import sys
from collections import OrderedDict
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))


def _corpus():
    """The corpus, imported lazily.

    r1 #8/#9: this used to be a module-level `from corpus import CORPUS`, which made
    shard_plan unimportable without a corpus on sys.path and forced every caller that
    wanted to exercise the planner to install one globally. Importing lazily lets the
    layout be verified against arbitrary rows without touching sys.modules.
    """
    from corpus import CORPUS
    return CORPUS


def families(rows=None):
    """id lists grouped by family, in first-appearance order (stable)."""
    out = OrderedDict()
    for row in (rows if rows is not None else _corpus()):
        out.setdefault(row.get("family") or "_none", []).append(row["id"])
    return out


def plan(shard_count, rows=None):
    if shard_count < 1:
        raise ValueError("shard-count must be >= 1")
    rows = rows if rows is not None else _corpus()
    fams = families(rows)
    shards = [[] for _ in range(shard_count)]
    cursor = 0
    for _family, ids in fams.items():
        for qid in ids:
            shards[cursor % shard_count].append(qid)
            cursor += 1

    total = sum(len(s) for s in shards)
    assert total == len(rows), f"layout dropped rows: {total} != {len(rows)}"
    flat = [q for s in shards for q in s]
    assert len(set(flat)) == total, "layout duplicated a row across shards"

    return {
        "corpus_rows": len(rows),
        "shard_count": shard_count,
        "families": {k: len(v) for k, v in fams.items()},
        "shards": [
            {"shard": i, "n": len(s), "ids": s} for i, s in enumerate(shards)
        ],
    }


def main():
    if len(sys.argv) < 2:
        sys.exit("usage: shard_plan.py <shard-count> [--ids]")
    p = plan(int(sys.argv[1]))
    if "--ids" in sys.argv:
        for s in p["shards"]:
            for qid in s["ids"]:
                print(f"{s['shard']}\t{qid}")
    else:
        print(json.dumps(p, indent=2))


if __name__ == "__main__":
    main()
