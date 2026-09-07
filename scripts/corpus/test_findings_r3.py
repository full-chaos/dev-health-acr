"""RED-FIRST pins for round 3. These assert INVARIANTS over whole domains, not cases.

Rounds 1-3 each found the same class: a pin written to the reviewer's example proved the
narrow case and left its neighbours open. So these enumerate:
  - the full (expect x terminal_status) domain, asserting fail-closed BY CONSTRUCTION;
  - every ordering of {result, failure, unparseable} up to length 3;
  - a URL table;
  - sys.modules isolation checked through a DEPENDENT module, not the `corpus` key.
"""
import itertools
import json
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).parent
sys.path.insert(0, str(HERE))

import corpus_example                                   # noqa: E402
from corpus_stub import using_example_corpus            # noqa: E402
import expectations as E                                # noqa: E402
import subject_identity as SI                           # noqa: E402


def _row(**kw):
    d = {"id": "r", "text": "synthetic", "family": "f", "variant": "v",
         "member_kind": None, "group_kind": None, "requested_kind": None,
         "anchor_kind": None, "note": "", "expect": None, "basis": None,
         "anchor": None, "nonexistent": False}
    d.update(kw)
    return d


def _attempt(kind, committed=None, facts=0):
    if kind == "unparseable":
        return None                                     # written as raw bytes
    if kind == "failure":
        return {"request": {}, "status": 422, "dt": 1.0,
                "response": {"failure": {"httpStatus": 422, "code": "acr_answer_rejected"}}}
    cands = [{"state": "committed", "subject": c, "match_mechanisms": [], "matched_terms": []}
             for c in (committed or [])]
    return {"request": {}, "status": 200, "dt": 1.0, "response": {"result": {
        "request_id": "req", "result_id": "res", "status": "partial",
        "claimed_facts": [{"claim_id": f"c{i}"} for i in range(facts)],
        "subject_resolution": {"committed": committed or [], "candidates": cands}}}}


def _write_chain(tmp, kinds, committed_on=None, subdir="replicate"):
    d = Path(tmp) / subdir
    d.mkdir(parents=True, exist_ok=True)
    for i, k in enumerate(kinds, start=1):
        f = d / f"q-rep1-t{i}-a1.json"
        body = _attempt(k, committed=(committed_on or {}).get(i))
        f.write_text("{not json" if body is None else json.dumps(body))
    return tmp


# ================================================= #4 : the verdict is TOTAL
def test_f4_no_agreement_exists_outside_the_declared_table():
    """Enumerate the WHOLE domain. Nothing may reach agree/agree_weak unless VERDICTS
    (or the named-basis override) names that cell."""
    statuses = sorted(E.TERMINAL_STATUSES) + [None, "", "weird_status", "timeout"]
    buckets = ["served_with_data", "served_degraded", "unserved",
               "clarification_needed", "error", None, "nonsense"]
    leaked = []
    for cls in (E.SERVE, E.REFUSE, E.DECLINE, E.CLARIFY):
        for basis in (None, "named_basis"):
            for st in statuses:
                for bk in buckets:
                    e = {"expectation": cls, "expectation_basis": basis}
                    v, why = E.score(e, bk, terminal_status=st)
                    if v in ("agree", "agree_weak"):
                        key = E._bucket_of(st, bk)
                        if key is None:
                            key = {"served_with_data": "served_with_data",
                                   "served_degraded": "served_degraded",
                                   "clarification_needed": "clarification"}.get(bk)
                        named = (cls, key) in E.NAMED_BASIS_OVERRIDES and basis
                        if key is None or ((cls, key) not in E.VERDICTS and not named):
                            leaked.append((cls, basis, st, bk, v))
    assert not leaked, f"agreement reached outside the table: {leaked[:8]}"


def test_f4_an_absent_or_unknown_terminal_status_is_never_agreement():
    for cls in (E.SERVE, E.REFUSE, E.DECLINE, E.CLARIFY):
        e = {"expectation": cls, "expectation_basis": None}
        for st in (None, "", "weird_status"):
            v = E.score(e, "unserved", terminal_status=st)[0]
            assert v == "unscored", f"{cls} + {st!r} -> {v}"


def test_f4_unscored_rows_stay_unscored_whatever_happens():
    e = {"expectation": E.UNSCORED, "expectation_basis": None}
    for st in sorted(E.TERMINAL_STATUSES) + [None]:
        assert E.score(e, "served_with_data", terminal_status=st)[0] == "unscored"


# ================================================= #3 : no text parsing at all
def test_f3_the_note_no_longer_influences_the_expectation():
    """Every note that previously misparsed must now be inert."""
    for note in ("not only SERVABLE", "not expected to be SERVABLE", "expect not to decline",
                 "unexpected refusal", "SERVABLE", "expect refuse basis=member_kind_unservable",
                 "expect decline with a named basis", "anchor=Platform/team"):
        got = E.expectation_for(_row(note=note))["expectation"]
        assert got == E.UNSCORED, f"note {note!r} still drove expectation {got!r}"


def test_f3_the_structured_field_is_what_is_read():
    assert E.expectation_for(_row(expect="serve"))["expectation"] == E.SERVE
    assert E.expectation_for(_row(expect="refuse"))["expectation"] == E.REFUSE
    assert E.expectation_for(_row(expect="decline", basis="named_basis"))["expectation_basis"] == "named_basis"
    a = E.expectation_for(_row(anchor={"kind": "team", "label": "Fullchaos"}))
    assert (a["declared_anchor_name"], a["declared_anchor_kind"]) == ("Fullchaos", "team")
    assert E.expectation_for(_row())["expectation"] == E.UNSCORED


