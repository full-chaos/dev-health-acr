"""Pins for CHAOS-5562: the corpus harness must refuse to run without an explicit
CORPUS_BASE.

INCIDENT (dictation 978, 2026-09-11 ~02:01:30-02:03:10Z): `harness.py:66` defaulted
CORPUS_BASE to `http://127.0.0.1:3040/api/investigations` -- a shared rig leg.
A lane drove the harness inline without setting the variable and sent 5
investigation requests to a leg it did not own before anyone noticed. The fix has three parts, each pinned
below against a REAL local HTTP server (never a mock of urllib, never a live rig
port):

  1. NO DEFAULT. `harness.BASE` is `os.environ.get("CORPUS_BASE")` with no fallback,
     and `harness.py` (run directly) / `run_shard.py` (run directly, bypassing the
     two shell launchers that already export their own default) both refuse before
     doing any work, naming the variable.
  2. The base and the FIRST response's `service_version` are printed before any
     SECOND request goes out -- so a caller pointed at the wrong leg finds out after
     one request, not after five.
  3. An optional expected build (env `CORPUS_EXPECTED_BUILD`, or `harness.py`'s own
     `--expected-build VALUE` CLI flag, which overrides the env for that run) makes
     the harness refuse the instant the first response's `service_version` disagrees
     -- again after exactly one request, never more.

Every subprocess test runs against an ISOLATED COPY of the corpus scripts in a temp
directory (never the real `scripts/corpus/replicate/` -- `harness.OUTDIR` is derived
from `Path(__file__).parent`, so running the real files in place would leave stray
JSON in the repo tree), with the synthetic `testdata_corpus` corpus on PYTHONPATH,
exactly as `run_pins.sh` supplies it.
"""
import json
import os
import shutil
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).parent
TESTDATA = HERE / "testdata_corpus"

# The closed set of local modules harness.py / run_shard.py import, transitively.
# Copied together so an isolated invocation resolves them exactly as the real
# directory does, never against the real replicate/shards output dirs.
_COPY_FILES = ("harness.py", "validators.py", "contract.py", "attempt_classes.py",
               "attempt_order.py", "shard_plan.py", "run_shard.py", "artefact_schema.json",
               "reclassify_deadlines.py", "merge_corpus.py", "engine_failures.py",
               "subject_identity.py", "expectations.py", "corpus_example.py")

CORPUS_ID = "example-serve-named-project"  # a real id in testdata_corpus/corpus_example.py


def _isolated_copy(tmp):
    dest = Path(tmp) / "corpusdir"
    dest.mkdir()
    for name in _COPY_FILES:
        shutil.copy2(HERE / name, dest / name)
    return dest


def _env(**overrides):
    env = dict(os.environ)
    # TESTDATA supplies the `corpus` module; HERE supplies `corpus_example`, which
    # `testdata_corpus/corpus.py` imports from -- real invocations get this for free
    # because run_pins.sh runs each pin file with HERE as cwd (sys.path[0]), but an
    # isolated copy in a tempdir does not, so it is named explicitly here.
    env["PYTHONPATH"] = f"{TESTDATA}:{HERE}:{env.get('PYTHONPATH', '')}"
    # Never inherit a real CORPUS_BASE / CORPUS_EXPECTED_BUILD from the launching
    # shell -- each test states exactly what it wants unset or set.
    env.pop("CORPUS_BASE", None)
    env.pop("CORPUS_EXPECTED_BUILD", None)
    env.update(overrides)
    return env


def _run(script, *args, env_overrides=None, cwd=None):
    return subprocess.run(
        [sys.executable, str(script), *args],
        env=_env(**(env_overrides or {})), capture_output=True, text=True,
        timeout=30, cwd=str(cwd or script.parent))


class _StubServer:
    """A REAL local HTTP server (port 0, 127.0.0.1-only) serving one fixed, terminal
    200 body and counting the requests it actually received -- so a pin can prove
    the harness stopped after exactly one, not just that it eventually exited."""

    def __init__(self, service_version="build-A", status="complete"):
        from http.server import BaseHTTPRequestHandler, HTTPServer
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                outer.requests += 1
                length = int(self.headers.get("Content-Length") or 0)
                self.rfile.read(length)
                payload = json.dumps(outer.body).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

            def log_message(self, *_a):    # keep pin output readable
                pass

        self.requests = 0
        self.body = {"result": {"status": status,
                                 "versions": {"service_version": service_version}}}
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


