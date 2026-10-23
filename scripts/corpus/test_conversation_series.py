"""Pins for the conversation series: per-turn scoring, redemption, cross-turn identity, and the
runner's loud failures. Live paths run against a REAL local HTTP server in a subprocess with a
synthetic corpus on PYTHONPATH (never the real corpus)."""
import json
import os
import shutil
import subprocess
import sys
import tempfile
from http.server import BaseHTTPRequestHandler, HTTPServer
import threading
from pathlib import Path

HERE = Path(__file__).parent
sys.path.insert(0, str(HERE))

import conversation as C  # noqa: E402

CONV_CORPUS = HERE / "testdata_corpus_conversations"
PLAIN_CORPUS = HERE / "testdata_corpus"
_COPY = ("harness.py", "validators.py", "contract.py", "attempt_classes.py", "attempt_order.py",
         "artefact_schema.json", "expectations.py", "corpus_example.py", "conversation.py",
         "run_conversations.py", "merge_conversations.py")


def _turn(expect, want=None, cands=None, **kw):
    t = {"n": 1, "text": "x", "expect": expect}
    if want:
        t["expected_subject"] = {"kind": "team", "canonical_id": want}
    if cands:
        t["clarify_candidates"] = cands
    t.update(kw)
    return t


def test_golden_table_matches_scorer():
    gold = json.loads((HERE / "golden_conversation_verdicts.json").read_text())
    for case in gold["cases"]:
        cands = ([{"canonical_id": "s:a", "remembered": True}, {"canonical_id": "s:b"}]
                 if case["expect"] == "clarify" else None)
        if case["name"] == "clarify, candidate missing":
            cands = [{"canonical_id": "s:a", "remembered": True}, {"canonical_id": "s:b"}]
        got = C.score_turn(_turn(case["expect"], case["want"], cands), case["obs"])
        assert got["verdict"] == case["verdict"], (case["name"], got)


def _obs(status, committed=(), offered=(), facts=1, http=200):
    return {"http": http, "status": status, "claimed_facts_n": facts,
            "committed": list(committed), "offered": list(offered)}


def _conv():
    return {"id": "c", "shape": "(c) x", "turns": [
        {"n": 1, "text": "a", "expect": "serve", "anchor_kind": "team",
         "expected_subject": {"canonical_id": "s:a"}},
        {"n": 2, "text": "b", "parent": "turn1", "expect": "clarify", "anchor_kind": "team",
         "clarify_candidates": [{"canonical_id": "s:a", "remembered": True}, {"canonical_id": "s:b"}]},
        {"n": 3, "text": "b", "parent": "turn2", "expect": "serve", "anchor_kind": "team",
         "redeem": {"canonical_id": "s:b"}, "expected_subject": {"canonical_id": "s:b"}}]}


def _rec(*obs):
    return [{"ran": True, "obs": o} for o in obs]


def test_clarify_counts_only_when_redeemed():
    ok = C.score_conversation(_conv(), _rec(
        _obs("complete", ["s:a"]), _obs("clarification_required", [], ["s:a", "s:b"], 0),
        _obs("complete", ["s:b"])))
    assert [r["verdict"] for r in ok] == ["agree"] * 3 and ok[1]["redeemed"] is True
    hollow = C.score_conversation(_conv(), _rec(
        _obs("complete", ["s:a"]), _obs("clarification_required", [], ["s:a", "s:b"], 0),
        _obs("degraded", ["s:b"], facts=0)))
    assert hollow[1]["verdict"] == "disagree" and hollow[1]["redeemed"] is False
    wrong = C.score_conversation(_conv(), _rec(
        _obs("complete", ["s:a"]), _obs("clarification_required", [], ["s:a", "s:b"], 0),
        _obs("complete", ["s:a"])))
    assert wrong[2]["why"] == "wrong_subject" and wrong[1]["verdict"] == "disagree"


