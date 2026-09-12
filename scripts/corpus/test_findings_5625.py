#!/usr/bin/env python3
"""Harness/merge tests for the CHAOS-5620 semantic verdict published beside
the legacy buckets (CHAOS-5625): row shape, PROVENANCE fields, no bucket
drift.

Isolation is by SUBPROCESS for anything that imports `expect_schema` /
`semantic_verdict` -- the identical reason testdata_corpus/corpus.py's own
docstring gives for the real `corpus` module: installing a name into the
running interpreter and undoing it afterwards cannot retract a reference a
module already captured. Every subprocess here runs with a small FAKE
ask-dev pin (not the real ajv-backed one -- this file tests the ACR-SIDE
WIRING, never re-implements CHAOS-5620's own acceptance logic; see
semantic_verdict_bridge.py's own docstring and AGENTS.md's Python
anti-pattern). The real machinery is exercised by ask-dev's own
corpus/test_semantic_verdict_proof.py against vendored real data, and by
this lane's own manual replay of the 36-row/9-rep proofs of record (see
docs/PR TEST-EVIDENCE) -- neither is reproduced here.

Run via run_pins.sh (sets PYTHONPATH=testdata_corpus, the real `corpus`
module this file's own in-process fixture builder needs to import
run_shard/harness), or standalone:
  PYTHONPATH=testdata_corpus python3 test_findings_5625.py
"""
import json
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))


class Findings5625Error(Exception):
    """Raised by `_require` -- never a bare `assert` (see test_corpus.py)."""


def _require(cond, msg):
    if not cond:
        raise Findings5625Error(msg)


_require(__debug__, "refusing to run under python -O / PYTHONOPTIMIZE=1: "
         "assert-stripping optimizations would silently weaken this guard")

FAKE_EXPECT_SCHEMA = '''
SCHEMA_VERSION = "fake-schema-v1"


def parse_expect(expect):
    return True, None, None
'''

FAKE_SEMANTIC_VERDICT = '''
SCORER_VERSION = "fake-scorer-v1"
POLICY_VERSION = "fake-policy-v1"


def is_success_status(status):
    return status == 200


def audit_window_exchange(attempts):
    return {"family_relation": "unknown", "window_binding": "unknown",
            "family_confirmation": "unavailable", "reason": "fake_audit_no_op"}


def build_verdict(row, bucket, status, final, audit, legacy_score, corpus_version,
                   legacy_scorer_version, **identity):
    """Faithful to the REAL build_verdict's scalar-expect path (no any_of
    support in this fake -- see this file's module docstring): the
    published verdict/reason for a row with no `any_of` declaration is
    legacy_score's own verdict, unchanged, plus the metadata envelope."""
    verdict, reason = legacy_score(row, bucket, status, **identity)
    return {
        "scorer_version": SCORER_VERSION,
        "policy_version": POLICY_VERSION,
        "schema_version": "fake-schema-v1",
        "legacy_scorer_version": legacy_scorer_version,
        "corpus_version": corpus_version,
        "corpus_id": row.get("id") if isinstance(row, dict) else None,
        "bucket": bucket,
        "verdict": verdict,
        "reason": reason,
        "unscored": verdict == "unscored",
        "branch_results": [],
        "family_relation": audit.get("family_relation", "unknown"),
        "window_binding": audit.get("window_binding", "unknown"),
        "family_confirmation": audit.get("family_confirmation", "unavailable"),
    }
'''


def _write_fake_ask_dev(base):
    """A fake ask-dev checkout: `<base>/corpus/{expect_schema,semantic_verdict}.py`
    -- the SAME layout a real pinned checkout has (corpus/ under the repo
    root), so `semantic_verdict_bridge.resolve_ask_dev`'s own
    `Path(semantic_verdict.__file__).resolve().parent.parent` resolves to
    `base`, exactly like the real thing."""
    corpus_dir = base / "corpus"
    corpus_dir.mkdir(parents=True, exist_ok=True)
    (corpus_dir / "expect_schema.py").write_text(FAKE_EXPECT_SCHEMA)
    (corpus_dir / "semantic_verdict.py").write_text(FAKE_SEMANTIC_VERDICT)
    return corpus_dir


