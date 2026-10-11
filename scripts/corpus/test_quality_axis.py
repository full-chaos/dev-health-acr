"""Pins for quality_axis.py. Every guard has a planted-defect pair: the clean fixture
scores the dimension at its top value, the planted defect makes it fall."""
import copy
import json
import sys
from pathlib import Path

HERE = Path(__file__).parent
sys.path.insert(0, str(HERE))
import expectations as EX  # noqa: E402
import quality_axis as Q  # noqa: E402
import subject_identity as SI  # noqa: E402

GOLDEN = HERE / "quality_golden.json"


def served_doc(**over):
    d = {
        "schema_version": "x", "result_id": "r1", "request_id": "q1", "status": "complete",
        "claimed_facts": [{"claim_id": "c1"}, {"claim_id": "c2"}],
        "drivers": [{"driver_id": "d1", "claimed_fact_ids": ["c1"],
                     "evidence_ref_ids": ["e1"]}],
        "evidence_ref_ids": ["e1", "e2"],
        "limitations": [], "warnings": [],
        "coverage": {"sources": [], "partial": False, "details": []},
        "subject_resolution": {"committed": [{"kind": "project", "canonical_id": "project:a:x",
                                              "label": "Alpha"}], "candidates": []},
        "effective_evidence_window": {"provenance": "stated", "relative_id": "trailing_90d"},
        "completeness": {"terminal_status": "complete", "state": "complete",
                         "claimed_facts_count": 2, "rows_count": 0,
                         "outcomes": [{"stage": "planning", "outcome": "satisfied",
                                       "impact": "none", "cause_observed": False,
                                       "served": 1, "declared": 1}]},
    }
    d.update(over)
    return d


def refusal_doc(**over):
    d = {"status": "no_match", "claimed_facts": [], "drivers": [], "evidence_ref_ids": [],
         "limitations": ["x"], "warnings": [], "coverage": {"sources": [], "partial": False},
         "subject_resolution": {"committed": [], "candidates": []},
         "completeness": {"terminal_status": "no_match", "state": "not_derived",
                          "claimed_facts_count": 0, "rows_count": 0,
                          "refusal_basis": "declared_kind_unmatched"}}
    d.update(over)
    return d


EXP_SERVE = {"expectation": "serve", "expectation_basis": None, "declares_nonexistent": False,
             "declared_anchor_name": "Alpha", "declared_anchor_kind": "project"}
EXP_REFUSE = {"expectation": "refuse", "expectation_basis": "declared_kind_unmatched",
              "declares_nonexistent": False, "declared_anchor_name": None,
              "declared_anchor_kind": None}
EXP_NONE = {"expectation": "unscored", "expectation_basis": None,
            "declares_nonexistent": False, "declared_anchor_name": None,
            "declared_anchor_kind": None}
EXP_NONEXISTENT = dict(EXP_NONE, expectation="decline", declares_nonexistent=True)


def dim(doc, exp, k):
    return Q.score_row(doc, exp)["dims"][k]["score"]


def test_clean_serve_scores_top_on_every_dimension():
    r = Q.score_row(served_doc(), EXP_SERVE)
    assert r["q"] == 1.0 and r["gates_failed"] == []
    assert {k: d["score"] for k, d in r["dims"].items()} == {
        "D1": 2, "D2": 2, "D3": 2, "D4": 2, "D5": 2, "D6": 2, "D7": None, "D8": 2}


def test_d1_hollow_serve():
    d = served_doc()
    d["completeness"] = dict(d["completeness"], claimed_facts_count=0)
    assert dim(served_doc(), EXP_SERVE, "D1") == 2 and dim(d, EXP_SERVE, "D1") == 0


def test_d2_hard_gap_vs_soft_narrowing():
    hard = served_doc(coverage={"sources": [], "partial": True, "details": [
        {"degrading": True, "code": "fact_provider_reported:no_data",
         "source_state": "no_data"}]})
    soft = served_doc(coverage={"sources": [], "partial": True, "details": [
        {"degrading": True, "code": "x", "source_state": "truncated"}]})
    assert dim(hard, EXP_SERVE, "D2") == 0 and dim(soft, EXP_SERVE, "D2") == 1