def test_unrun_turns_are_not_measured():
    rec = [{"ran": True, "obs": _obs("complete", ["s:a"])},
           {"ran": False, "why": "redeem_unavailable"}, {"ran": False, "why": "x"}]
    res = C.score_conversation(_conv(), rec)
    assert [r["verdict"] for r in res][1:] == ["not_measured"] * 2


def test_identity_check():
    turns = [{"expected_subject": {"canonical_id": "s:a"}, "anchor_kind": "team"},
             {"anchor_kind": None}, {"expected_subject": {"canonical_id": "s:c"}, "anchor_kind": "team"}]
    obs = [_obs("complete", ["s:a"]), _obs("complete", ["s:b"]), _obs("complete", ["s:c"])]
    assert C.conversation_identity_check(turns, obs) == ["ok", "wrong_subject_carried",
                                                          "silent_substitution"]
    obs2 = [_obs("complete", ["s:a"]), _obs("clarification_required"), _obs("complete", ["s:c"])]
    assert C.conversation_identity_check(turns, obs2)[2] == "ok"


def test_validate_refuses_clarify_without_redemption():
    conv = _conv()
    assert C.validate_conversation(conv) == (True, None)
    conv["turns"].pop()
    ok, reason = C.validate_conversation(conv)
    assert not ok and "redemption" in reason


class _Server:
    def __init__(self, responses):
        outer = self
        self.responses, self.seen, self.n = responses, [], 0

        class H(BaseHTTPRequestHandler):
            def do_POST(self):
                raw = self.rfile.read(int(self.headers.get("Content-Length") or 0))
                outer.seen.append(json.loads(raw))
                st, body = outer.responses[min(outer.n, len(outer.responses) - 1)]
                outer.n += 1
                data = json.dumps(body).encode()
                self.send_response(st)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def do_GET(self):
                self.send_response(200)
                self.end_headers()

            def log_message(self, *_):
                pass

        self.srv = HTTPServer(("127.0.0.1", 0), H)
        self.base = f"http://127.0.0.1:{self.srv.server_port}/api/investigations"

    def __enter__(self):
        threading.Thread(target=self.srv.serve_forever, daemon=True).start()
        return self

    def __exit__(self, *_):
        self.srv.shutdown()
        self.srv.server_close()


def _res(status, rid, committed=(), cands=(), facts=1):
    return (200, {"result": {
        "status": status, "result_id": rid, "claimed_facts": [{}] * facts,
        "subject_resolution": {
            "committed": [{"kind": "team", "canonical_id": c, "label": c} for c in committed],
            "candidates": [{"subject": {"kind": "team", "canonical_id": c}, "receipt_id": f"rc-{c}"}
                           for c in cands]}}})


def _run(script, corpus, argv, base=None, extra_env=None):
    tmp = tempfile.mkdtemp()
    dest = Path(tmp) / "d"
    dest.mkdir()
    for n in _COPY:
        shutil.copy2(HERE / n, dest / n)
    env = dict(os.environ, PYTHONPATH=f"{corpus}:{dest}", CORPUS_CONV_DIR=str(Path(tmp) / "out"))
    env.pop("CORPUS_EXPECTED_BUILD", None)
    env["CORPUS_BASE"] = base or "http://127.0.0.1:1/x"
    env.update(extra_env or {})
    r = subprocess.run([sys.executable, str(dest / script), *argv], env=env, cwd=dest,
                       capture_output=True, text=True, timeout=60)
    return r, Path(tmp)


def test_runner_refuses_corpus_without_conversations():
    r, _ = _run("run_conversations.py", PLAIN_CORPUS, ["1"])
    assert r.returncode != 0 and "no CONVERSATIONS section" in (r.stdout + r.stderr)


