"""RED-FIRST pins for round 4, and the GOLDEN TABLE.

The previous round's domain pin could not detect the defect it was written for, because it
recomputed the implementation's own fallback to decide what the table "should" say. A pin
that derives its expectation from the code under test can only ever agree with that code.

GOLDEN below is written by hand from the specification, independently of `expectations.py`.
It is the authority; the code is compared to it. Nothing here calls `terminal_key`,
`VERDICTS`, or any other helper of the module under test to decide what to expect.
"""
import json
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).parent
sys.path.insert(0, str(HERE))

import expectations as E          # noqa: E402
import subject_identity as SI     # noqa: E402
import engine_failures            # noqa: E402
from attempt_order import order_attempts  # noqa: E402
from validators import validate_attempt, validate_corpus_row  # noqa: E402

# --------------------------------------------------------------------------- GOLDEN
# (expect, terminal_status) -> verdict, for a row with NO declared basis and a readable
# identity. Written from the spec by hand. `served_*` rows use the bucket only to choose
# between the two served keys, so both spellings are listed explicitly.
GOLDEN = {
    ("serve", "complete", "served_with_data"): "agree",
    ("serve", "partial", "served_with_data"): "agree",
    ("serve", "answered", "served_with_data"): "agree",
    ("serve", "degraded", "served_degraded"): "agree_weak",
    ("serve", "partial", "served_degraded"): "agree_weak",
    ("serve", "no_match", "unserved"): "disagree",
    ("serve", "refused", "unserved"): "disagree",
    ("serve", "clarification_required(max_turns_exhausted)", "clarification_needed"): "disagree",
    ("serve", "http_502:x", "error"): "disagree",

    ("refuse", "refused", "unserved"): "agree",
    ("refuse", "no_match", "unserved"): "agree",
    ("refuse", "complete", "served_with_data"): "disagree",
    ("refuse", "degraded", "served_degraded"): "disagree",
    ("refuse", "clarification_required(max_turns_exhausted)", "clarification_needed"): "agree_weak",
    ("refuse", "http_502:x", "error"): "disagree",

    ("decline", "refused", "unserved"): "agree",
    ("decline", "no_match", "unserved"): "agree",
    ("decline", "complete", "served_with_data"): "disagree",
    ("decline", "degraded", "served_degraded"): "disagree",
    ("decline", "clarification_required(max_turns_exhausted)", "clarification_needed"): "agree_weak",
    ("decline", "http_502:x", "error"): "disagree",

    ("clarify", "clarification_required(max_turns_exhausted)", "clarification_needed"): "agree",
    ("clarify", "complete", "served_with_data"): "disagree",
    ("clarify", "degraded", "served_degraded"): "disagree",
    ("clarify", "no_match", "unserved"): "disagree",
    ("clarify", "refused", "unserved"): "disagree",
    ("clarify", "http_502:x", "error"): "disagree",
}

# A declared named basis changes exactly one cell.
GOLDEN_NAMED_BASIS = {("decline", "no_match", "unserved"): "disagree"}

# Terminal statuses that carry no meaning to the scorer. NONE of these may ever score.
UNRECOGNISED = [None, "", "future_status", "weird", 0, [], {}, True]


def test_golden_table_matches_the_implementation_cell_for_cell():
    bad = []
    for (cls, term, bucket), want in GOLDEN.items():
        got = E.score({"expectation": cls, "expectation_basis": None}, bucket,
                      terminal_status=term)[0]
        if got != want:
            bad.append((cls, term, bucket, want, got))
    for (cls, term, bucket), want in GOLDEN_NAMED_BASIS.items():
        got = E.score({"expectation": cls, "expectation_basis": "named_basis"}, bucket,
                      terminal_status=term)[0]
        if got != want:
            bad.append((cls, term, bucket, want, got, "named_basis"))
    assert not bad, f"{len(bad)} cells disagree with the golden table: {bad[:6]}"


def test_no_cell_outside_the_golden_table_ever_scores():
    """The complement of GOLDEN. Every combination NOT in the hand-written table must be
    `unscored` -- this is the assertion the previous pin could not make, because it asked
    the code what the table said."""
    buckets = ["served_with_data", "served_degraded", "unserved", "clarification_needed",
               "error", None, "nonsense"]
    leaked = []
    for cls in ("serve", "refuse", "decline", "clarify"):
        for term in UNRECOGNISED:
            for bucket in buckets:
                v = E.score({"expectation": cls, "expectation_basis": None}, bucket,
                            terminal_status=term)[0]
                if v != "unscored":
                    leaked.append((cls, term, bucket, v))
    assert not leaked, f"unrecognised terminal statuses scored: {leaked[:8]}"


def test_the_bucket_can_never_rescue_an_absent_terminal_status():
    """r4 repro: terminals {} -> agree; 'future_status' -> agree."""
    for bucket in ("served_with_data", "served_degraded", "unserved",
                   "clarification_needed", "error"):
        for term in (None, "future_status"):
            v = E.score({"expectation": "serve", "expectation_basis": None}, bucket,
                        terminal_status=term)[0]
            assert v == "unscored", f"bucket={bucket} terminal={term!r} -> {v}"


def test_table_level_absent_terminals_do_not_score():
    rows = {"q": {"id": "q", "expect": "serve", "basis": None, "anchor": None,
                  "nonexistent": False, "family": "f"}}
    for terminals in ({}, {"q": "future_status"}, {"q": ["list"]}):
        out = E.table(rows, {"q": "served_with_data"}, {"q": False}, terminals_by_id=terminals)
        assert out[0]["verdict"] == "unscored", f"{terminals} -> {out[0]['verdict']}"