def test_d3_silent_partial():
    ok = served_doc(status="partial", limitations=["narrowed"])
    ok["completeness"] = dict(ok["completeness"], terminal_status="partial", state="partial")
    bad = copy.deepcopy(ok)
    bad["limitations"] = []
    assert dim(ok, EXP_SERVE, "D3") == 2 and dim(bad, EXP_SERVE, "D3") == 0


def test_d4_dangling_driver_reference_is_a_gate():
    bad = served_doc()
    bad["drivers"][0]["claimed_fact_ids"] = ["c-missing"]
    r = Q.score_row(bad, EXP_SERVE)
    assert dim(served_doc(), EXP_SERVE, "D4") == 2
    assert r["dims"]["D4"]["score"] == 0 and r["gates_failed"] == ["D4"] and r["q"] <= 0.5
    ref = served_doc()
    ref["drivers"][0]["evidence_ref_ids"] = ["e-missing"]
    assert dim(ref, EXP_SERVE, "D4") == 0


def test_d4_omission_by_budget_is_not_dangling():
    d = {"status": "complete", "key_facts": [{"claim_id": "c1"}, {"claim_id": "c2"}],
         "principal_drivers": [{"driver_id": "d1", "claimed_fact_ids": ["c-omitted"],
                                "evidence_ref_ids": ["e-omitted"]}],
         "evidence_ref_ids": ["e1"], "projection_budget": {
             "truncated": True, "facts_omitted": 3, "evidence_refs_omitted": 2},
         "completeness": {"terminal_status": "complete", "state": "complete",
                          "claimed_facts_count": 5}}
    assert dim(d, EXP_SERVE, "D4") == 2
    d["projection_budget"] = {"truncated": False}
    assert dim(d, EXP_SERVE, "D4") == 0


def test_d5_substitution_zeroes_the_row():
    bad = served_doc()
    bad["subject_resolution"]["committed"][0]["label"] = "Other"
    bad["subject_resolution"]["committed"][0]["canonical_id"] = "project:a:other"
    r = Q.score_row(bad, EXP_SERVE)
    assert r["dims"]["D5"]["score"] == 0 and r["q"] == 0.0
    r1 = Q.score_row(served_doc(), EXP_NONEXISTENT)
    assert r1["q"] == 0.0 and r1["gates_failed"] == ["R2"]
    assert Q.d5_subject(Q.view(served_doc()), EXP_NONEXISTENT)["score"] == 0


def test_d5_matches_subject_identity_semantics(tmp_path):
    """Parity: the local R1/R2 must agree with subject_identity.inspect on the same file."""
    cases = [(EXP_SERVE, "Alpha", "project:a:x"), (EXP_SERVE, "Other", "project:a:o"),
             (EXP_NONEXISTENT, "Alpha", "project:a:x"),
             (dict(EXP_SERVE, declared_anchor_kind="team"), "Alpha", "project:a:x")]
    for i, (exp, label, cid) in enumerate(cases):
        doc = served_doc()
        doc["subject_resolution"]["committed"] = [{"kind": "project", "canonical_id": cid,
                                                   "label": label}]
        rep = tmp_path / f"c{i}" / "replicate"
        rep.mkdir(parents=True)
        (rep / f"row-rep1-t1-a1.json").write_text(json.dumps(
            {"request": {}, "status": 200, "response": {"result": doc}}))
        rec = SI.inspect(tmp_path / f"c{i}", "row", exp, rep=1)
        mine = Q.d5_subject(Q.view(doc), exp)["score"] == 0
        assert mine == rec["subject_substitution"], (i, rec)


def test_d6_window_stated_vs_not():
    nw = served_doc()
    del nw["effective_evidence_window"]
    assert dim(served_doc(), EXP_SERVE, "D6") == 2 and dim(nw, EXP_SERVE, "D6") == 1