# ==================================================== PIN 1: unset -> refuse

def test_unset_corpus_base_refuses_with_exit_code_and_message():
    import tempfile
    with tempfile.TemporaryDirectory() as tmp:
        dest = _isolated_copy(tmp)
        r = _run(dest / "harness.py", CORPUS_ID)
    out = r.stdout + r.stderr
    assert r.returncode != 0, f"unset CORPUS_BASE exited 0:\n{out}"
    assert "CORPUS_BASE" in out, f"refusal does not name the variable: {out}"


def test_run_shard_also_refuses_when_corpus_base_is_unset():
    """CHAOS-5562 requirement 4: a lane invoking run_shard.py directly (bypassing
    run_corpus_sequential.sh / run_corpus_parallel.sh, which already export their
    own CORPUS_BASE) must not silently inherit the old default either."""
    import tempfile
    with tempfile.TemporaryDirectory() as tmp:
        dest = _isolated_copy(tmp)
        r = _run(dest / "run_shard.py", "0", "1", "1")
    out = r.stdout + r.stderr
    assert r.returncode != 0, f"unset CORPUS_BASE exited 0 for run_shard.py:\n{out}"
    assert "CORPUS_BASE" in out, f"refusal does not name the variable: {out}"


# ==================================================== PIN 2: set -> proceeds

def test_set_corpus_base_proceeds_and_reports_base_and_service_version():
    import tempfile
    with tempfile.TemporaryDirectory() as tmp:
        dest = _isolated_copy(tmp)
        with _StubServer(service_version="build-A") as srv:
            r = _run(dest / "harness.py", CORPUS_ID,
                      env_overrides={"CORPUS_BASE": srv.base})
        out = r.stdout + r.stderr
        assert r.returncode == 0, f"a set CORPUS_BASE still refused:\n{out}"
        assert srv.requests >= 1, "the harness never reached the stub server"
        assert f"CORPUS_BASE={srv.base}" in out, out
        assert "service_version='build-A'" in out, out


# ==================================================== PIN 3: expected-build mismatch -> refuse

def test_expected_build_mismatch_refuses_after_exactly_one_request():
    """The whole point of printing the first response's service_version before any
    further request: a caller pointed at the wrong leg (or the right leg on the
    wrong build) finds out after ONE request, never after five -- the incident's own
    shape, minimised."""
    import tempfile
    with tempfile.TemporaryDirectory() as tmp:
        dest = _isolated_copy(tmp)
        with _StubServer(service_version="build-A") as srv:
            r = _run(dest / "harness.py", CORPUS_ID,
                      env_overrides={"CORPUS_BASE": srv.base,
                                     "CORPUS_EXPECTED_BUILD": "build-B"})
        out = r.stdout + r.stderr
        assert r.returncode != 0, f"a build mismatch still exited 0:\n{out}"
        assert "build-A" in out and "build-B" in out, out
        assert srv.requests == 1, (
            f"expected-build mismatch let {srv.requests} requests through -- must "
            "refuse after exactly the first response")


def test_expected_build_cli_flag_overrides_env_and_still_refuses():
    """`--expected-build` on harness.py's own CLI overrides CORPUS_EXPECTED_BUILD,
    not merely adds to it."""
    import tempfile
    with tempfile.TemporaryDirectory() as tmp:
        dest = _isolated_copy(tmp)
        with _StubServer(service_version="build-A") as srv:
            r = _run(dest / "harness.py", "--expected-build", "build-B", CORPUS_ID,
                      env_overrides={"CORPUS_BASE": srv.base,
                                     "CORPUS_EXPECTED_BUILD": "build-A"})
        out = r.stdout + r.stderr
        assert r.returncode != 0, f"the CLI flag's mismatch was not honoured:\n{out}"
        assert srv.requests == 1, out


# ==================================================== PIN 4: expected-build match -> proceeds