def test_series_posts_parent_and_redemption_receipt_then_merges():
    script = [_res("complete", "res-a", ["syn:one"]),
              _res("clarification_required", "res-b", [], ["syn:one", "syn:two"], 0),
              _res("complete", "res-c", ["syn:two"])]
    with _Server(script) as srv:
        r, tmp = _run("run_conversations.py", CONV_CORPUS, ["1"], srv.base)
        assert r.returncode == 0, r.stderr
        t1, t2, t3 = srv.seen
    assert "parentResultId" not in t1
    assert t2["parentResultId"] == "res-a" and "priorSubjectReceipts" not in t2
    assert t3["parentResultId"] == "res-b"
    assert t3["priorSubjectReceipts"] == [{"result_id": "res-b", "receipt_id": "rc-syn:two"}]
    out = tmp / "out" / "verdict.json"
    m, _ = subprocess.run, None
    d = tmp / "d"
    env = dict(os.environ, PYTHONPATH=f"{CONV_CORPUS}:{d}", CORPUS_BASE="http://127.0.0.1:1/x")
    ok = subprocess.run([sys.executable, str(d / "merge_conversations.py"), "--in", str(tmp / "out"),
                         "--out", str(out), "--reps", "1"], env=env, capture_output=True, text=True)
    assert ok.returncode == 0, ok.stderr
    v = json.loads(out.read_text())
    assert v["tally"]["agree"] == 3 and v["rows"][0]["redeemed"] == [True]
    short = subprocess.run([sys.executable, str(d / "merge_conversations.py"), "--in", str(tmp / "out"),
                            "--out", str(out), "--reps", "3"], env=env, capture_output=True, text=True)
    assert short.returncode == 2 and "missing summary for rep2" in short.stderr


def test_unredeemable_offer_is_not_measured_and_merge_fails():
    script = [_res("complete", "res-a", ["syn:one"]),
              _res("clarification_required", "res-b", [], ["syn:one"], 0)]
    with _Server(script) as srv:
        r, tmp = _run("run_conversations.py", CONV_CORPUS, ["1"], srv.base)
        assert r.returncode == 0 and len(srv.seen) == 2
    d = tmp / "d"
    env = dict(os.environ, PYTHONPATH=f"{CONV_CORPUS}:{d}", CORPUS_BASE="http://127.0.0.1:1/x")
    m = subprocess.run([sys.executable, str(d / "merge_conversations.py"), "--in", str(tmp / "out"),
                        "--out", str(tmp / "v.json"), "--reps", "1"], env=env,
                       capture_output=True, text=True)
    assert m.returncode == 2 and "not measured" in m.stderr


def test_window_prompt_is_answered_inside_the_turn():
    win = _res("clarification_required", "res-w", [], [], 0)
    win[1]["result"]["window_clarification"] = {
        "options": [{"relative_id": "trailing_90d", "receipt_id": "wr-1"}]}
    script = [win, _res("complete", "r1x", ["syn:one"]),
              _res("clarification_required", "r2x", [], ["syn:one", "syn:two"], 0),
              _res("complete", "r3x", ["syn:two"])]
    with _Server(script) as srv:
        r, tmp = _run("run_conversations.py", CONV_CORPUS, ["1"], srv.base)
        assert r.returncode == 0, r.stderr
        seen = srv.seen
    assert len(seen) == 4
    assert seen[1]["priorWindowReceipts"] == [{"result_id": "res-w", "receipt_id": "wr-1"}]
    summ = json.loads((tmp / "out" / "rep1" / "conversation-summary.json").read_text())
    t1 = summ["records"][0]["turns"][0]
    assert t1["steps"] == 2 and t1["obs"]["status"] == "complete"
    assert seen[2]["parentResultId"] == "r1x" and "priorWindowReceipts" not in seen[2]


# ----------------------------------------------------------------------------------------
# MCP answer projection (CHAOS-10741). The MCP route serves the answer PROJECTION: committed
# subjects at `committed_subjects`, offers at `clarification.candidates[].subject`, the
# un-clamped fact total at `completeness.claimed_facts_count`; there is no `subject_resolution`
# and no `claimed_facts`. Fixtures in testdata_mcp/ are synthetic copies of stored MCP answers
# (same key structure, no org text).

MCP_DIR = HERE / "testdata_mcp"


def _mcp(name):
    return json.loads((MCP_DIR / f"mcp_{name}.json").read_text())["structured"]


