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

import corpus_example                                   # noqa: E402,F401
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
    # CHAOS-5430: receipt_id is REQUIRED (observed on every one of 1597 real
    # candidates and dereferenced unconditionally by the harness). A fixture without
    # it is an artefact the engine never emits, so the FIXTURE changes.
    cands = [{"receipt_id": f"rc{i}", "state": "committed", "subject": c,
              "match_mechanisms": [], "matched_terms": []}
             for i, c in enumerate(committed or [])]
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
def test_f4_no_agreement_outside_the_table_SUPERSEDED():
    """SUPERSEDED by test_findings_r4.GOLDEN.

    This pin enumerated the domain but recomputed the implementation's own key derivation
    to decide what the table "should" say, so it could only ever agree with the code -- it
    passed while the bucket fallback it was written to catch was still live. The
    replacement in test_findings_r4.py compares against a table written by hand from the
    specification, and asserts the COMPLEMENT (everything not in that table is unscored).
    Kept as a named marker so the lesson is not silently deleted.
    """
    import test_findings_r4 as R4
    assert R4.GOLDEN and R4.UNRECOGNISED, "the replacement golden table is missing"


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
    for name in ("subject_identity.py", "run_shard.py"):
        t = (HERE / name).read_text()
        assert "order_attempts" in t, f"{name} does not use the shared ordering helper"
        assert "sorted(glob.glob" not in t, f"{name} still path-sorts attempts"


# ================================================= #9 : isolation via dependents
def test_f9_isolation_SUPERSEDED_by_process_isolation():
    """SUPERSEDED. Both previous pins tested a sys.modules helper that no longer exists.

    Two attempts at in-process isolation failed for the same reason: restoring the dict
    cannot retract a reference a module already captured. Isolation is now by SUBPROCESS
    (run_pins.sh gives each pin file a fresh interpreter), which is what
    test_findings_r4.test_isolation_is_by_process_not_by_sys_modules asserts.
    """
    assert not (HERE / "corpus_stub.py").exists(), "the in-process stub is back"
    assert (HERE / "run_pins.sh").exists(), "the subprocess runner is missing"


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