def test_expected_build_match_proceeds():
    import tempfile
    with tempfile.TemporaryDirectory() as tmp:
        dest = _isolated_copy(tmp)
        with _StubServer(service_version="build-A") as srv:
            r = _run(dest / "harness.py", CORPUS_ID,
                      env_overrides={"CORPUS_BASE": srv.base,
                                     "CORPUS_EXPECTED_BUILD": "build-A"})
        out = r.stdout + r.stderr
        assert r.returncode == 0, f"a matching expected build still refused:\n{out}"
        assert srv.requests >= 1


def test_expected_build_cli_flag_match_proceeds():
    import tempfile
    with tempfile.TemporaryDirectory() as tmp:
        dest = _isolated_copy(tmp)
        with _StubServer(service_version="build-A") as srv:
            r = _run(dest / "harness.py", "--expected-build=build-A", CORPUS_ID,
                      env_overrides={"CORPUS_BASE": srv.base})
        out = r.stdout + r.stderr
        assert r.returncode == 0, f"a matching CLI --expected-build still refused:\n{out}"
        assert srv.requests >= 1


# ==================================================== supporting unit pins

def test_service_version_extraction_matches_run_shards_own_shape():
    """The same reading `run_shard.py`'s `detail_for` already does off
    `versions.service_version` -- one shape, never two that could disagree."""
    import harness
    assert harness._service_version(
        {"result": {"versions": {"service_version": "x"}}}) == "x"
    assert harness._service_version({"result": {}}) is None
    assert harness._service_version({"result": {"versions": {}}}) is None
    assert harness._service_version({}) is None
    assert harness._service_version("not a dict") is None
    assert harness._service_version(None) is None


def test_require_base_names_the_variable_and_base_is_none_when_unset():
    import importlib
    saved = os.environ.pop("CORPUS_BASE", None)
    try:
        sys.path.insert(0, str(HERE))
        import harness
        importlib.reload(harness)
        assert harness.BASE is None, "harness.BASE silently defaulted"
        try:
            harness.require_base()
            assert False, "require_base() did not raise with CORPUS_BASE unset"
        except harness.MissingCorpusBase as e:
            assert "CORPUS_BASE" in str(e), e
    finally:
        if saved is not None:
            os.environ["CORPUS_BASE"] = saved
        import harness
        importlib.reload(harness)   # leave the shared module state clean


def test_require_base_is_silent_once_corpus_base_is_set():
    import importlib
    saved = os.environ.get("CORPUS_BASE")
    try:
        os.environ["CORPUS_BASE"] = "http://127.0.0.1:1/api/investigations"
        import harness
        importlib.reload(harness)
        harness.require_base()   # must not raise
    finally:
        if saved is None:
            os.environ.pop("CORPUS_BASE", None)
        else:
            os.environ["CORPUS_BASE"] = saved
        import harness
        importlib.reload(harness)


# ==================================================== requirement 4: the two shell
# launchers require CORPUS_BASE explicitly, same as the harness -- team-lead ruling
# after r1: finding 3 (launchers still defaulted to the shared rig) is IN SCOPE, same
# class as the ticket. No launcher anywhere in scripts/ may default CORPUS_BASE.

def test_the_shell_launchers_refuse_before_exporting_an_unset_corpus_base():
    for name in ("run_corpus_sequential.sh", "run_corpus_parallel.sh"):
        t = (HERE / name).read_text()
        assert "http://127.0.0.1:3040" not in t, (
            f"{name} still hard-codes the old default base")
        assert '${CORPUS_BASE:?' in t, f"{name} no longer refuses an unset CORPUS_BASE"
        assert "export CORPUS_BASE" in t, f"{name} no longer exports CORPUS_BASE"
        invoke = 'python3 "$HERE/run_shard.py"'
        assert invoke in t, f"{name} no longer invokes run_shard.py the expected way"
        refuse_at = t.index('${CORPUS_BASE:?')
        assert refuse_at < t.index("export CORPUS_BASE") < t.index(invoke), (
            f"{name} does not refuse-then-export CORPUS_BASE before invoking run_shard.py")


# ==================================================== r1 review findings, fixed + pinned

