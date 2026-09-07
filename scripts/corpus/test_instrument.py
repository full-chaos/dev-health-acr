"""Contract tests for the corpus instrument. No network, no rig, no real corpus.

Installs the synthetic example as the module named `corpus` BEFORE importing
anything that does `from corpus import CORPUS`, so CI exercises the scorer
without the real evaluation questions being present.
"""
import sys
from pathlib import Path

HERE = Path(__file__).parent
sys.path.insert(0, str(HERE))

import corpus_example                      # noqa: E402

import expectations  # noqa: E402
from shard_plan import plan  # noqa: E402

BY_ID = {r["id"]: r for r in corpus_example.CORPUS}


def test_expectation_classes():
    got = {i: expectations.expectation_for(r)["expectation"] for i, r in BY_ID.items()}
    assert got["example-serve-named-project"] == "expect_serve"
    assert got["example-refuse-unservable-kind"] == "expect_refuse"
    assert got["example-decline-nonexistent-team"] == "expect_decline"
    assert got["example-unscored-open-question"] == "unscored"


def test_declared_anchor_and_nonexistent_are_read_from_the_note():
    e = expectations.expectation_for(BY_ID["example-serve-named-project"])
    assert e["declared_anchor_name"] == "Example Project"
    assert e["declared_anchor_kind"] == "project"
    n = expectations.expectation_for(BY_ID["example-decline-nonexistent-team"])
    assert n["declares_nonexistent"] is True
    assert n["forbids_fabrication"] is True


def test_serving_a_refuse_row_is_a_disagreement():
    e = expectations.expectation_for(BY_ID["example-refuse-unservable-kind"])
    assert expectations.score(e, "unserved")[0] == "agree"
    # looping to MAX_TURNS is NOT agreement -- it never terminated
    assert expectations.score(e, "clarification_needed")[0] == "agree_weak"
    assert expectations.score(e, "served_with_data")[0] == "disagree"
    assert expectations.score(e, "served_degraded")[0] == "disagree"


def test_a_substitution_is_a_disagreement_whatever_the_bucket():
    for rid in BY_ID:
        e = expectations.expectation_for(BY_ID[rid])
        if e["expectation"] == "unscored":
            continue
        v, why = expectations.score(e, "served_with_data", subject_substitution=True)
        assert v == "disagree", rid
        assert "substitution" in why


def test_shard_plan_is_total_and_disjoint():
    ids = {r["id"] for r in corpus_example.CORPUS}
    for n in range(1, len(ids) + 1):
        shards = plan(n, corpus_example.CORPUS)["shards"]
        seen = [i for s in shards for i in s["ids"]]
        assert len(seen) == len(ids), n
        assert set(seen) == ids, n
        assert max(s["n"] for s in shards) - min(s["n"] for s in shards) <= 1, n


if __name__ == "__main__":
    fails = 0
    for name, fn in sorted(globals().items()):
        if name.startswith("test_") and callable(fn):
            try:
                fn()
                print(f"PASS  {name}")
            except AssertionError as exc:
                fails += 1
                print(f"FAIL  {name}: {exc}")
    raise SystemExit(1 if fails else 0)
