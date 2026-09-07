"""RED-FIRST pins for the six findings r2 reported NOT FIXED, plus the regression r2
found in test_findings_r1.py.

Each finding gets TWO kinds of case:
  (a) the reviewer's OWN reproduction input, adopted verbatim;
  (b) a second adversarial case of my own, in the same shape family.

The r1 pins were each written to the reviewer's single example, so a green pin proved
the narrow case and left its neighbours open -- six of thirteen. These are tables of
inputs per rule, not one case per rule.
"""
import json
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).parent
sys.path.insert(0, str(HERE))

import corpus_example  # noqa: E402
from corpus_stub import using_example_corpus  # noqa: E402



import expectations as E          # noqa: E402
import subject_identity as SI     # noqa: E402


def _row(note, **kw):
    d = {"id": "r", "text": "synthetic", "family": "f", "variant": "v",
         "member_kind": None, "group_kind": None, "requested_kind": None,
         "anchor_kind": None, "note": note,
         "expect": None, "basis": None, "anchor": None, "nonexistent": False}
    d.update(kw)
    return d


def _write(tmp, qid, turn, attempt, committed=None, status="partial", facts=0, raw=None):
    d = Path(tmp) / "replicate"
    d.mkdir(parents=True, exist_ok=True)
    f = d / f"{qid}-rep1-t{turn}-a{attempt}.json"
    if raw is not None:
        f.write_text(raw)
        return f
    cands = [{"state": "committed", "subject": c, "match_mechanisms": [], "matched_terms": []}
             for c in (committed or [])]
    f.write_text(json.dumps({"request": {}, "status": 200, "dt": 1.0, "response": {"result": {
        "request_id": f"req_{turn}", "result_id": "res", "status": status,
        "claimed_facts": [{"claim_id": f"c{i}"} for i in range(facts)],
        "subject_resolution": {"committed": committed or [], "candidates": cands}}}}))
    return f


# ============================================================ #2
def test_f2_reviewer_mixed_unreadable_history_does_not_escape_the_cap():
    """r2: 'F2 mixed unreadable history: read False agree'."""
    with tempfile.TemporaryDirectory() as tmp:
        _write(tmp, "q", 1, 1, raw="{not json")                      # earlier: malformed
        _write(tmp, "q", 2, 1, committed=[{"kind": "team", "canonical_id": "t:1",
                                           "label": "T"}], facts=2)  # terminal: readable
        e = E.expectation_for(_row("SERVABLE", id="q", expect="serve"))
        rec = SI.inspect(tmp, "q", e)
        assert rec["state"] != "read", f'mixed history reported state={rec["state"]!r}'
        v = E.score(e, "served_with_data", identity_state=rec["state"],
                    terminal_status="partial")
        assert v[0] != "agree", f"mixed-readability history scored {v[0]}"


def test_f2_mine_unreadable_in_the_middle_of_a_long_chain():
    with tempfile.TemporaryDirectory() as tmp:
        _write(tmp, "q", 1, 1, committed=[{"kind": "team", "canonical_id": "t:1", "label": "T"}])
        _write(tmp, "q", 2, 1, raw='{"truncated"')
        _write(tmp, "q", 3, 1, committed=[{"kind": "team", "canonical_id": "t:1", "label": "T"}],
               facts=1)
        e = E.expectation_for(_row("SERVABLE", id="q", expect="serve"))
        rec = SI.inspect(tmp, "q", e)
        assert rec["state"] != "read", rec["state"]
        assert E.score(e, "served_with_data", identity_state=rec["state"],
                       terminal_status="partial")[0] != "agree"


def test_f2_mine_a_failure_response_is_readable_not_unreadable():
    """A 422/504 attempt is a normal artefact. Only an UNPARSEABLE one taints the row."""
    with tempfile.TemporaryDirectory() as tmp:
        d = Path(tmp) / "replicate"; d.mkdir(parents=True)
        (d / "q-rep1-t1-a1.json").write_text(json.dumps(
            {"request": {}, "status": 422, "dt": 1.0,
             "response": {"failure": {"httpStatus": 422, "code": "acr_answer_rejected"}}}))
        _write(tmp, "q", 2, 1, committed=[{"kind": "team", "canonical_id": "t:1",
                                           "label": "T"}], facts=2)
        e = E.expectation_for(_row("SERVABLE", id="q", expect="serve"))
        rec = SI.inspect(tmp, "q", e)
        assert rec["state"] == "read", f"a failure response was treated as {rec['state']}"
        assert E.score(e, "served_with_data", identity_state=rec["state"],
                       terminal_status="partial")[0] == "agree"