def _serve_turn(want):
    return {"n": 1, "text": "x", "expect": "serve", "anchor_kind": "project",
            "expected_subject": {"kind": "project", "canonical_id": want}}


def test_mcp_projection_serve_scores_from_committed_subjects():
    doc = _mcp("serve_committed")
    want = doc["committed_subjects"][0]["canonical_id"]
    obs = C.observe(200, doc)
    assert obs["committed"] == [want], obs
    got = C.score_turn(_serve_turn(want), obs)
    assert got["verdict"] == "agree", got


def test_mcp_projection_wrong_subject_still_disagrees():
    obs = C.observe(200, _mcp("serve_committed"))
    got = C.score_turn(_serve_turn("syn:not-the-committed-one"), obs)
    assert got["verdict"] == "disagree" and got["why"] == "wrong_subject", got


def test_mcp_projection_fact_count_is_the_unclamped_total_not_key_facts():
    doc = _mcp("serve_clamped")
    assert len(doc["key_facts"]) < doc["completeness"]["claimed_facts_count"]
    obs = C.observe(200, doc)
    assert obs["claimed_facts_n"] == doc["completeness"]["claimed_facts_count"], obs
    want = doc["committed_subjects"][0]["canonical_id"]
    assert C.score_turn(_serve_turn(want), obs)["verdict"] == "agree"


def test_mcp_projection_every_committed_subject_is_read():
    doc = dict(_mcp("serve_committed"))
    doc["committed_subjects"] = doc["committed_subjects"] + [
        {"kind": "team", "canonical_id": "syn:second", "label": "synthetic"}]
    assert C.observe(200, doc)["committed"] == [doc["committed_subjects"][0]["canonical_id"],
                                                "syn:second"]


def test_mcp_projection_offers_are_read_from_clarification_candidates():
    doc = _mcp("clarify_two")
    ids = [c["subject"]["canonical_id"] for c in doc["clarification"]["candidates"]]
    obs = C.observe(200, doc)
    assert obs["offered"] == ids and obs["committed"] == [], obs
    turn = {"n": 2, "text": "x", "expect": "clarify", "anchor_kind": "project",
            "clarify_candidates": [{"canonical_id": i} for i in ids]}
    assert C.score_turn(turn, obs)["verdict"] == "agree"
    turn["clarify_candidates"].append({"canonical_id": "syn:never-offered"})
    assert C.score_turn(turn, obs)["why"] == "expected_candidates_not_offered(1)"


def test_full_result_shape_is_read_as_before():
    full = {"status": "complete", "claimed_facts": [{"claim_id": "a"}, {"claim_id": "b"}],
            "subject_resolution": {"committed": [{"canonical_id": "s:a"}],
                                   "candidates": [{"subject": {"canonical_id": "s:b"}}]}}
    obs = C.observe(200, full)
    assert (obs["committed"], obs["offered"], obs["claimed_facts_n"]) == (["s:a"], ["s:b"], 2)
    empty = {"status": "no_match", "claimed_facts": [], "subject_resolution": {}}
    obs = C.observe(200, empty)
    assert (obs["committed"], obs["offered"], obs["claimed_facts_n"]) == ([], [], 0)
    # a result that carries neither shape's fields yields nothing and does not raise
    assert C.observe(200, {"status": "no_match"})["committed"] == []
    assert C.observe(502, None)["committed"] == []


def test_projection_fields_read_exist_in_the_contract_schema():
    """Drift guard: if the projection contract renames a field the scorer reads, fail here."""
    schema = json.loads((HERE.parents[1] / "contracts" / "jsonschema" / "v1"
                         / "context_fabric_answer_projection.v1.schema.json").read_text())
    defs, props = schema["$defs"], schema["properties"]
    assert "committed_subjects" in props
    cand = defs[defs["ProjectedClarification"]["properties"]["candidates"]["items"]["$ref"]
                .rsplit("/", 1)[1]]
    assert "subject" in cand["properties"]
    assert "canonical_id" in defs["SubjectRef"]["properties"]
    comp_ref = props["completeness"]["$ref"].rsplit("/", 1)[1]
    assert "claimed_facts_count" in defs[comp_ref]["properties"]
    assert "clarification" in props


