"""RED-FIRST pins for the 08:36Z instrument correction (CHAOS-5387).

`vector_committed_rows` read `match_mechanisms` -- the mechanisms that MATCHED a committed
candidate -- and reported it under a name that claims the commit RESTED on the vector hit.
Those are different claims, and in the four measured arms they differ: participation is
0/0/6/6 across arm2 / 3A-read / 3B / 3B-par, and the basis count is 0, because every one of
the 34 committed candidates also carries an `exact` mechanism.

The engine emits no `decision_summary` and no `commit_bases` field -- that was measured
across all 425 artefacts of the four arms -- so the basis is derived from the candidates
themselves, per candidate, and these pins hold that derivation to the distinction.
"""
import json
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).parent
sys.path.insert(0, str(HERE))

import subject_identity as SI      # noqa: E402
import expectations as E           # noqa: E402
import validators as V             # noqa: E402


def _artefact(mechs, committed_kind="team"):
    """One attempt whose single committed candidate matched by `mechs`."""
    return {"status": 200, "response": {"request_id": "r", "result": {
        "request_id": "r", "result_id": "res", "status": "complete",
        "subject_resolution": {
            "committed": [{"kind": committed_kind, "canonical_id": "caller_canonical_id",
                           "label": "Platform"}],
            "candidates": [{
                "receipt_id": "rc1", "state": "committed",
                "subject": {"kind": committed_kind, "canonical_id": "caller_canonical_id",
                            "label": "Platform"},
                "matched_terms": ["platform"],
                "match_reasons": ["Exact canonical subject label match."],
                "match_mechanisms": mechs,
                "evidence_ref_ids": [], "confidence": 0.9}]}}}}


def _inspect(mechs):
    with tempfile.TemporaryDirectory() as tmp:
        d = Path(tmp) / "replicate"
        d.mkdir(parents=True)
        (d / "q-rep1-t1-a1.json").write_text(json.dumps(_artefact(mechs)))
        return SI.inspect(tmp, "q", E.expectation_for(
            {"id": "q", "expect": "serve", "basis": None, "anchor": None,
             "nonexistent": False}))


def _columns(rec):
    """The two merge columns, from THE MERGE'S OWN function.

    Recomputing the derivation here would be the mirror trap again -- the pin would pass
    because it reimplemented the thing it is meant to hold, and would go green against the
    very code it is supposed to indict. It calls the shipped function or it proves nothing.
    """
    from merge_corpus import vector_columns
    cols = vector_columns({"q": rec})
    return cols["vector_matched_rows"], cols["vector_committed_rows"]


def test_vector_MATCHED_but_committed_on_the_caller_id_counts_1_matched_0_committed():
    """The fixture the correction was written for. The candidate is matched by vector AND
    exact and is committed on `caller_canonical_id`; vector was not load-bearing. The old
    column called this a vector commit."""
    rec = _inspect(["exact", "lexical", "vector"])
    assert rec["state"] == "read", rec
    assert rec["committed"][0]["canonical_id"] == "caller_canonical_id", rec["committed"]
    matched, committed = _columns(rec)
    assert matched == ["q"], f"vector participation was lost: {matched}"
    assert committed == [], f"an exact-backed commit was counted as a vector commit: {committed}"


def test_a_commit_with_NO_exact_match_does_count_as_a_vector_commit():
    """The other direction, or the column is just always empty and proves nothing."""
    matched, committed = _columns(_inspect(["vector"]))
    assert matched == ["q"], matched
    assert committed == ["q"], f"a vector-only commit was not counted: {committed}"
    matched, committed = _columns(_inspect(["lexical", "vector"]))
    assert committed == ["q"], f"a commit with no exact match was not counted: {committed}"


def test_a_commit_with_no_vector_at_all_counts_in_neither_column():
    matched, committed = _columns(_inspect(["exact", "lexical"]))
    assert matched == [] and committed == [], (matched, committed)


def test_commit_bases_are_PER_CANDIDATE_not_a_flattened_union():
    """Two committed candidates, one exact-only and one vector-only. The union carries both
    mechanisms and cannot tell this apart from a single candidate matched by both -- which
    is exactly why the basis needs the candidates kept separate."""
    a = _artefact(["exact"])
    res = a["response"]["result"]
    second = json.loads(json.dumps(res["subject_resolution"]["candidates"][0]))
    second["receipt_id"] = "rc2"
    second["match_mechanisms"] = ["vector"]
    second["subject"] = {"kind": "team", "canonical_id": "other_id", "label": "Other"}
    res["subject_resolution"]["candidates"].append(second)
    res["subject_resolution"]["committed"].append(
        {"kind": "team", "canonical_id": "other_id", "label": "Other"})
    with tempfile.TemporaryDirectory() as tmp:
        d = Path(tmp) / "replicate"
        d.mkdir(parents=True)
        (d / "q-rep1-t1-a1.json").write_text(json.dumps(a))
        rec = SI.inspect(tmp, "q", E.expectation_for(
            {"id": "q", "expect": "serve", "basis": None, "anchor": None,
             "nonexistent": False}))
    assert sorted(rec["match_mechanisms"]) == ["exact", "vector"], rec["match_mechanisms"]
    assert rec["commit_bases"] == [["exact"], ["vector"]], rec["commit_bases"]
    _, committed = _columns(rec)
    assert committed == ["q"], "a vector-only commit hid inside the union"


