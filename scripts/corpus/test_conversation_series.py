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