# ============================================================ #3
# SUPERSEDED by round 3: notes are no longer parsed. See
# test_findings_r3.test_f3_the_note_no_longer_influences_the_expectation, which pins that
# every one of these strings is now inert -- strictly stronger than asserting each parse.

# ============================================================ #4
def test_f4_reviewer_fail_open_matrix():
    """r2: 'no_match=agree_weak, missing=agree, refused=agree'. Only refused may agree."""
    e = E.expectation_for(_row("decline, named basis", expect="decline", basis="named_basis"))
    assert E.score(e, "unserved", terminal_status="refused")[0] == "agree"
    # an ABSENT terminal status is a MISSING MEASUREMENT, not a measured pass
    v_missing = E.score(e, "unserved", terminal_status=None)
    assert v_missing[0] == "unscored", f"absent terminal status scored {v_missing[0]}"
    assert "terminal status" in v_missing[1].lower()
    assert E.score(e, "unserved", terminal_status="no_match")[0] == "disagree"


def test_f4_mine_a_decline_without_a_named_basis_is_unaffected():
    e = E.expectation_for(_row("decline", expect="decline"))
    assert e["expectation_basis"] is None
    for t in ("no_match", "refused"):
        assert E.score(e, "unserved", terminal_status=t)[0] == "agree", t
    assert E.score(e, "unserved", terminal_status=None)[0] == "unscored"


# ============================================================ #5
def test_f5_reviewer_missing_kind_does_not_satisfy_a_declared_anchor_kind():
    """r2: 'F5 missing-kind anchor: False' -- kind=None slipped through."""
    with tempfile.TemporaryDirectory() as tmp:
        _write(tmp, "q", 1, 1, committed=[{"kind": None, "canonical_id": None,
                                           "label": "Platform"}])
        e = E.expectation_for(_row("anchor", id="q", anchor={"kind": "team", "label": "Platform"}))
        assert SI.inspect(tmp, "q", e)["subject_substitution"] is True


def test_f5_mine_empty_and_absent_kind_keys():
    for committed in ([{"kind": "", "canonical_id": "x", "label": "Platform"}],
                      [{"canonical_id": "x", "label": "Platform"}]):
        with tempfile.TemporaryDirectory() as tmp:
            _write(tmp, "q", 1, 1, committed=committed)
            e = E.expectation_for(_row("anchor", id="q", anchor={"kind": "team", "label": "Platform"}))
            assert SI.inspect(tmp, "q", e)["subject_substitution"] is True, committed
    # the correct kind still passes
    with tempfile.TemporaryDirectory() as tmp:
        _write(tmp, "q", 1, 1, committed=[{"kind": "team", "canonical_id": "team:P",
                                           "label": "Platform"}])
        e = E.expectation_for(_row("anchor", id="q", anchor={"kind": "team", "label": "Platform"}))
        assert SI.inspect(tmp, "q", e)["subject_substitution"] is False


# ============================================================ #11
def test_f11_reviewer_sibling_replay_round_trip():
    """r2: 'F11 actual sibling replay scan: no_artefact'. Round-trip, not a name check."""
    with using_example_corpus():
        import reclassify_deadlines as RD
    with tempfile.TemporaryDirectory() as tmp:
        root = Path(tmp) / "shards"
        (root / "shard-00" / "replicate").mkdir(parents=True)
        replay = root / RD.REPLAY_DIRNAME / "q" / "replicate"
        replay.mkdir(parents=True)
        _write(str(replay.parent), "q", 9, 1,
               committed=[{"kind": "team", "canonical_id": "t:W", "label": "Wrong"}], facts=1)
        e = E.expectation_for(_row("nonexistent", id="q", expect="decline", nonexistent=True))
        rec = SI.inspect(str(root), "q", e)
        assert rec["state"] == "read", f"replay attempts invisible: {rec['state']}"
        assert rec["subject_substitution"] is True, rec