def test_d7_silent_truncation():
    ok = served_doc(projection_budget={"truncated": True}, limitations=["cut"])
    bad = served_doc(projection_budget={"truncated": True}, limitations=[])
    assert dim(ok, EXP_SERVE, "D7") == 1 and dim(bad, EXP_SERVE, "D7") == 0


def test_d8_terminal_mismatch_is_a_gate():
    bad = served_doc()
    bad["completeness"]["terminal_status"] = "partial"
    r = Q.score_row(bad, EXP_SERVE)
    assert r["dims"]["D8"]["score"] == 0 and "D8" in r["gates_failed"]


def test_refusal_correct_row_earns_full_marks():
    r = Q.score_row(refusal_doc(), EXP_REFUSE)
    assert r["q"] == 1.0 and r["track"] == "refusal" and r["gates_failed"] == []


def test_refusal_without_basis_and_serving_a_refuse_row():
    nb = refusal_doc()
    nb["completeness"] = dict(nb["completeness"], refusal_basis=None)
    assert dim(nb, EXP_REFUSE, "R1") == 0
    served = Q.score_row(served_doc(), EXP_REFUSE)
    assert served["q"] == 0.0 and served["gates_failed"] == ["R2"]


def test_unserved_serve_row_and_failed_attempt_are_not_measured(tmp_path):
    r = Q.score_row(refusal_doc(), EXP_SERVE)
    assert r["measured"] is False and r["reason"] == "not_served" and r["q"] is None
    d = tmp_path / "replicate"
    d.mkdir()
    (d / "row-rep1-t1-a1.json").write_text(json.dumps(
        {"request": {}, "status": 504, "response": {"failure": {"code": "x"}}}))
    doc, reason, _ = Q.row_records(tmp_path, "row", 1)
    assert doc is None and reason.startswith("terminal_attempt_failure")
    assert Q.row_records(tmp_path, "absent", 1)[1] == "no_artefact"


def test_shapes_normalise_to_one_view():
    full = Q.view(served_doc())
    proj = Q.view({"status": "complete", "key_facts": [{"claim_id": "c1"}, {"claim_id": "c2"}],
                   "principal_drivers": served_doc()["drivers"],
                   "evidence_ref_ids": ["e1", "e2"],
                   "committed_subjects": served_doc()["subject_resolution"]["committed"],
                   "coverage_details": [], "coverage_summary": [], "coverage_partial": False,
                   "limitations": [], "warnings": [],
                   "effective_evidence_window": {"provenance": "stated"},
                   "completeness": served_doc()["completeness"],
                   "projection_budget": {}})
    for k in ("status", "claims", "drivers", "committed", "partial", "window"):
        assert full[k] == proj[k] or k == "window"
    wrapped = Q._doc_of({"structured": {"status": "complete", "completeness": {}}})
    assert wrapped["status"] == "complete"


def test_compare_gate_regression_beats_net_improvement():
    def row(i, d1, gate=False):
        return {"id": i, "class": "serve", "measured": True, "track": "serve", "q": 0.9,
                "dims": {"D1": {"score": d1}, "D4": {"score": 0 if gate else 2}},
                "gates_failed": ["D4"] if gate else []}
    a_rows = [row("a", 0), row("b", 0), row("c", 2)]
    b_rows = [row("a", 2), row("b", 2), row("c", 2, gate=True)]
    a = {"rows": a_rows, "rollup": Q.rollup(a_rows)}
    b = {"rows": b_rows, "rollup": Q.rollup(b_rows)}
    out = Q.compare(a, b)
    assert out["verdict"] == "worse" and out["new_gate_failures"] == ["c"]
    clean = {"rows": b_rows[:2] + [row("c", 2)], "rollup": Q.rollup(b_rows[:2] + [row("c", 2)])}
    assert Q.compare(a, clean)["verdict"] == "better"