class _SequencedServer:
    """Like `_StubServer`, but serves a DIFFERENT scripted (status, body) per request in
    order, holding the last one for any request past the end of the list -- needed to
    prove the build-mismatch check survives a retryable failure as attempt 1."""

    def __init__(self, responses):
        from http.server import BaseHTTPRequestHandler, HTTPServer
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                length = int(self.headers.get("Content-Length") or 0)
                self.rfile.read(length)
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


def test_a_retryable_first_response_does_not_disarm_the_build_check():
    """r1 review, P1, harness.py:129 (pre-fix line): a retryable failure with no
    `service_version` as attempt 1 used to consume the one-shot report/check flag --
    attempt 2 then served a DIFFERENT (wrong) build and nothing caught it. The check
    must stay armed until a response actually yields a determinate service_version."""
    import tempfile
    with tempfile.TemporaryDirectory() as tmp:
        dest = _isolated_copy(tmp)
        with _SequencedServer([
                (503, {"failure": {"code": "acr_upstream_deadline", "retryable": True}}),
                (200, {"result": {"status": "complete",
                                  "versions": {"service_version": "build-A"}}}),
        ]) as srv:
            r = _run(dest / "harness.py", CORPUS_ID,
                      env_overrides={"CORPUS_BASE": srv.base,
                                     "CORPUS_EXPECTED_BUILD": "build-B"})
        out = r.stdout + r.stderr
        assert r.returncode != 0, (
            f"a retryable failure as attempt 1 disarmed the build-mismatch check -- "
            f"attempt 2's wrong build was never caught:\n{out}")
        assert "build-A" in out and "build-B" in out, out
        assert srv.requests == 2, (
            f"expected exactly 2 requests (the retry, then the mismatch refusal): "
            f"{srv.requests}")


def test_corpus_base_credentials_and_query_are_never_printed():
    """r1 review, P2, harness.py:133 (pre-fix line): the diagnostic print carried
    CORPUS_BASE verbatim, which can embed basic-auth credentials or a query token --
    logging it straight into shard logs. Redacted to scheme+host+path only."""
    import importlib
    saved = os.environ.get("CORPUS_BASE")
    try:
        os.environ["CORPUS_BASE"] = (
            "http://alice:s3cr3t-pw@example.invalid:8443/api/investigations"
            "?access_token=q-secret-tok")
        import harness
        importlib.reload(harness)
        redacted = harness._redacted_base()
        assert "s3cr3t-pw" not in redacted, redacted
        assert "alice" not in redacted, redacted
        assert "q-secret-tok" not in redacted, redacted
        assert "access_token" not in redacted, redacted
        assert redacted == "http://example.invalid:8443/api/investigations", redacted
    finally:
        if saved is None:
            os.environ.pop("CORPUS_BASE", None)
        else:
            os.environ["CORPUS_BASE"] = saved
        import harness
        importlib.reload(harness)


def test_corpus_base_with_no_credentials_prints_unchanged():
    """Negative control for the redaction above: a base with nothing to strip must
    come back byte-identical, or the redaction could be silently over-eager."""
    import importlib
    saved = os.environ.get("CORPUS_BASE")
    try:
        os.environ["CORPUS_BASE"] = "http://127.0.0.1:9999/api/investigations"
        import harness
        importlib.reload(harness)
        assert harness._redacted_base() == "http://127.0.0.1:9999/api/investigations"
    finally:
        if saved is None:
            os.environ.pop("CORPUS_BASE", None)
        else:
            os.environ["CORPUS_BASE"] = saved
        import harness
        importlib.reload(harness)


