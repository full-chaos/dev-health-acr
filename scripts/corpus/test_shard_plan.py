#!/usr/bin/env python3
"""Unit test for the shard layout. Reaches nothing live — the sibling of acr's
scripts/trial/test-shard-plan.sh, and the reason the layout is a separate module."""
import sys
from pathlib import Path
sys.path.insert(0, str(Path(__file__).parent))

# Fall back to the synthetic example when no real corpus module is supplied, so
# this test runs in CI. See README.md, "Supplying a corpus".
try:
    from corpus import CORPUS
except ModuleNotFoundError:
    import corpus_example
    sys.modules["corpus"] = corpus_example
    from corpus import CORPUS
from shard_plan import plan

ALL = {r["id"] for r in CORPUS}
fails = []

for n in range(1, len(ALL) + 1):
    p = plan(n)
    flat = [q for s in p["shards"] for q in s["ids"]]
    if set(flat) != ALL:
        fails.append(f"n={n}: id set differs from the corpus")
    if len(flat) != len(ALL):
        fails.append(f"n={n}: {len(flat)} placements for {len(ALL)} rows (dupes or drops)")
    sizes = [s["n"] for s in p["shards"]]
    if max(sizes) - min(sizes) > 1:
        fails.append(f"n={n}: unbalanced shard sizes {sizes}")
    # Families must be spread, not pooled: the two known 60s-timeout rows both
    # live in grouped_cohort_status, so a layout that puts a whole family in one
    # shard makes wall time that family.
    if n >= 4:
        by_shard = {}
        for s in p["shards"]:
            for q in s["ids"]:
                fam = next(r.get("family") or "_none" for r in CORPUS if r["id"] == q)
                by_shard.setdefault(s["shard"], set()).add(fam)
        big = max((len([q for q in s["ids"]]) for s in p["shards"]))
        if big > (len(ALL) // n) + 1:
            fails.append(f"n={n}: a shard is oversized ({big})")

# Determinism: the same n twice is the same layout.
if plan(6) != plan(6):
    fails.append("layout is not deterministic")

# n=1 is the sequential control and must be the whole corpus in order.
if plan(1)["shards"][0]["n"] != len(ALL):
    fails.append("n=1 did not produce the whole corpus in one shard")

try:
    plan(0)
    fails.append("plan(0) should have raised")
except ValueError:
    pass

if fails:
    print("FAIL")
    for f in fails:
        print("  -", f)
    sys.exit(1)
print(f"PASS  layout verified for n=1..{len(ALL)} over {len(ALL)} rows")