def test_identity_check_reads_an_mcp_attempt():
    """Row axis: a committed subject on a nonexistent-entity row must be flagged on the MCP shape."""
    import subject_identity as SI
    doc = _mcp("serve_committed")
    with tempfile.TemporaryDirectory() as tmp:
        rep = Path(tmp) / "replicate"
        rep.mkdir()
        (rep / "q-rep1-t1-a1.json").write_text(json.dumps(
            {"request": {"question": "x"}, "status": 200, "response": {"result": doc},
             "dt": 1.0, "body_undecodable": False, "mcp": True}))
        rec = SI.inspect(tmp, "q", {"declares_nonexistent": True})
        assert rec["state"] == "read" and rec["committed_n"] == 1, rec
        assert rec["subject_substitution"] is True, rec
        assert rec["claimed_facts_n"] == doc["completeness"]["claimed_facts_count"], rec
        ok = SI.inspect(tmp, "q", {})
        assert ok["subject_substitution"] is False and ok["committed_n"] == 1


# ---- absent-id marks (CHAOS-10741): a turn that expects an id absent from the live graph is
# reported `unscored`, counted apart, never silently dropped.

def _conv_absent():
    return {"id": "ca", "shape": "(c) x", "turns": [
        {"n": 1, "text": "a", "expect": "serve", "anchor_kind": "team",
         "expected_subject": {"canonical_id": "s:gone"}},
        {"n": 2, "text": "b", "expect": "serve", "anchor_kind": "team",
         "expected_subject": {"canonical_id": "s:here"}}]}


def test_absent_id_marks_the_turn_unscored_and_counts_it():
    rec = _rec(_obs("no_match"), _obs("complete", ["s:here"]))
    plain = C.score_conversation(_conv_absent(), rec)
    assert plain[0]["verdict"] == "disagree"
    marked = C.score_conversation(_conv_absent(), rec, absent_ids=frozenset({"s:gone"}))
    assert marked[0]["verdict"] == "unscored" and marked[0]["why"] == C.ABSENT_WHY
    assert marked[1] == plain[1]
    assert C.score_conversation(_conv_absent(), rec, absent_ids=frozenset()) == plain


def test_absent_id_match_is_exact_not_substring():
    rec = _rec(_obs("no_match"), _obs("complete", ["s:here"]))
    got = C.score_conversation(_conv_absent(), rec, absent_ids=frozenset({"s:gon", "s:gone2"}))
    assert got[0]["verdict"] == "disagree"


def test_absent_candidate_or_redeem_marks_the_clarify_pair():
    conv = _conv()
    conv["turns"][1]["clarify_candidates"][1] = {"canonical_id": "s:b", "remembered": False}
    rec = _rec(_obs("complete", ["s:a"]), _obs("clarification_required", [], ["s:a"], 0),
               _obs("no_match"))
    got = C.score_conversation(conv, rec, absent_ids=frozenset({"s:b"}))
    assert [r["verdict"] for r in got] == ["agree", "unscored", "unscored"], got
    assert got[1]["why"] == got[2]["why"] == C.ABSENT_WHY


def test_merge_reports_marked_turns_as_a_separate_count():
    import merge_conversations as M
    with tempfile.TemporaryDirectory() as tmp:
        d = Path(tmp) / "rep1"
        d.mkdir()
        recs = [{"n": 1, "ran": True, "obs": _obs("no_match")},
                {"n": 2, "ran": True, "obs": _obs("complete", ["s:here"])}]
        (d / "conversation-summary.json").write_text(json.dumps(
            {"rep": 1, "planned_ids": ["ca"], "records": [{"id": "ca", "rep": 1, "turns": recs}]}))
        base = M.merge(tmp, [_conv_absent()], 1)
        assert base["marked_absent_turns"] == 0 and base["tally"]["disagree"] == 1
        out = M.merge(tmp, [_conv_absent()], 1, absent_ids=frozenset({"s:gone", "s:unused"}))
        assert out["marked_absent_turns"] == 1 and out["tally"]["unscored"] == 1
        assert out["tally"]["disagree"] == 0 and out["denominator_scored_turns"] == 1
        assert out["absent_ids_unused"] == ["s:unused"]
        assert "marked_absent=1" in M.render(out)