def test_the_candidate_fields_the_derivation_reads_are_TYPED_in_the_schema():
    """The derivation dereferences candidate fields, so validation must reach them. Typing
    `candidates` as an array of objects stopped one level above -- the r9 #1 class."""
    # CHAOS-5430: the schema is now GENERATED, so nodes carry their full measured path
    # instead of a hand-chosen short name. The property is unchanged -- the fields the
    # vector derivation dereferences must be typed -- only the address is.
    schema = V.schema()
    SR = "attempt.response.result.subject_resolution"
    CAND = f"{SR}.candidates[]"
    assert CAND in schema, "candidate elements are not a described node"
    assert schema[SR]["candidates"]["element_node"] == CAND
    assert schema[CAND]["match_mechanisms"]["items"] == "string"
    assert schema[CAND]["state"]["type"] == "string"
    # measured across every candidate: ints and floats both occur, so `number`, not `int`
    assert schema[CAND]["confidence"]["type"] == "number"

    bad = {"response": {"result": {"subject_resolution": {"candidates": [
        {"state": "committed", "match_mechanisms": [1]}]}}}}
    ok, reason = V.validate_attempt(bad)
    assert not ok, "a non-string mechanism inside a candidate validated true"
    assert "match_mechanisms[0]" in reason, reason
    bad2 = {"response": {"result": {"subject_resolution": {"candidates": [
        {"state": 7}]}}}}
    ok, reason = V.validate_attempt(bad2)
    assert not ok and "state" in reason, reason
    ok, reason = V.validate_attempt(
        {"response": {"result": {"subject_resolution": {"candidates": [
            {"state": "committed", "match_mechanisms": ["vector"], "confidence": 1}]}}}})
    assert ok, f"a MEASURED candidate shape was rejected: {reason}"


def test_the_old_column_name_is_gone_from_the_merge():
    """The rename must be a rename. Two columns where one claims the other's meaning is how
    this became wrong in the first place."""
    src = (HERE / "merge_corpus.py").read_text()
    assert '"vector_matched_rows"' in src, "the participation column was not renamed"
    assert src.count('"vector_committed_rows"') == 1, "the basis column is emitted twice"
    assert "_vector_columns" in src, "the two columns ship without their definitions"


def test_element_typing_never_UPGRADES_a_failed_row():
    """Element-typing `committed` rejects an artefact carrying an explicit null kind, and
    the row then scores agree_weak through the unreadable-artefact cap instead of the
    disagree it earns as a subject substitution. Doubt must never make a failed row look
    BETTER -- this instrument already fixed that exact class once, and typing one more
    array brought it back. So `committed` keeps its `items` check (which is what catches
    r9 #1's `committed: [1]`) and stops there.
    """
    import tempfile
    # CHAOS-5430 CHANGED THE MECHANISM AND KEPT THE PROPERTY. Committed elements ARE typed
    # now -- with nullable string fields, which is what lets an explicit null reach the
    # substitution rule instead of being rejected into the unreadable cap. The property
    # this pin defends is the one below and it is unchanged: doubt must never turn a row
    # that earns `disagree` into `agree_weak`.
    schema = V.schema()
    SR = "attempt.response.result.subject_resolution"
    assert schema[SR]["committed"].get("element_node"), \
        "committed elements are untyped again"
    assert schema[f"{SR}.committed[]"]["kind"].get("nullable"), \
        "committed kind is no longer nullable; a null-kind row will be capped, not scored"
    ok, reason = V.validate_attempt(
        {"response": {"result": {"subject_resolution": {"committed": [1]}}}})
    assert not ok and "committed[0]" in reason, reason

    def verdict(committed):
        with tempfile.TemporaryDirectory() as tmp:
            d = Path(tmp) / "replicate"
            d.mkdir(parents=True)
            cands = [{"state": "committed", "subject": c, "match_mechanisms": [],
                      "matched_terms": []} for c in committed]
            (d / "q-rep1-t1-a1.json").write_text(json.dumps({
                "request": {}, "status": 200, "dt": 1.0, "response": {"result": {
                    "request_id": "r", "result_id": "res", "status": "complete",
                    "claimed_facts": [{"claim_id": "c0"}],
                    "subject_resolution": {"committed": committed,
                                           "candidates": cands}}}}))
            e = E.expectation_for({"id": "q", "expect": "serve", "basis": None,
                                   "anchor": {"kind": "team", "label": "Platform"},
                                   "nonexistent": False})
            rec = SI.inspect(tmp, "q", e)
            return E.score(e, "served_with_data",
                           subject_substitution=rec.get("subject_substitution"),
                           identity_state=rec.get("state"),
                           terminal_status="complete")[0]

    for c in ([{"kind": None, "canonical_id": None, "label": "Platform"}],
              [{"kind": "", "canonical_id": "x", "label": "Platform"}],
              [{"canonical_id": "x", "label": "Platform"}]):
        assert verdict(c) == "disagree", f"a wrong-kind commit scored {verdict(c)}: {c}"
    assert verdict([{"kind": "team", "canonical_id": "team:P",
                     "label": "Platform"}]) == "agree"


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
