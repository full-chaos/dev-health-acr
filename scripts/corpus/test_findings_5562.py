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
               "attempt_order.py", "shard_plan.py", "run_shard.py", "artefact_schema.json")

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
# launchers must set CORPUS_BASE for every lane that goes through them, even though
# the harness itself no longer defaults it.

def test_the_shell_launchers_export_corpus_base_before_invoking_run_shard():
    for name in ("run_corpus_sequential.sh", "run_corpus_parallel.sh"):
        t = (HERE / name).read_text()
        assert "export CORPUS_BASE" in t, f"{name} no longer exports CORPUS_BASE"
        invoke = 'python3 "$HERE/run_shard.py"'
        assert invoke in t, f"{name} no longer invokes run_shard.py the expected way"
        assert t.index("export CORPUS_BASE") < t.index(invoke), (
            f"{name} invokes run_shard.py before exporting CORPUS_BASE")


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