def test_f3_no_regex_module_use_remains_in_the_scorer():
    t = (HERE / "expectations.py").read_text()
    assert "import re" not in t, "the scorer still imports a regex engine"


# ================================================= #2 : one classifier, history matrix
def test_f2_attempt_classifier_covers_every_envelope():
    cases = {
        "result": {"response": {"result": {"x": 1}}},
        "failure": {"response": {"failure": {"code": "x"}}},
        "failure_http": {"status": 504, "response": {}},
        "unparseable_empty": {"response": {}},
        "unparseable_none": {"response": None},
        "unparseable_type": "not a dict",
    }
    want = {"result": "result", "failure": "failure", "failure_http": "failure",
            "unparseable_empty": "unparseable", "unparseable_none": "unparseable",
            "unparseable_type": "unparseable"}
    got = {k: SI.classify_attempt(v)[0] for k, v in cases.items()}
    assert got == want, got


def test_f2_history_matrix_every_ordering_up_to_three():
    """A row is readable iff its TERMINAL attempt parsed; an earlier unparseable attempt
    caps the verdict but never makes the row unreadable, and a failure never does."""
    kinds = ("result", "failure", "unparseable")
    bad = []
    for n in (1, 2, 3):
        for chain in itertools.product(kinds, repeat=n):
            with tempfile.TemporaryDirectory() as tmp:
                _write_chain(tmp, chain)
                e = E.expectation_for(_row(id="q", expect="serve"))
                rec = SI.inspect(tmp, "q", e)
                terminal_unparseable = chain[-1] == "unparseable"
                any_unparseable = "unparseable" in chain
                if terminal_unparseable or any_unparseable:
                    if rec["state"] == "read":
                        bad.append((chain, rec["state"], "should be tainted"))
                else:
                    if rec["state"] != "read":
                        bad.append((chain, rec["state"], "should be read"))
                v = E.score(e, "served_with_data", identity_state=rec["state"],
                            terminal_status="partial")[0]
                if any_unparseable and v == "agree":
                    bad.append((chain, rec["state"], "agree escaped the cap"))
    assert not bad, f"{len(bad)} bad chains, first few: {bad[:5]}"


# ================================================= #6 : ordering by sequence
def test_f6_replay_attempt_wins_over_the_original_it_replaces():
    with tempfile.TemporaryDirectory() as tmp:
        root = Path(tmp) / "shards"
        shard = root / "shard-00"
        (shard / "replicate").mkdir(parents=True)
        (shard / "replicate" / "q-rep1-t1-a1.json").write_text(json.dumps(
            {"request": {}, "status": 200, "dt": 1, "response": {"result": {
                "request_id": "parallel", "result_id": "r1", "status": "no_match",
                "claimed_facts": [], "subject_resolution": {"committed": [], "candidates": []}}}}))
        rp = root / SI.REPLAY_HINT / "q" / "replicate"
        rp.mkdir(parents=True)
        (rp / "q-rep1-t1-a1.json").write_text(json.dumps(
            {"request": {}, "status": 200, "dt": 1, "response": {"result": {
                "request_id": "replay", "result_id": "r2", "status": "partial",
                "claimed_facts": [{"claim_id": "c"}],
                "subject_resolution": {"committed": [], "candidates": []}}}}))
        rec = SI.inspect(str(root), "q", E.expectation_for(_row(id="q", expect="serve")))
        assert rec["request_id"] == "replay", f"selected {rec['request_id']} from {rec['artefact']}"
        assert rec["status"] == "partial", rec["status"]


def test_f6_ordering_is_not_lexicographic_on_the_path():
    t = (HERE / "subject_identity.py").read_text()
    assert "_attempt_order" in t and "key=_attempt_order" in t, \
        "attempts are still ordered by bare path sort"


# ================================================= #9 : isolation via dependents
def test_f9_no_test_module_leaks_a_synthetic_corpus_into_a_dependent():
    real = "/home/ubuntu/.cache/acr-kiac-askdev/proofs/2026-09-07-corpus-arm3/instrument-v2"
    mods = ["test_shard_plan", "test_findings_r1", "test_findings_r2",
            "test_findings_r3", "test_instrument"]
    code = (
        "import sys, importlib\n"
        f"sys.path.insert(0, {str(HERE)!r})\n"
        f"sys.path.append({real!r})\n"
        + "".join(f"importlib.import_module({m!r})\n" for m in mods) +
        "import merge_corpus\n"
        "print('ROWS', len(merge_corpus.CORPUS))\n")
    out = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True)
    assert "ROWS 36" in out.stdout, (
        f"a dependent resolved to a synthetic corpus after importing the test modules: "
        f"{out.stdout.strip()!r} {out.stderr.strip()[-200:]!r}")


def test_f9_the_stub_restores_sys_modules_exactly():
    before = dict(sys.modules)
    with using_example_corpus():
        import merge_corpus  # noqa: F401
    assert set(sys.modules) == set(before), "sys.modules was not restored exactly"


if __name__ == "__main__":
    fails = 0
    for name, fn in sorted(globals().items()):
        if name.startswith("test_") and callable(fn):
            try:
                fn()
                print(f"PASS  {name}")
            except Exception as exc:
                fails += 1
                print(f"FAIL  {name}: {type(exc).__name__}: {str(exc)[:180]}")
    print(f"\n{fails} failing")
    raise SystemExit(1 if fails else 0)
