"""RED-FIRST pins for every r1 finding. Each fails at 7ef9f83a and passes after the fix.

Numbering matches the r1 verdict. No network, no rig, no real corpus.
"""
import json
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).parent
sys.path.insert(0, str(HERE))

import corpus_example
_saved = sys.modules.get("corpus")
sys.modules["corpus"] = corpus_example

import expectations as E          # noqa: E402
import subject_identity as SI     # noqa: E402
import engine_failures            # noqa: E402

BY = {r["id"]: r for r in corpus_example.CORPUS}


def _row(note, rid="r", **kw):
    d = {"id": rid, "text": "synthetic", "family": "f", "variant": "v",
         "member_kind": None, "group_kind": None, "requested_kind": None,
         "anchor_kind": None, "note": note}
    d.update(kw)
    return d


def _attempt(tmp, qid, turn, attempt, committed=None, mechs=None, status="partial",
             facts=0, failure=None, raw=None):
    d = Path(tmp) / "replicate"
    d.mkdir(parents=True, exist_ok=True)
    f = d / f"{qid}-rep1-t{turn}-a{attempt}.json"
    if raw is not None:
        f.write_text(raw)
        return f
    cands = [{"state": "committed", "subject": c, "match_mechanisms": mechs or [],
              "matched_terms": []} for c in (committed or [])]
    body = {"request": {}, "status": 200, "dt": 1.0, "response": {
        "result": {"request_id": f"req_{qid}_{turn}", "result_id": f"res_{qid}",
                   "status": status,
                   "claimed_facts": [{"claim_id": f"c{i}"} for i in range(facts)],
                   "subject_resolution": {"committed": committed or [], "candidates": cands}}}}
    if failure is not None:
        body["status"] = failure.get("status", 504)
        body["response"] = {"failure": failure.get("failure")} if failure.get("failure") else {}
    f.write_text(json.dumps(body))
    return f


# ---------------------------------------------------------------- #1
def test_f1_substitution_on_an_unscored_row_is_disagree_in_the_table():
    row = _row("no declared expectation", rid="u")
    t = E.table({"u": row}, {"u": "served_with_data"}, {"u": True})
    assert t[0]["verdict"] == "disagree", f'got {t[0]["verdict"]}'


# ---------------------------------------------------------------- #2
def test_f2_unreadable_artefact_never_scores_as_agreement():
    with tempfile.TemporaryDirectory() as tmp:
        _attempt(tmp, "q", 1, 1, raw="{not json")
        e = E.expectation_for(_row("SERVABLE", rid="q"))
        rec = SI.inspect(tmp, "q", e)
        assert rec["state"] == "unreadable_artefact"
        v = E.score(e, "served_with_data", identity_state=rec["state"])
        assert v[0] != "agree", f"unreadable artefact scored {v}"


def test_f2_missing_artefact_never_scores_as_agreement():
    with tempfile.TemporaryDirectory() as tmp:
        e = E.expectation_for(_row("SERVABLE", rid="q"))
        rec = SI.inspect(tmp, "q", e)
        assert rec["state"] == "no_artefact"
        v = E.score(e, "served_with_data", identity_state=rec["state"])
        assert v[0] != "agree", f"missing artefact scored {v}"


def test_f2_untrusted_identity_caps_an_agree_but_never_improves_a_disagree():
    e = E.expectation_for(_row("SERVABLE"))
    # a row that plainly failed stays failed
    assert E.score(e, "error", identity_state="unreadable_artefact")[0] == "disagree"
    assert E.score(e, "unserved", identity_state="no_artefact")[0] == "disagree"
    # only an agree is capped
    assert E.score(e, "served_with_data", identity_state="unreadable_artefact")[0] == "agree_weak"
    assert E.score(e, "served_with_data")[0] == "agree"


# ---------------------------------------------------------------- #3
def test_f3_decline_synonyms_are_not_silently_unscored():
    for note in ("expected to decline with a named basis", "expect refusal of this shape"):
        got = E.expectation_for(_row(note))["expectation"]
        assert got != E.UNSCORED, f'{note!r} -> {got}'


def test_f3_negated_servable_is_not_expect_serve():
    got = E.expectation_for(_row("not SERVABLE; negative control"))["expectation"]
    assert got != E.SERVE, f'"not SERVABLE" -> {got}'


def test_f3_a_mention_of_unservable_does_not_suppress_a_real_servable():
    note = "SERVABLE; contrast with the unservable member kinds"
    got = E.expectation_for(_row(note))["expectation"]
    assert got == E.SERVE, f'{note!r} -> {got}'


# ---------------------------------------------------------------- #4
def test_f4_named_basis_decline_does_not_agree_on_bare_no_match():
    e = E.expectation_for(_row("NEGATIVE: expect decline with a named basis"))
    assert e["expectation_basis"] == "named_basis"
    v = E.score(e, "unserved", terminal_status="no_match")
    assert v[0] != "agree", f"bare no_match scored {v}"
    assert E.score(e, "unserved", terminal_status="refused")[0] == "agree"


# ---------------------------------------------------------------- #5
def test_f5_r2_rejects_a_merely_similar_label():
    with tempfile.TemporaryDirectory() as tmp:
        _attempt(tmp, "q", 1, 1, committed=[{"kind": "team", "canonical_id": "team:PE",
                                             "label": "Platform Engineering"}])
        e = E.expectation_for(_row("anchor=Platform/team", rid="q"))
        assert SI.inspect(tmp, "q", e)["subject_substitution"] is True