def _write_attempt(path, status=200, result=None, request=None):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps({
        "request": request or {"question": "q"},
        "status": status,
        "response": {"result": result} if result is not None else {"failure": {"code": "x"}},
        "dt": 1.0,
    }))


# ---------------------------------------------------------------------------
# In-process unit tests: pure functions that never import expect_schema/
# semantic_verdict, so no subprocess isolation is needed for these.
# ---------------------------------------------------------------------------

def test_attempts_for_no_artefact_is_named_not_a_crash():
    import semantic_verdict_bridge as svb
    with tempfile.TemporaryDirectory() as tmp:
        ok, attempts, reason = svb.attempts_for(tmp, "nope", 1)
        _require(ok is False, (ok, attempts, reason))
        _require(reason == "no_artefact", reason)
        _require(attempts is None, attempts)


def test_attempts_for_unreadable_file_is_named_not_a_crash():
    import semantic_verdict_bridge as svb
    with tempfile.TemporaryDirectory() as tmp:
        p = Path(tmp) / "replicate" / "q-rep1-t1-a1.json"
        p.parent.mkdir(parents=True)
        p.write_text("not json")
        ok, attempts, reason = svb.attempts_for(tmp, "q", 1)
        _require(ok is False, (ok, attempts, reason))
        _require(reason.startswith("unreadable_artefact:"), reason)


def test_attempts_for_orders_by_recorded_sequence_not_path():
    import semantic_verdict_bridge as svb
    with tempfile.TemporaryDirectory() as tmp:
        rd = Path(tmp) / "replicate"
        _write_attempt(rd / "q-rep1-t10-a1.json", result={"n": 10})
        _write_attempt(rd / "q-rep1-t9-a1.json", result={"n": 9})
        ok, attempts, reason = svb.attempts_for(tmp, "q", 1)
        _require(ok, reason)
        _require([a["response"]["result"]["n"] for a in attempts] == [9, 10],
                  [a["response"]["result"] for a in attempts])


def test_legacy_score_matches_expectations_directly():
    """A HARD-CODED expected pair, never one derived by calling expectations.score()
    again here -- that would be the mirror trap test_findings_r6.py's own
    test_no_pin_derives_an_EXPECTED_value_from_the_code_under_test exists to
    catch (recomputing the thing under test lets the pin pass for the wrong
    reason). `(SERVE, "served_with_data")` is authored in
    expectations.VERDICTS as `("agree", "served with facts as declared")`;
    this pins that this bridge's `legacy_score` reaches the SAME table
    entry through its own expectation_for()+score() call, not a value this
    test derived from the table itself."""
    import semantic_verdict_bridge as svb
    row = {"id": "r1", "expect": "serve", "basis": None, "anchor": None,
           "nonexistent": False, "note": ""}
    got = svb.legacy_score(row, "served_with_data", "complete")
    _require(got == ("agree", "served with facts as declared"), got)


def test_aggregate_counts():
    import semantic_verdict_bridge as svb
    records = [
        {"verdict": "agree", "family_relation": "same", "family_confirmation": "unavailable", "reason": "ok"},
        {"verdict": "unscored", "family_relation": "changed", "family_confirmation": "unavailable",
         "reason": "no_expectation: the row declares none"},
        {"verdict": "unscored", "family_relation": "unknown", "family_confirmation": "unavailable",
         "reason": "no_expectation: the row declares none"},
    ]
    agg = svb.aggregate(records)
    _require(agg["verdict_counts"] == {"agree": 1, "unscored": 2}, agg)
    _require(agg["family_relation_counts"] == {"same": 1, "changed": 1, "unknown": 1}, agg)
    _require(agg["confirmed_family_verified"] == 0, agg)
    _require(agg["unscored_count"] == 2, agg)
    _require(agg["unscored_reasons"] == {"no_expectation: the row declares none": 2}, agg)