def test_absent_ids_file_is_read_strictly():
    with tempfile.TemporaryDirectory() as tmp:
        f = Path(tmp) / "absent.txt"
        f.write_text("# why\n\ns:a\n  s:b  \n")
        assert C.load_absent_ids(f) == frozenset({"s:a", "s:b"})
        f.write_text("s:a s:b\n")
        try:
            C.load_absent_ids(f)
        except ValueError:
            pass
        else:
            raise AssertionError("a line with two ids must be refused")
        try:
            C.load_absent_ids(Path(tmp) / "missing.txt")
        except (OSError, ValueError):
            pass
        else:
            raise AssertionError("a missing file must be refused")


def test_merge_cli_absent_ids_marks_the_pair_and_exits_clean():
    script = [_res("complete", "res-a", ["syn:one"]),
              _res("clarification_required", "res-b", [], ["syn:one"], 0)]
    with _Server(script) as srv:
        r, tmp = _run("run_conversations.py", CONV_CORPUS, ["1"], srv.base)
        assert r.returncode == 0
    d = tmp / "d"
    env = dict(os.environ, PYTHONPATH=f"{CONV_CORPUS}:{d}", CORPUS_BASE="http://127.0.0.1:1/x")
    base = [sys.executable, str(d / "merge_conversations.py"), "--in", str(tmp / "out"),
            "--out", str(tmp / "v.json"), "--reps", "1"]
    (tmp / "absent.txt").write_text("# absent from the live graph\nsyn:two\n")
    m = subprocess.run(base + ["--absent-ids", str(tmp / "absent.txt")], env=env,
                       capture_output=True, text=True)
    v = json.loads((tmp / "v.json").read_text())
    assert v["marked_absent_turns"] == 2 and v["absent_ids_unused"] == []
    assert "marked_absent=2" in m.stdout, m.stdout
    bad = subprocess.run(base + ["--absent-ids", str(tmp / "nope.txt")], env=env,
                         capture_output=True, text=True)
    assert bad.returncode != 0 and "--absent-ids" in bad.stderr


def test_full_result_without_claimed_facts_is_still_the_full_shape():
    full = {"status": "no_match", "subject_resolution": {"committed": [{"canonical_id": "s:a"}]}}
    assert C.observe(200, full)["committed"] == ["s:a"]


def test_absent_redeem_turn_marks_a_clarify_turn_that_lists_no_candidates():
    conv = _conv()
    del conv["turns"][1]["clarify_candidates"]
    rec = _rec(_obs("complete", ["s:a"]), _obs("clarification_required", [], ["s:a"], 0),
               _obs("no_match"))
    got = C.score_conversation(conv, rec, absent_ids=frozenset({"s:b"}))
    assert [r["verdict"] for r in got] == ["agree", "unscored", "unscored"], got
    assert got[1]["redeemed"] is None


def test_mcp_projection_no_match_commits_and_offers_nothing():
    obs = C.observe(200, _mcp("no_match"))
    assert (obs["status"], obs["committed"], obs["offered"], obs["claimed_facts_n"]) == (
        "no_match", [], [], 0)


if __name__ == "__main__":
    fails = 0
    for name, fn in sorted(globals().items()):
        if name.startswith("test_") and callable(fn):
            try:
                fn()
                print(f"PASS  {name}")
            except Exception as exc:  # noqa: BLE001
                fails += 1
                print(f"FAIL  {name}: {type(exc).__name__}: {exc}")
    print(f"\n{fails} failing")
    raise SystemExit(1 if fails else 0)