def test_f5_r2_rejects_a_right_name_on_the_wrong_kind():
    with tempfile.TemporaryDirectory() as tmp:
        _attempt(tmp, "q", 1, 1, committed=[{"kind": "repository", "canonical_id": "repo:Alpha",
                                             "label": "Alpha"}])
        e = E.expectation_for(_row("anchor=Alpha/team", rid="q"))
        assert SI.inspect(tmp, "q", e)["subject_substitution"] is True


def test_f5_r2_still_accepts_the_declared_anchor():
    with tempfile.TemporaryDirectory() as tmp:
        _attempt(tmp, "q", 1, 1, committed=[{"kind": "team", "canonical_id": "team:CHAOS",
                                             "label": "Fullchaos"}])
        e = E.expectation_for(_row("anchor=Fullchaos/team", rid="q"))
        assert SI.inspect(tmp, "q", e)["subject_substitution"] is False


# ---------------------------------------------------------------- #6
def test_f6_a_subject_committed_on_an_earlier_attempt_is_not_lost():
    with tempfile.TemporaryDirectory() as tmp:
        _attempt(tmp, "q", 1, 1, committed=[{"kind": "team", "canonical_id": "team:W",
                                             "label": "Wrong"}], facts=3)
        _attempt(tmp, "q", 2, 1, committed=[], status="no_match")
        e = E.expectation_for(_row("NEGATIVE: nonexistent team name", rid="q"))
        rec = SI.inspect(tmp, "q", e)
        assert rec["subject_substitution"] is True, rec


# ---------------------------------------------------------------- #7
def test_f7_r3_records_a_wrong_kind_even_when_another_subject_matches():
    committed = [{"kind": "project", "canonical_id": "p:1", "label": "P"},
                 {"kind": "team", "canonical_id": "t:1", "label": "T"}]
    mismatch = SI.kind_observations({"requested_kind": "project"}, committed)
    assert mismatch, "an extra wrong-kind commit was not recorded"
    assert [m["kind"] for m in mismatch] == ["team"], mismatch
    # and the all-matching case records nothing
    assert SI.kind_observations({"requested_kind": "project"},
                                [{"kind": "project", "canonical_id": "p:1", "label": "P"}]) == []


# ---------------------------------------------------------------- #8
def test_f8_shard_plan_check_refuses_a_degenerate_corpus():
    import test_shard_plan as TSP
    assert hasattr(TSP, "verify"), "test_shard_plan exposes no reusable verify()"
    try:
        TSP.verify([])
    except AssertionError:
        pass
    else:
        raise AssertionError("an EMPTY corpus passed verification")
    try:
        TSP.verify([{"id": "only", "family": "f"}])
    except AssertionError:
        pass
    else:
        raise AssertionError("a 1-ROW corpus passed verification")
    rows = [{"id": f"r{i}", "family": "fam" if i % 2 else "other"} for i in range(6)]
    TSP.verify(rows)


# ---------------------------------------------------------------- #9
def test_f9_importing_the_shard_test_does_not_leave_a_synthetic_corpus_installed():
    import subprocess
    code = ("import sys, importlib;"
            "sys.path.insert(0, %r);"
            "importlib.import_module('test_shard_plan');"
            "print('LEAK' if 'corpus' in sys.modules else 'CLEAN')" % str(HERE))
    out = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True).stdout.strip()
    assert out.endswith("CLEAN"), f"sys.modules left contaminated: {out!r}"


# ---------------------------------------------------------------- #10
def test_f10_a_non_json_504_attempt_is_still_classified():
    with tempfile.TemporaryDirectory() as tmp:
        d = Path(tmp) / "replicate"
        d.mkdir(parents=True)
        (d / "q-rep1-t1-a1.json").write_text(json.dumps(
            {"request": {}, "status": 504, "dt": 60.0, "response": None}))
        recs = engine_failures.scan(tmp)
        kinds = {r["kind"] for r in recs}
        assert "UPSTREAM_504" in kinds, f"504 attempt dropped; got {kinds}"


# ---------------------------------------------------------------- #11
def test_f11_reclassified_attempts_are_visible_to_the_identity_scan():
    import reclassify_deadlines as RD
    assert hasattr(RD, "REPLAY_DIRNAME"), "no shared name for the replay dir"
    assert SI.EXTRA_ATTEMPT_GLOBS, "identity scan has no reclassify-aware glob"
    assert any(RD.REPLAY_DIRNAME in g for g in SI.EXTRA_ATTEMPT_GLOBS), \
        f"identity scan does not look under {RD.REPLAY_DIRNAME}"


# ---------------------------------------------------------------- #12 / #13
def test_f12_launchers_create_their_own_log_dir():
    for name in ("run_corpus_sequential.sh", "run_corpus_parallel.sh"):
        t = (HERE / name).read_text()
        assert "mkdir -p" in t and "logs" in t, f"{name} never creates logs/"


def test_f13_readiness_probes_follow_corpus_base():
    for name in ("run_corpus_sequential.sh", "run_corpus_parallel.sh"):
        t = (HERE / name).read_text()
        assert "CORPUS_BASE" in t, f"{name} ignores CORPUS_BASE for its probes"
        assert "127.0.0.1:3040/\"" not in t and "127.0.0.1:3040/'" not in t, \
            f"{name} still hard-codes the :3040 probe"


if __name__ == "__main__":
    fails = 0
    for name, fn in sorted(globals().items()):
        if name.startswith("test_") and callable(fn):
            try:
                fn()
                print(f"PASS  {name}")
            except Exception as exc:
                fails += 1
                print(f"FAIL  {name}: {type(exc).__name__}: {exc}")
    if _saved is not None:
        sys.modules["corpus"] = _saved
    else:
        sys.modules.pop("corpus", None)
    print(f"\n{fails} failing")
    raise SystemExit(1 if fails else 0)