def _launcher_dir(tmp):
    """A throwaway copy of one launcher plus everything it shells out to, with
    `python3`/`curl` stubbed on PATH: `python3` intercepts run_shard.py/merge_corpus.py
    invocations (echoing what CORPUS_BASE it was handed) and delegates every other
    invocation (corpus_origin.sh's URL parse, shard_plan.py) to the REAL python3;
    `curl` always answers 200 so the readiness probe never needs a real rig."""
    real_python3 = shutil.which("python3")
    assert real_python3, "no real python3 on PATH to delegate to"
    dest = Path(tmp) / "launcherdir"
    dest.mkdir()
    for f in ("run_corpus_sequential.sh", "run_corpus_parallel.sh", "corpus_origin.sh",
              "run_shard.py", "merge_corpus.py", "shard_plan.py"):
        shutil.copy2(HERE / f, dest / f)
    stub_bin = Path(tmp) / "stubbin"
    stub_bin.mkdir()
    (stub_bin / "python3").write_text(
        "#!/usr/bin/env bash\n"
        'for a in "$@"; do\n'
        '  case "$a" in\n'
        '    */run_shard.py|*/merge_corpus.py)\n'
        '      echo "FAKE $(basename "$a") CORPUS_BASE=$CORPUS_BASE"\n'
        "      exit 0 ;;\n"
        "  esac\n"
        "done\n"
        f'exec "{real_python3}" "$@"\n')
    (stub_bin / "python3").chmod(0o755)
    (stub_bin / "curl").write_text("#!/usr/bin/env bash\nprintf '200'\n")
    (stub_bin / "curl").chmod(0o755)
    return dest, stub_bin


def _run_launcher(name, dest, stub_bin, corpus_base):
    env = dict(os.environ)
    if corpus_base is None:
        env.pop("CORPUS_BASE", None)
    else:
        env["CORPUS_BASE"] = corpus_base
    env["PATH"] = f"{stub_bin}:{env['PATH']}"
    # shard_plan.py (invoked for real by run_corpus_parallel.sh) needs the synthetic
    # corpus on PYTHONPATH, same as every other subprocess test in this file.
    env["PYTHONPATH"] = f"{TESTDATA}:{HERE}:{env.get('PYTHONPATH', '')}"
    args = [str(dest / name)] if name == "run_corpus_sequential.sh" else [str(dest / name), "1", "1"]
    return subprocess.run(["bash", *args], env=env, capture_output=True, text=True,
                          timeout=30, cwd=str(dest))


def test_the_shell_launchers_refuse_when_corpus_base_is_unset_executed():
    """r1 review team-lead ruling: finding 3 is in scope, same class as the ticket --
    the launchers must refuse an unset CORPUS_BASE too, executed, not just documented."""
    import tempfile
    for name in ("run_corpus_sequential.sh", "run_corpus_parallel.sh"):
        with tempfile.TemporaryDirectory() as tmp:
            dest, stub_bin = _launcher_dir(tmp)
            r = _run_launcher(name, dest, stub_bin, corpus_base=None)
        out = r.stdout + r.stderr
        assert r.returncode != 0, f"{name} exited 0 with CORPUS_BASE unset:\n{out}"
        assert "CORPUS_BASE" in out, f"{name}'s refusal does not name the variable: {out}"
        assert "FAKE" not in out, (
            f"{name} reached run_shard.py/merge_corpus.py despite CORPUS_BASE unset:\n{out}")


def test_the_shell_launchers_pass_a_set_corpus_base_through_to_run_shard_executed():
    """r1 review, P3 (pre-fix): the launcher pin checked source text and ordering
    only, never an executed run. Runs each REAL launcher with `python3`/`curl`
    stubbed on PATH, and asserts a CALLER-SUPPLIED CORPUS_BASE actually reaches
    run_shard.py unchanged -- proving requirement 4 by execution, not by reading
    source."""
    import tempfile
    base = "http://127.0.0.1:19999/api/investigations"
    for name in ("run_corpus_sequential.sh", "run_corpus_parallel.sh"):
        with tempfile.TemporaryDirectory() as tmp:
            dest, stub_bin = _launcher_dir(tmp)
            r = _run_launcher(name, dest, stub_bin, corpus_base=base)
            out = r.stdout + r.stderr
            # run_corpus_parallel.sh redirects each shard's run_shard.py invocation to
            # its own per-shard log file rather than the launcher's own stdout/stderr.
            for log in (dest / "logs").glob("shard-*.log"):
                out += "\n" + log.read_text()
        assert f"FAKE run_shard.py CORPUS_BASE={base}" in out, (
            f"{name} did not pass the caller's CORPUS_BASE through to run_shard.py:\n{out}")


# ==================================================== r2 review findings, fixed + pinned