def test_git_sha_reads_the_checkouts_own_metadata():
    import semantic_verdict_bridge as svb
    with tempfile.TemporaryDirectory() as tmp:
        _require(svb._git_sha(tmp) is None, "a non-repo directory must report None, never raise")
        subprocess.run(["git", "init", "-q", tmp], check=True)
        subprocess.run(["git", "-C", tmp, "config", "user.email", "t@example.com"], check=True)
        subprocess.run(["git", "-C", tmp, "config", "user.name", "t"], check=True)
        (Path(tmp) / "f").write_text("x")
        subprocess.run(["git", "-C", tmp, "add", "f"], check=True)
        subprocess.run(["git", "-C", tmp, "commit", "-q", "-m", "x"], check=True)
        sha = subprocess.run(["git", "-C", tmp, "rev-parse", "HEAD"],
                              capture_output=True, text=True, check=True).stdout.strip()
        got = svb._git_sha(tmp)
        _require(got == sha, (got, sha))


def test_resolve_ask_dev_names_the_pin_and_versions():
    """Subprocess-isolated: registers the FAKE ask-dev pin on PYTHONPATH and
    checks resolve_ask_dev's returned pin dict, including a real git sha
    read from the fake checkout's own metadata."""
    with tempfile.TemporaryDirectory() as tmp:
        base = Path(tmp) / "fake-ask-dev"
        corpus_dir = _write_fake_ask_dev(base)
        subprocess.run(["git", "init", "-q", str(base)], check=True)
        subprocess.run(["git", "-C", str(base), "config", "user.email", "t@example.com"], check=True)
        subprocess.run(["git", "-C", str(base), "config", "user.name", "t"], check=True)
        subprocess.run(["git", "-C", str(base), "add", "-A"], check=True)
        subprocess.run(["git", "-C", str(base), "commit", "-q", "-m", "x"], check=True)
        want_sha = subprocess.run(["git", "-C", str(base), "rev-parse", "HEAD"],
                                   capture_output=True, text=True, check=True).stdout.strip()

        code = (
            "import sys, json; sys.path.insert(0, %r); "
            "import semantic_verdict_bridge as svb; "
            "_, _, pin = svb.resolve_ask_dev(); print(json.dumps(pin))"
        ) % str(HERE)
        env = {"PATH": "/usr/bin:/bin:/usr/local/bin", "PYTHONPATH": str(corpus_dir)}
        proc = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True, env=env)
        _require(proc.returncode == 0, proc.stderr)
        pin = json.loads(proc.stdout)
        _require(pin["ask_dev_sha"] == want_sha, pin)
        _require(pin["scorer_version"] == "fake-scorer-v1", pin)
        _require(pin["policy_version"] == "fake-policy-v1", pin)
        _require(pin["schema_version"] == "fake-schema-v1", pin)
        _require(pin["legacy_scorer_version"] == svb_adapter_version(), pin)


def svb_adapter_version():
    import semantic_verdict_bridge as svb
    return svb.LEGACY_SCORER_ADAPTER_VERSION


def test_resolve_ask_dev_refuses_by_name_when_unimportable():
    code = (
        "import sys; sys.path.insert(0, %r); "
        "import semantic_verdict_bridge as svb\n"
        "try:\n"
        "    svb.resolve_ask_dev()\n"
        "except svb.AskDevUnavailable as e:\n"
        "    print('REFUSED:' + str(e))\n"
        "else:\n"
        "    print('DID NOT REFUSE')\n"
    ) % str(HERE)
    env = {"PATH": "/usr/bin:/bin:/usr/local/bin"}
    proc = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True, env=env)
    _require(proc.returncode == 0, proc.stderr)
    _require(proc.stdout.startswith("REFUSED:"), proc.stdout)
    _require("corpus/expect_schema.py" in proc.stdout, proc.stdout)