# --------------------------------------------------------------------------- validation
def test_malformed_declarations_are_invalid_not_trusted():
    cases = {
        "expect-list": {"id": "x", "expect": ["serve"]},
        "expect-unknown": {"id": "x", "expect": "sorve"},
        "anchor-list": {"id": "x", "anchor": [1]},
        "anchor-partial": {"id": "x", "anchor": {"kind": "team"}},
        "anchor-empty-label": {"id": "x", "anchor": {"kind": "team", "label": ""}},
        "nonexistent-str": {"id": "x", "nonexistent": "false"},
        "nonexistent-int": {"id": "x", "nonexistent": 1},
        "basis-int": {"id": "x", "basis": 7},
        "not-a-row": "nope",
    }
    for label, row in cases.items():
        e = E.expectation_for(row)
        assert e["expectation"] == E.INVALID, f"{label} -> {e['expectation']}"
        assert E.score(e, "served_with_data", terminal_status="partial")[0] == "unscored", label


def test_a_valid_declaration_still_scores():
    row = {"id": "x", "expect": "serve", "basis": None,
           "anchor": {"kind": "team", "label": "T"}, "nonexistent": False}
    e = E.expectation_for(row)
    assert e["expectation"] == E.SERVE
    assert E.score(e, "served_with_data", terminal_status="partial")[0] == "agree"


def test_attempt_envelopes_are_validated_at_ingestion():
    bad = {"result-list": {"response": {"result": [1]}},
           "failure-str": {"response": {"failure": "boom"}},
           "response-list": {"response": [1]},
           "status-str": {"status": "422"},
           "not-a-dict": "nope"}
    for label, a in bad.items():
        assert SI.classify_attempt(a)[0] == SI.UNPARSEABLE, label
        ok, _ = validate_attempt(a)
        assert not ok, label


def test_a_truthy_non_dict_result_cannot_crash_identity():
    with tempfile.TemporaryDirectory() as tmp:
        d = Path(tmp) / "replicate"; d.mkdir(parents=True)
        (d / "q-rep1-t1-a1.json").write_text(json.dumps({"response": {"result": [1]}}))
        rec = SI.inspect(tmp, "q", E.expectation_for(
            {"id": "q", "expect": "serve", "basis": None, "anchor": None, "nonexistent": False}))
        assert rec["state"] == "unreadable_artefact", rec["state"]


def test_engine_failures_reports_an_invalid_envelope_instead_of_crashing():
    with tempfile.TemporaryDirectory() as tmp:
        d = Path(tmp) / "replicate"; d.mkdir(parents=True)
        (d / "q-rep1-t1-a1.json").write_text(json.dumps({"response": {"failure": "boom"}}))
        recs = engine_failures.scan(tmp)
        assert any(r["kind"] == "UNREADABLE_ARTEFACT" for r in recs), recs


# --------------------------------------------------------------------------- ordering
def test_attempt_order_is_numeric_not_lexicographic():
    got = [Path(p).name for p in order_attempts(
        [f"d/q-rep1-t{n}-a1.json" for n in (1, 2, 9, 10, 11)])]
    assert got == [f"q-rep1-t{n}-a1.json" for n in (1, 2, 9, 10, 11)], got


def test_run_shard_uses_the_shared_ordering_helper():
    t = (HERE / "run_shard.py").read_text()
    assert "order_attempts" in t, "run_shard.py does not use the shared ordering helper"
    assert "sorted(glob.glob" not in t, "run_shard.py still path-sorts attempts"


def test_run_shard_last_attempt_picks_t10_over_t9():
    sys.path.insert(0, str(HERE))
    import importlib
    rs = importlib.import_module("run_shard")
    with tempfile.TemporaryDirectory() as tmp:
        d = Path(tmp)
        for n in (9, 10):
            (d / f"q-rep1-t{n}-a1.json").write_text("{}")
        assert Path(rs.last_attempt_file(d, "q", 1)).name == "q-rep1-t10-a1.json"


# --------------------------------------------------------------------------- isolation
def test_isolation_is_by_process_not_by_sys_modules():
    assert not (HERE / "corpus_stub.py").exists(), "corpus_stub.py still exists"
    for name in ("test_shard_plan.py", "test_findings_r1.py", "test_findings_r2.py",
                 "test_findings_r3.py", "test_findings_r4.py", "test_instrument.py"):
        f = HERE / name
        if f.exists():
            t = f.read_text()
            # Look for an ASSIGNMENT or a deletion, not a mention -- this file names the
            # pattern in its own assertion, and a substring check would flag itself.
            import re as _re
            bad = _re.search(r'sys\.modules\[[\'"]corpus[\'"]\]\s*=', t) or \
                  _re.search(r'sys\.modules\.pop\(\s*[\'"]corpus', t)
            assert not bad, f"{name} still mutates sys.modules: {bad.group(0)!r}"


def test_a_fresh_interpreter_without_the_testdata_path_has_no_corpus():
    code = (f"import sys; sys.path.insert(0, {str(HERE)!r});"
            "import expectations, subject_identity, validators, attempt_order;"
            "print('CORPUS' if 'corpus' in sys.modules else 'NOCORPUS')")
    out = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True,
                         env={"PATH": "/usr/bin:/bin"})
    assert out.stdout.strip().endswith("NOCORPUS"), out.stdout.strip()


if __name__ == "__main__":
    fails = 0
    for name, fn in sorted(globals().items()):
        if name.startswith("test_") and callable(fn):
            try:
                fn()
                print(f"PASS  {name}")
            except Exception as exc:
                fails += 1
                print(f"FAIL  {name}: {type(exc).__name__}: {str(exc)[:170]}")
    print(f"\n{fails} failing")
    raise SystemExit(1 if fails else 0)