def test_a_malformed_but_versioned_response_still_arms_the_build_check():
    """r2 review, P1, harness.py:268 (pre-fix line): `post()` checked the VALIDATED
    body, which `validate_live_payload` replaces with a bare failure envelope for any
    response malformed by some OTHER measure -- discarding a genuine
    `versions.service_version` before the build check ever saw it. The check must read
    the RAW decoded payload."""
    import tempfile
    with tempfile.TemporaryDirectory() as tmp:
        dest = _isolated_copy(tmp)
        # Malformed per validate_response (a candidate must be an object, this one is
        # an int) but genuinely carries a real, determinate service_version.
        malformed_but_versioned = {
            "result": {"status": "complete",
                       "versions": {"service_version": "build-A"},
                       "subject_resolution": {"committed": [1]}}}
        with _StubServer(service_version=None) as srv:
            srv.body = malformed_but_versioned
            r = _run(dest / "harness.py", CORPUS_ID,
                      env_overrides={"CORPUS_BASE": srv.base,
                                     "CORPUS_EXPECTED_BUILD": "build-B"})
        out = r.stdout + r.stderr
        assert r.returncode != 0, (
            f"a malformed-but-versioned response did not trip the build-mismatch "
            f"check:\n{out}")
        assert "build-A" in out and "build-B" in out, out
        assert srv.requests == 1, (
            f"expected exactly 1 request before refusing: {srv.requests}")


def _bare_isolated_copy(tmp, entry_files):
    """Like `_isolated_copy`, but WITHOUT `corpus_example.py` and WITHOUT the
    synthetic `corpus` module anywhere on PYTHONPATH -- there is genuinely no
    `corpus` module resolvable, matching the r2 review scenario."""
    dest = Path(tmp) / "bare"
    dest.mkdir()
    for name in entry_files:
        shutil.copy2(HERE / name, dest / name)
    return dest


def _run_bare(script, *args, env_overrides=None):
    env = dict(os.environ)
    env.pop("CORPUS_BASE", None)
    # Deliberately NOT adding TESTDATA/HERE to PYTHONPATH -- `corpus` must not resolve.
    env["PYTHONPATH"] = env.get("PYTHONPATH", "")
    env.update(env_overrides or {})
    return subprocess.run([sys.executable, str(script), *args], env=env,
                          capture_output=True, text=True, timeout=30,
                          cwd=str(script.parent))


def test_harness_refuses_before_importing_the_external_corpus_module():
    """r2 review, P2, harness.py:58 (pre-fix line): a direct run with CORPUS_BASE
    unset AND no `corpus` module supplied hit a raw `ModuleNotFoundError` instead of
    ever reaching the CORPUS_BASE message -- the module-level `from corpus import`
    runs before any of this file's own code. Now refuses before that import."""
    import tempfile
    with tempfile.TemporaryDirectory() as tmp:
        dest = _bare_isolated_copy(tmp, ("harness.py", "validators.py", "contract.py"))
        r = _run_bare(dest / "harness.py", CORPUS_ID)
    out = r.stdout + r.stderr
    assert r.returncode != 0, out
    assert "ModuleNotFoundError" not in out, (
        f"a missing corpus module leaked past the CORPUS_BASE refusal:\n{out}")
    assert "CORPUS_BASE" in out, f"refusal does not name the variable: {out}"


def test_run_shard_refuses_before_importing_the_external_corpus_module():
    """Same class as above, for run_shard.py's own `import harness` (which is what
    actually pulls in `corpus`)."""
    import tempfile
    with tempfile.TemporaryDirectory() as tmp:
        dest = _bare_isolated_copy(tmp, ("harness.py", "validators.py", "contract.py",
                                        "attempt_classes.py", "attempt_order.py",
                                        "shard_plan.py", "run_shard.py"))
        r = _run_bare(dest / "run_shard.py", "0", "1", "1")
    out = r.stdout + r.stderr
    assert r.returncode != 0, out
    assert "ModuleNotFoundError" not in out, (
        f"a missing corpus module leaked past the CORPUS_BASE refusal:\n{out}")
    assert "CORPUS_BASE" in out, f"refusal does not name the variable: {out}"