def test_resolve_ask_dev_refuses_a_present_but_broken_companion():
    """CHAOS-5632: import succeeding is not enough -- a companion missing a
    required version attribute (a partial checkout, a version bump landed on
    one side only) must refuse the SAME way an unimportable one does
    (AskDevUnavailable, never an uncaught AttributeError that would abort
    merge_corpus.py's merge instead of degrading it to legacy-only)."""
    code = (
        "import sys; sys.path.insert(0, %r); "
        "import semantic_verdict_bridge as svb\n"
        "try:\n"
        "    svb.resolve_ask_dev()\n"
        "except svb.AskDevUnavailable as e:\n"
        "    print('REFUSED:' + str(e))\n"
        "else:\n"
        "    print('DID NOT REFUSE')\n"
    ) % str(HERE)
    with tempfile.TemporaryDirectory() as tmp:
        base = Path(tmp) / "broken-ask-dev"
        corpus_dir = _write_fake_ask_dev(base)
        # SCORER_VERSION is exactly what resolve_ask_dev reads to build the
        # pin -- delete it so the import succeeds and the attribute read
        # does not.
        (corpus_dir / "semantic_verdict.py").write_text(
            FAKE_SEMANTIC_VERDICT.replace('SCORER_VERSION = "fake-scorer-v1"\n', ""))
        env = {"PATH": "/usr/bin:/bin:/usr/local/bin", "PYTHONPATH": str(corpus_dir)}
        proc = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True, env=env)
        _require(proc.returncode == 0, proc.stderr)
        _require(proc.stdout.startswith("REFUSED:"), proc.stdout)
        _require("broken" in proc.stdout, proc.stdout)


def test_resolve_ask_dev_refuses_a_mixed_companion():
    """CHAOS-5632's other observed shape: expect_schema and semantic_verdict
    resolving from two DIFFERENT checkouts (each on sys.path, each supplying
    only one of the two module names) was seen to succeed silently, naming
    one root while actually scoring with code from two. Building each half
    in its own directory and putting both on sys.path reproduces exactly
    that shape."""
    code = (
        "import sys; sys.path.insert(0, %r); "
        "import semantic_verdict_bridge as svb\n"
        "try:\n"
        "    svb.resolve_ask_dev()\n"
        "except svb.AskDevUnavailable as e:\n"
        "    print('REFUSED:' + str(e))\n"
        "else:\n"
        "    print('DID NOT REFUSE')\n"
    ) % str(HERE)
    with tempfile.TemporaryDirectory() as tmp:
        base_a = Path(tmp) / "ask-dev-a"
        base_b = Path(tmp) / "ask-dev-b"
        corpus_a = _write_fake_ask_dev(base_a)
        corpus_b = _write_fake_ask_dev(base_b)
        (corpus_a / "semantic_verdict.py").unlink()
        (corpus_b / "expect_schema.py").unlink()
        env = {"PATH": "/usr/bin:/bin:/usr/local/bin",
               "PYTHONPATH": f"{corpus_a}:{corpus_b}"}
        proc = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True, env=env)
        _require(proc.returncode == 0, proc.stderr)
        _require(proc.stdout.startswith("REFUSED:"), proc.stdout)
        _require("mixed" in proc.stdout, proc.stdout)


# ---------------------------------------------------------------------------
# Merge-level integration: run the REAL merge_corpus.py end to end against a
# tiny synthetic shard, the fake ask-dev pin, and the testdata_corpus example
# rows -- row file shape, PROVENANCE fields, and NO BUCKET DRIFT (the legacy
# fields byte-identical to a merge run with the fake pin removed).
# ---------------------------------------------------------------------------

