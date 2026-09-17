"""Pins for CHAOS-5838: the corpus harness must mirror the parent-result carry shape
Ask Dev's chat surface sends on a follow-up (`deriveParentReference`, ask-dev
src/lib/conversation.ts, PR #83) on turn 2+ of a replicate's own clarification chain --
otherwise the frozen corpus never exercises acr's ParentResultID-keyed confirmed-need
ledger path and every multi-turn row scores miss_no_reference regardless of the engine.

Same isolation discipline as test_findings_5562.py: every subprocess test runs against
an ISOLATED COPY of the corpus scripts in a temp dir (never the real replicate/ output),
against a REAL local HTTP server (never a mock of urllib), with the synthetic
`testdata_corpus` corpus on PYTHONPATH.
"""
import json
import os
import shutil
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).parent
TESTDATA = HERE / "testdata_corpus"

_COPY_FILES = ("harness.py", "validators.py", "contract.py", "attempt_classes.py",
               "attempt_order.py", "shard_plan.py", "run_shard.py", "artefact_schema.json",
               "reclassify_deadlines.py", "merge_corpus.py", "engine_failures.py",
               "subject_identity.py", "expectations.py", "corpus_example.py")

CORPUS_ID = "example-serve-named-project"  # requested_kind=project, anchor_kind=project


def _isolated_copy(tmp):
    dest = Path(tmp) / "corpusdir"
    dest.mkdir()
    for name in _COPY_FILES:
        shutil.copy2(HERE / name, dest / name)
    return dest


def _env(base, **overrides):
    env = dict(os.environ)
    env["PYTHONPATH"] = f"{TESTDATA}:{HERE}:{env.get('PYTHONPATH', '')}"
    env.pop("CORPUS_BASE", None)
    env.pop("CORPUS_EXPECTED_BUILD", None)
    env.pop("CORPUS_HARNESS_LEGACY_NO_CARRY", None)
    env["CORPUS_BASE"] = base
    env.update(overrides)
    return env


class _SequencedServer:
    """Serves a DIFFERENT scripted (status, body) per request in order, holding the last
    one for any request past the end of the list -- same shape as test_findings_5562.py's
    server of the same name, copied here so this file has no import dependency on it."""

    def __init__(self, responses):
        from http.server import BaseHTTPRequestHandler, HTTPServer
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                length = int(self.headers.get("Content-Length") or 0)
                raw = self.rfile.read(length)
                outer.requests_seen.append(json.loads(raw.decode("utf-8")))
                idx = min(outer.requests, len(outer.responses) - 1)
                status, body_obj = outer.responses[idx]
                outer.requests += 1
                payload = json.dumps(body_obj).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

            def log_message(self, *_a):
                pass

        self.requests = 0
        self.requests_seen = []  # every decoded request body, in arrival order
        self.responses = responses
        self._srv = HTTPServer(("127.0.0.1", 0), Handler)
        self.port = self._srv.server_port

    @property
    def base(self):
        return f"http://127.0.0.1:{self.port}/api/investigations"

    def __enter__(self):
        import threading
        self._t = threading.Thread(target=self._srv.serve_forever, daemon=True)
        self._t.start()
        return self

    def __exit__(self, *_exc):
        self._srv.shutdown()
        self._srv.server_close()


def _turn1_response(committed, result_id="res-turn1-aaaaaaaa"):
    """A non-terminal clarification_required result: offers a kind clarification (so
    the chain reaches turn 2) and carries the given `subject_resolution.committed`
    list, independent of `candidates` (which `update_memory_and_build_receipts` reads
    for a DIFFERENT purpose -- redeeming an offer, never the parent-result carry)."""
    return (200, {"result": {
        "status": "clarification_required",
        "result_id": result_id,
        "structure_needs": {"kind_options": [{"kind": "project", "receipt_id": "kind-receipt-1"}],
                             "missing": ["kind"]},
        "subject_resolution": {"candidates": [], "committed": committed},
    }})


_TURN2_TERMINAL = (200, {"result": {
    "status": "complete", "result_id": "res-turn2-bbbbbbbb",
    "subject_resolution": {"candidates": [], "committed": []},
}})

_ONE_COMMITTED_SUBJECT = [{"kind": "team", "canonical_id": "team-123", "label": "fullchaos"}]


def _run_two_turn_replicate(committed_turn1, env_overrides=None):
    """Runs the CLI against a 2-turn scripted exchange (turn1=clarification_required
    with `committed_turn1`, turn2=terminal) inside an isolated copy, and returns
    (server, turn1_artefact_request, turn2_artefact_request) -- the raw `request` dict
    each artefact file recorded, read back from disk, never from the in-memory
    dict the harness built (proving what was actually SENT, not merely constructed)."""
    import tempfile
    tmp = tempfile.mkdtemp()
    dest = _isolated_copy(tmp)
    with _SequencedServer([_turn1_response(committed_turn1), _TURN2_TERMINAL]) as srv:
        r = subprocess.run([sys.executable, str(dest / "harness.py"), CORPUS_ID],
                            env=_env(srv.base, **(env_overrides or {})),
                            capture_output=True, text=True, timeout=30, cwd=str(dest))
    assert r.returncode == 0, f"harness.py exited nonzero:\n{r.stdout}\n{r.stderr}"
    t1 = json.loads((dest / "replicate" / f"{CORPUS_ID}-rep1-t1-a1.json").read_text())
    t2 = json.loads((dest / "replicate" / f"{CORPUS_ID}-rep1-t2-a1.json").read_text())
    return srv, t1["request"], t2["request"]


# ==================================================== new shape (default)