def test_f11_reviewer_replay_root_is_the_shards_root_not_the_script_dir():
    """r2's actual repro: reclassify wrote <HERE>/reclassify, a SIBLING of the scan root,
    so the identity scan under shards/ reported no_artefact. One path convention: the
    replay output belongs under the shards root the scanner is given."""
    import inspect as _i
    import re as _re
    with using_example_corpus():
        import reclassify_deadlines as RD
    src = _i.getsource(RD.main)
    # Behavioural, not prose: find the ASSIGNMENT to outroot and check what it is rooted
    # at. An earlier version of this pin grepped the whole function text, which also
    # matched the comment explaining the old expression -- a pin that reads its own
    # documentation instead of the code.
    m = _re.search(r"^\s*outroot\s*=\s*(.+)$", src, _re.M)
    assert m, "no outroot assignment found in reclassify_deadlines.main"
    expr = m.group(1).strip()
    assert "shards_dir" in expr, f"replay output is not rooted at the shards dir: {expr}"
    assert "HERE" not in expr, f"replay output is still rooted at the script dir: {expr}"


def test_f11_mine_replay_and_shard_attempts_merge():
    with using_example_corpus():
        import reclassify_deadlines as RD
    with tempfile.TemporaryDirectory() as tmp:
        root = Path(tmp) / "shards"
        shard = root / "shard-00"
        (shard / "replicate").mkdir(parents=True)
        _write(str(shard), "q", 1, 1, committed=[], status="no_match")
        replay = root / RD.REPLAY_DIRNAME / "q"
        (replay / "replicate").mkdir(parents=True)
        _write(str(replay), "q", 9, 1,
               committed=[{"kind": "team", "canonical_id": "t:W", "label": "Wrong"}], facts=1)
        e = E.expectation_for(_row("nonexistent", id="q", expect="decline", nonexistent=True))
        rec = SI.inspect(str(root), "q", e)
        assert rec["subject_substitution"] is True, rec


# ============================================================ #13
def _origin(base):
    out = subprocess.run(["bash", str(HERE / "corpus_origin.sh"), base],
                         capture_output=True, text=True)
    assert out.returncode == 0, out.stderr
    return out.stdout.strip()


def test_f13_reviewer_query_and_fragment_urls():
    """r2: sed produced 'http://host:3040?tenant=foo/' and 'https://[::1]:443#frag/'."""
    assert _origin("http://host:3040?tenant=foo") == "http://host:3040"
    assert _origin("https://[::1]:443#frag") == "https://[::1]:443"


def test_f13_mine_more_url_shapes():
    cases = {
        "http://127.0.0.1:3040/api/investigations": "http://127.0.0.1:3040",
        "http://127.0.0.1:3040": "http://127.0.0.1:3040",
        "https://host/api?a=1#f": "https://host",
        "http://[::1]:8080/x": "http://[::1]:8080",
        "http://user@host:80/p": "http://user@host:80",
    }
    bad = {b: (_origin(b), w) for b, w in cases.items() if _origin(b) != w}
    assert not bad, bad


def test_f13_launchers_do_not_parse_urls_with_sed():
    for name in ("run_corpus_sequential.sh", "run_corpus_parallel.sh"):
        t = (HERE / name).read_text()
        assert "sed -E 's#(https?" not in t, f"{name} still sed-parses the URL"
        assert "corpus_origin.sh" in t, f"{name} does not use the shared origin parser"


# ============================================================ r2's regression on my pins
def test_r2_regression_importing_a_pin_file_leaves_no_synthetic_corpus():
    for mod in ("test_shard_plan", "test_findings_r1", "test_findings_r2"):
        code = (f"import sys, importlib; sys.path.insert(0, {str(HERE)!r});"
                f"importlib.import_module({mod!r});"
                "print('LEAK:' + sys.modules['corpus'].__name__ if 'corpus' in sys.modules else 'CLEAN')")
        out = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True)
        assert out.stdout.strip().endswith("CLEAN"), f"{mod}: {out.stdout.strip()!r}"


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
    print(f"\n{fails} failing")
    raise SystemExit(1 if fails else 0)