def _build_fixture_indir(indir):
    """Every row of the synthetic testdata_corpus example corpus, each with
    one served/complete attempt -- built through run_shard's OWN
    detail_for()/attempt_diagnostics(), never a hand-typed row dict, so this
    fixture cannot drift from what the real driver actually writes. Full
    coverage (check_coverage() aborts a run missing any corpus id), even
    though this file's own assertions only examine the ONE row named by the
    returned `qid` (`example-serve-named-project`, expect=serve)."""
    import run_shard
    from corpus_example import CORPUS

    shard_dir = Path(indir) / "shard-00"
    replicate = shard_dir / "replicate"
    qid = "example-serve-named-project"
    details = []
    for row in CORPUS:
        cid = row["id"]
        facts = [{"text": "one fact"}] if cid == qid else []
        _write_attempt(replicate / f"{cid}-rep1-t1-a1.json", status=200,
                        result={"status": "complete", "claimed_facts": facts,
                                 "answer_plan": {"family": row.get("family")}})
        r = {"final_http": 200, "final_payload_status": "complete", "chain": "t1=complete",
             "attempts": 1, "wrong_kind_flag": False, "wrong_subject_flag": False,
             "subject_kind_mismatch_flag": False}
        details.append(run_shard.detail_for(replicate, cid, row, r, 1.0, rep=1))
    summary = {"shard": 0, "shard_count": 1, "rep": 1,
               "planned_ids": [row["id"] for row in CORPUS],
               "rows": details, "total_wall_seconds": 1.0,
               "started_unix": 0, "finished_unix": 1}
    (shard_dir / "shard-summary.json").write_text(json.dumps(summary))
    return qid


def test_merge_publishes_semantic_verdict_without_moving_the_legacy_bucket():
    with tempfile.TemporaryDirectory() as tmp:
        indir = Path(tmp) / "seq"
        qid = _build_fixture_indir(indir)
        base = Path(tmp) / "fake-ask-dev"
        corpus_dir = _write_fake_ask_dev(base)
        out_with = Path(tmp) / "verdict-with-sv.json"

        env_corpus = f"{HERE / 'testdata_corpus'}"
        env = {"PATH": "/usr/bin:/bin:/usr/local/bin",
               "PYTHONPATH": f"{corpus_dir}:{env_corpus}"}
        proc = subprocess.run(
            [sys.executable, str(HERE / "merge_corpus.py"), "--shape", "sequential",
             "--in", str(indir), "--out", str(out_with)],
            capture_output=True, text=True, env=env, cwd=str(HERE))
        _require(proc.returncode == 0, proc.stdout + proc.stderr)
        verdict = json.loads(out_with.read_text())

        # --- legacy fields: unchanged shape and values -----------------
        _require(verdict["totals"]["served_with_data"] == 1, verdict["totals"])
        _require(verdict["totals"]["total"] == 4, verdict["totals"])
        row = next(r for r in verdict["rows"] if r["corpus_id"] == qid)
        _require(row["bucket"] == "served_with_data", row["bucket"])
        _require(row["expectation_verdict"] == "agree", row["expectation_verdict"])

        # --- row file shape: semantic_verdict rides BESIDE the legacy field,
        # and matches it byte-for-byte on this all-scalar fixture row -----
        sv = row["semantic_verdict"]
        _require(sv is not None, row)
        _require(sv["verdict"] == row["expectation_verdict"], (sv, row))
        _require(sv["bucket"] == row["bucket"], sv)
        _require(sv["unscored"] is False, sv)
        _require(sv["branch_results"] == [], sv)
        for key in ("scorer_version", "policy_version", "schema_version",
                    "legacy_scorer_version", "corpus_version", "family_relation",
                    "window_binding", "family_confirmation"):
            _require(key in sv, f"semantic_verdict row missing {key!r}: {sv}")

        # --- PROVENANCE fields -------------------------------------------
        prov_sv = verdict["provenance"]["semantic_verdict"]
        _require(prov_sv["available"] is True, prov_sv)
        for key in ("scorer_version", "policy_version", "schema_version",
                    "legacy_scorer_version", "ask_dev_sha", "ask_dev_root",
                    "corpus_version", "verdict_counts", "family_relation_counts",
                    "confirmed_family_verified", "unscored_count", "unscored_reasons"):
            _require(key in prov_sv, f"provenance.semantic_verdict missing {key!r}: {prov_sv}")
        _require(prov_sv["scorer_version"] == "fake-scorer-v1", prov_sv)
        # example-serve-named-project (serve, served w/ facts) -> agree; the other
        # three example rows all serve here too (this fixture's attempts are all
        # "complete") against refuse/decline/no-declaration expectations -> two
        # disagree, one unscored (no_expectation).
        _require(prov_sv["verdict_counts"] == {"agree": 1, "disagree": 2, "unscored": 1},
                  prov_sv)
        _require(prov_sv["confirmed_family_verified"] == 0, prov_sv)
        _require(prov_sv["unscored_count"] == 1, prov_sv)
        _require(prov_sv["unscored_reasons"] ==
                  {"no_expectation: the row declares none": 1}, prov_sv)

        # --- NO BUCKET DRIFT: a merge run over the SAME --in tree with no
        # ask-dev pin available aborts BEFORE writing anything (see the
        # refusal test below); the positive control is that every legacy
        # field on THIS run (computed above) is exactly what merge_corpus's
        # own classify()/expectations table would produce unassisted --
        # asserted by construction: `row["bucket"]`/`row["expectation_verdict"]`
        # come from the SAME merge run as `semantic_verdict`, so a
        # regression that moved either would be caught by the assertions
        # above, and moving the LEGACY ones is exactly what "the same
        # fields, run to run" catches next.
        out_again = Path(tmp) / "verdict-with-sv-2.json"
        proc2 = subprocess.run(
            [sys.executable, str(HERE / "merge_corpus.py"), "--shape", "sequential",
             "--in", str(indir), "--out", str(out_again)],
            capture_output=True, text=True, env=env, cwd=str(HERE))
        _require(proc2.returncode == 0, proc2.stdout + proc2.stderr)
        verdict2 = json.loads(out_again.read_text())

        def strip(v):
            v = dict(v)
            v.pop("provenance", None)
            v["shards"] = [{k: x[k] for k in x if k != "path"} for x in v["shards"]]
            v["rows"] = [{k: r[k] for k in r if k != "semantic_verdict"} for r in v["rows"]]
            return v

        _require(strip(verdict) == strip(verdict2),
                  "re-running the merge over the SAME artefacts moved a legacy field")