def test_turn1_request_never_carries_a_parent_reference():
    _srv, t1_req, _t2_req = _run_two_turn_replicate(_ONE_COMMITTED_SUBJECT)
    assert "parentResultId" not in t1_req, t1_req
    assert "subjectHints" not in t1_req, t1_req
    assert t1_req == {"question": "Example question, synthetic."}, t1_req


def test_turn2_request_carries_parent_result_id_and_mapped_subject_hints():
    _srv, t1_req, t2_req = _run_two_turn_replicate(_ONE_COMMITTED_SUBJECT)
    assert t2_req["parentResultId"] == "res-turn1-aaaaaaaa", t2_req
    assert t2_req["subjectHints"] == [
        {"kind": "team", "id": "team-123", "label": "fullchaos",
         "source": "ask_dev_parent_result_subject"},
    ], t2_req
    # alongside the existing receipt mechanism, never replacing it
    assert t2_req["priorKindReceipts"] == [
        {"result_id": "res-turn1-aaaaaaaa", "receipt_id": "kind-receipt-1"}], t2_req


def test_empty_committed_still_yields_a_defined_parent_result_id():
    """Same rule as ask-dev's own producer: a turn that committed nothing still names
    itself as the parent, alongside an empty hints list -- itself a fact worth sending,
    never the same as omitting parentResultId entirely (that is turn 1's own shape)."""
    _srv, _t1_req, t2_req = _run_two_turn_replicate([])
    assert t2_req["parentResultId"] == "res-turn1-aaaaaaaa", t2_req
    assert t2_req["subjectHints"] == [], t2_req


def test_subject_hints_capped_at_fifty_in_order():
    sixty = [{"kind": "team", "canonical_id": f"team-{i}", "label": f"team {i}"}
             for i in range(60)]
    _srv, _t1_req, t2_req = _run_two_turn_replicate(sixty)
    assert len(t2_req["subjectHints"]) == 50, len(t2_req["subjectHints"])
    assert t2_req["subjectHints"][0]["id"] == "team-0", t2_req["subjectHints"][0]
    assert t2_req["subjectHints"][-1]["id"] == "team-49", t2_req["subjectHints"][-1]


def test_turn2_artefact_still_satisfies_the_measured_artefact_schema():
    """Byte-compatibility with the scorer/validator: an artefact whose `request` gained
    two new top-level keys must still validate -- the measured schema checks only the
    keys it knows about (see validators.py's `_check_node`), so new keys never break
    it, but this proves that empirically against the real validator, not by reading
    the implementation."""
    sys.path.insert(0, str(HERE))
    from validators import validate_attempt
    import tempfile
    tmp = tempfile.mkdtemp()
    dest = _isolated_copy(tmp)
    with _SequencedServer([_turn1_response(_ONE_COMMITTED_SUBJECT), _TURN2_TERMINAL]) as srv:
        r = subprocess.run([sys.executable, str(dest / "harness.py"), CORPUS_ID],
                            env=_env(srv.base), capture_output=True, text=True,
                            timeout=30, cwd=str(dest))
    assert r.returncode == 0, r.stdout + r.stderr
    artefact = json.loads((dest / "replicate" / f"{CORPUS_ID}-rep1-t2-a1.json").read_text())
    assert "parentResultId" in artefact["request"]
    assert "subjectHints" in artefact["request"]
    ok, reason = validate_attempt(artefact)
    assert ok, f"turn-2 artefact with the new carry fields failed validation: {reason}"


# ==================================================== legacy shape (env var set)

def test_legacy_env_var_reverts_to_the_old_shape_on_every_turn():
    _srv, t1_req, t2_req = _run_two_turn_replicate(
        _ONE_COMMITTED_SUBJECT, env_overrides={"CORPUS_HARNESS_LEGACY_NO_CARRY": "1"})
    assert "parentResultId" not in t1_req and "subjectHints" not in t1_req, t1_req
    assert "parentResultId" not in t2_req and "subjectHints" not in t2_req, t2_req
    # the pre-existing receipt mechanism is untouched by the switch
    assert t2_req["priorKindReceipts"] == [
        {"result_id": "res-turn1-aaaaaaaa", "receipt_id": "kind-receipt-1"}], t2_req


# ==================================================== unit-level: the pure builder

def test_derive_parent_reference_maps_fields_straight_across():
    sys.path.insert(0, str(HERE))
    import importlib
    import harness
    importlib.reload(harness)
    result = {"result_id": "res-x", "subject_resolution": {"committed": [
        {"kind": "repository", "canonical_id": "repo-1", "label": "dev-health-acr"},
    ]}}
    ref = harness.derive_parent_reference(result)
    assert ref == {"parentResultId": "res-x", "subjectHints": [
        {"kind": "repository", "id": "repo-1", "label": "dev-health-acr",
         "source": "ask_dev_parent_result_subject"},
    ]}


def test_derive_parent_reference_on_a_result_with_no_committed_subjects():
    sys.path.insert(0, str(HERE))
    import importlib
    import harness
    importlib.reload(harness)
    ref = harness.derive_parent_reference({"result_id": "res-y",
                                            "subject_resolution": {"committed": []}})
    assert ref == {"parentResultId": "res-y", "subjectHints": []}


def test_derive_parent_reference_tolerates_a_missing_subject_resolution_key():
    """Defence in depth: a result missing `subject_resolution` entirely (should not
    happen per the contract, but this instrument never trusts an unvalidated shape --
    same discipline validators.py itself documents) reads as no committed subjects,
    never a crash."""
    sys.path.insert(0, str(HERE))
    import importlib
    import harness
    importlib.reload(harness)
    ref = harness.derive_parent_reference({"result_id": "res-z"})
    assert ref == {"parentResultId": "res-z", "subjectHints": []}


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