def test_compare_one_step_single_dim_is_noise():
    def row(v):
        return {"id": "a", "class": "serve", "measured": True, "track": "serve", "q": 0.5,
                "dims": {"D1": {"score": v}}, "gates_failed": []}
    a = {"rows": [row(0)], "rollup": Q.rollup([row(0)])}
    b = {"rows": [row(1)], "rollup": Q.rollup([row(1)])}
    assert Q.compare(a, b)["verdict"] == "same"
    assert Q.compare(a, b, reps_agree=True)["verdict"] == "better"


EXP_CONDITIONAL = dict(EXP_REFUSE, expectation="decline", expectation_basis=None,
                       decline_if_shape_emitted=True)


def cohort_doc(group_kind=None):
    cohort = {"kind": "team", "members": [], "total": 3}
    if group_kind:
        cohort["groups"] = [{"subject": {"kind": group_kind, "canonical_id": "team:a"},
                             "member_canonical_ids": []}]
    return served_doc(cohort=cohort)


def test_conditional_decline_scores_a_legal_reading_by_content():
    # the rev 73 shape: a flat team cohort served on the intentionally illegal row
    r = Q.score_row(cohort_doc(), EXP_CONDITIONAL)
    assert r["measured"] and r["track"] == "serve" and r["class"] == "decline_conditional"
    assert r["conditional_decline"] == "legal_reading_served"
    assert "R2" not in r["dims"] and r["q"] == 1.0 and r["gates_failed"] == []


def test_conditional_decline_kill_pair_shape_shown_or_flag_absent_stays_fabrication():
    # planted defect 1: the served result shows a team cohort grouped by team
    shown = Q.score_row(cohort_doc("team"), EXP_CONDITIONAL)
    assert shown["q"] == 0.0 and shown["gates_failed"] == ["R2"] and shown["track"] == "refusal"
    # planted defect 2: a served result that discloses a refusal basis
    disclosed = served_doc()
    disclosed["completeness"]["refusal_basis"] = "frame_invariant_violated"
    assert Q.score_row(disclosed, EXP_CONDITIONAL)["gates_failed"] == ["R2"]
    # planted defect 3: the same legal reading on a row that does NOT declare the condition
    strict = Q.score_row(cohort_doc(), EXP_REFUSE)
    assert strict["q"] == 0.0 and strict["gates_failed"] == ["R2"]


def test_conditional_decline_still_gates_on_grounding():
    bad = cohort_doc()
    bad["drivers"] = [{"driver_id": "d1", "claimed_fact_ids": ["nope"], "evidence_ref_ids": ["e1"]}]
    r = Q.score_row(bad, EXP_CONDITIONAL)
    assert r["track"] == "serve" and "D4" in r["gates_failed"] and r["q"] <= 0.5


def test_conditional_decline_refusal_is_scored_as_a_refusal():
    r = Q.score_row(refusal_doc(), EXP_CONDITIONAL)
    assert r["track"] == "refusal" and r["q"] == 1.0


def test_decline_if_shape_emitted_must_be_a_bool():
    ok, reason = EX.validate_corpus_row({"id": "x", "expect": "decline", "decline_if_shape_emitted": "false"})
    assert not ok and "decline_if_shape_emitted" in reason
    assert EX.expectation_for({"id": "x", "expect": "decline",
                               "decline_if_shape_emitted": True})["decline_if_shape_emitted"] is True


def test_golden_vectors():
    cases = {
        "clean_serve": (served_doc(), EXP_SERVE),
        "refusal_correct": (refusal_doc(), EXP_REFUSE),
        "served_on_refuse_row": (served_doc(), EXP_REFUSE),
        "undeclared_refusal": (refusal_doc(), EXP_NONE),
        "conditional_decline_legal_reading": (cohort_doc(), EXP_CONDITIONAL),
        "conditional_decline_self_grouped": (cohort_doc("team"), EXP_CONDITIONAL),
    }
    got = {k: {"q": Q.score_row(d, e)["q"],
               "dims": {n: x["score"] for n, x in Q.score_row(d, e)["dims"].items()}}
           for k, (d, e) in cases.items()}
    assert got == json.loads(GOLDEN.read_text())