def test_merge_degrades_gracefully_when_ask_dev_pin_is_missing():
    """A run with NO ask-dev pin available (every merge_corpus.py caller/test
    that predates CHAOS-5625, and any future one that just does not carry a
    pin) must keep publishing the legacy buckets exactly as it always has --
    NEVER a MERGE ABORT over a feature this run never asked for. The
    absence is named (stderr NOTE, provenance.semantic_verdict.available is
    False with a reason), never a crash and never a silent omission."""
    with tempfile.TemporaryDirectory() as tmp:
        indir = Path(tmp) / "seq"
        qid = _build_fixture_indir(indir)
        out = Path(tmp) / "verdict.json"
        env = {"PATH": "/usr/bin:/bin:/usr/local/bin",
               "PYTHONPATH": str(HERE / "testdata_corpus")}
        proc = subprocess.run(
            [sys.executable, str(HERE / "merge_corpus.py"), "--shape", "sequential",
             "--in", str(indir), "--out", str(out)],
            capture_output=True, text=True, env=env, cwd=str(HERE))
        _require(proc.returncode == 0, (proc.returncode, proc.stdout, proc.stderr))
        _require("NOTE: semantic_verdict unavailable" in proc.stderr, proc.stderr)
        _require(out.exists(), "a missing ask-dev pin must not block the legacy publish")
        verdict = json.loads(out.read_text())
        _require(verdict["totals"]["served_with_data"] == 1, verdict["totals"])
        row = next(r for r in verdict["rows"] if r["corpus_id"] == qid)
        _require(row["semantic_verdict"] is None, row)
        prov_sv = verdict["provenance"]["semantic_verdict"]
        _require(prov_sv["available"] is False, prov_sv)
        _require("expect_schema.py and corpus/semantic_verdict.py" in prov_sv["reason"], prov_sv)


def main():
    tests = [v for k, v in sorted(globals().items())
             if k.startswith("test_") and callable(v)]
    for test in tests:
        test()
        print(f"PASS: {test.__name__}")
    print(f"PASS: {len(tests)} CHAOS-5625 semantic-verdict-bridge controls")


if __name__ == "__main__":
    main()