def test_reclassify_deadlines_also_refuses_when_corpus_base_is_unset():
    """r2 review caller sweep: reclassify_deadlines.py is ANOTHER direct caller of
    harness.run_replicate, missed by the r1 caller sweep because it never spells the
    literal string "harness.py" (only `import harness`). Same guard, same class."""
    import tempfile
    with tempfile.TemporaryDirectory() as tmp:
        dest = _bare_isolated_copy(tmp, ("harness.py", "validators.py", "contract.py",
                                        "attempt_classes.py", "attempt_order.py",
                                        "shard_plan.py", "run_shard.py",
                                        "reclassify_deadlines.py", "merge_corpus.py",
                                        "engine_failures.py", "subject_identity.py",
                                        "expectations.py"))
        shards_dir = dest / "shards"
        shards_dir.mkdir()
        r = _run_bare(dest / "reclassify_deadlines.py", str(shards_dir), "1")
    out = r.stdout + r.stderr
    assert r.returncode != 0, out
    assert "ModuleNotFoundError" not in out, (
        f"a missing corpus module leaked past the CORPUS_BASE refusal:\n{out}")
    assert "CORPUS_BASE" in out, f"refusal does not name the variable: {out}"


def test_launcher_abort_never_leaks_corpus_base_credentials_executed():
    """r2 review, P2, run_corpus_sequential.sh:35 / run_corpus_parallel.sh:54
    (pre-fix lines): the readiness-probe ABORT message printed the probe URL
    verbatim, and corpus_origin.sh's own origin extraction PRESERVES userinfo (by
    necessity -- the real request needs it) -- so a CORPUS_BASE carrying basic-auth
    credentials or a token leaked straight onto stderr. Redacted for logging only."""
    import tempfile
    base = "http://alice:s3cr3t-pw@example.invalid:8443/api/investigations?access_token=q-tok"
    for name in ("run_corpus_sequential.sh", "run_corpus_parallel.sh"):
        with tempfile.TemporaryDirectory() as tmp:
            dest, stub_bin = _launcher_dir(tmp)
            # Force the readiness probe to FAIL (unlike _launcher_dir's default 200
            # stub) so the ABORT line actually fires -- this pin is about that one
            # log line, not about whether run_shard.py ever sees the real value (it
            # must, unredacted, to make the real request -- separately pinned above).
            (stub_bin / "curl").write_text("#!/usr/bin/env bash\nprintf '503'\n")
            env = dict(os.environ)
            env["CORPUS_BASE"] = base
            env["PATH"] = f"{stub_bin}:{env['PATH']}"
            args = [str(dest / name)] if name == "run_corpus_sequential.sh" else \
                   [str(dest / name), "1", "1"]
            r = subprocess.run(["bash", *args], env=env, capture_output=True,
                                text=True, timeout=30, cwd=str(dest))
        out = r.stdout + r.stderr
        assert r.returncode != 0, f"{name} did not abort on a failed readiness probe:\n{out}"
        assert "ABORT" in out, f"{name} never printed the abort line:\n{out}"
        assert "s3cr3t-pw" not in out, f"{name} leaked the CORPUS_BASE password:\n{out}"
        assert "alice" not in out, f"{name} leaked the CORPUS_BASE username:\n{out}"
        assert "q-tok" not in out, f"{name} leaked the CORPUS_BASE query token:\n{out}"


def test_launchers_refuse_before_creating_the_logs_directory_executed():
    """r2 review, P3, run_corpus_sequential.sh:18 / run_corpus_parallel.sh:37
    (pre-fix lines): both launchers created logs/ BEFORE the unset-CORPUS_BASE
    guard, contradicting "refuse before doing anything else." Reordered."""
    import tempfile
    for name in ("run_corpus_sequential.sh", "run_corpus_parallel.sh"):
        with tempfile.TemporaryDirectory() as tmp:
            dest, stub_bin = _launcher_dir(tmp)
            r = _run_launcher(name, dest, stub_bin, corpus_base=None)
            logs_exists = (dest / "logs").exists()
        assert r.returncode != 0, f"{name} exited 0 with CORPUS_BASE unset"
        assert not logs_exists, (
            f"{name} created logs/ before refusing on an unset CORPUS_BASE")


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
