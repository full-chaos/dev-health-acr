#!/usr/bin/env python3
"""Unit test for the shard layout. Reaches nothing live.

r1 #8: the old script asserted only over whatever corpus happened to be importable, so
an empty or one-row corpus printed PASS while proving nothing, and the family-spread
map it built was never asserted on. `verify()` now REFUSES a degenerate corpus and
checks the spread it computes.

r1 #9: importing this module used to install the synthetic corpus into sys.modules and
leave it there, so a later in-process import of merge_corpus could produce a
measurement-shaped verdict from four example rows. The fallback is now scoped to the
call and removed again.
"""
import sys
from contextlib import contextmanager
from pathlib import Path

HERE = Path(__file__).parent
sys.path.insert(0, str(HERE))

from shard_plan import plan  # noqa: E402

MIN_ROWS = 2


@contextmanager
def _corpus_rows():
    """Yield the real corpus if one is supplied, else the synthetic example.

    The example is installed as `corpus` only for the duration of the call, then the
    previous state of sys.modules is restored exactly.
    """
    try:
        from corpus import CORPUS
        yield CORPUS
        return
    except ModuleNotFoundError:
        pass
    import corpus_example
    had = "corpus" in sys.modules
    prev = sys.modules.get("corpus")
    sys.modules["corpus"] = corpus_example
    try:
        yield corpus_example.CORPUS
    finally:
        if had:
            sys.modules["corpus"] = prev
        else:
            sys.modules.pop("corpus", None)


def verify(rows):
    """Assert the layout is total, disjoint, balanced and family-spread. Raises on failure."""
    assert len(rows) >= MIN_ROWS, (
        f"refusing to verify a layout over {len(rows)} row(s): a corpus of fewer than "
        f"{MIN_ROWS} rows cannot distinguish a working planner from a broken one")
    ids = {r["id"] for r in rows}
    assert len(ids) == len(rows), "corpus contains duplicate ids"

    for n in range(1, len(ids) + 1):
        p = plan(n, rows)
        shards = p["shards"]
        flat = [q for s in shards for q in s["ids"]]
        assert len(flat) == len(ids), f"n={n}: {len(flat)} placed, {len(ids)} expected"
        assert set(flat) == ids, f"n={n}: id set changed"
        assert len(flat) == len(set(flat)), f"n={n}: an id appears twice"
        sizes = [s["n"] for s in shards]
        assert max(sizes) - min(sizes) <= 1, f"n={n}: unbalanced {sizes}"
        assert p == plan(n, rows), f"n={n}: not deterministic"

        # r1 #8: assert on the family spread instead of merely computing it. With more
        # shards than members of a family, no shard may hold two of that family.
        by_shard = {}
        for s in shards:
            for q in s["ids"]:
                fam = next(r.get("family") or "_none" for r in rows if r["id"] == q)
                by_shard.setdefault(s["shard"], []).append(fam)
        fams = {}
        for r in rows:
            fams.setdefault(r.get("family") or "_none", 0)
            fams[r.get("family") or "_none"] += 1
        for fam, count in fams.items():
            if n >= count:
                per_shard = [v.count(fam) for v in by_shard.values()]
                assert max(per_shard) <= 1, (
                    f"n={n}: family {fam!r} ({count} rows) piled up {max(per_shard)} "
                    f"in one shard despite {n} shards")
    return True


if __name__ == "__main__":
    with _corpus_rows() as rows:
        verify(rows)
        print(f"PASS  layout verified for n=1..{len(rows)} over {len(rows)} rows")
