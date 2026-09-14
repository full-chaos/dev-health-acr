#!/usr/bin/env python3
"""Tests for CHAOS-5722's acr-side wiring: the `persisted_semantic_state`
adapter (`semantic_verdict_bridge.make_persisted_semantic_state_adapter`)
that reads acr's OWN trial-postgres row for a result id via `psql`
subprocess, and `verdict_for_row`'s forward-compatibility guard against an
ask-dev pin that predates this ticket.

Isolation: these tests import `semantic_verdict_bridge` IN-PROCESS (unlike
test_findings_5625.py's subprocess isolation for anything that resolves a
real/fake ask-dev pin) -- nothing here imports `expect_schema`/
`semantic_verdict` from a checkout on `sys.path`, so there is no module
identity to leak between tests. The `psql` subprocess itself is replaced by
a FAKE `run` callable (a `subprocess.run`-shaped double): this file never
shells out to a real `psql`, and never touches the k3s trial store -- see
docs/PR TEST-EVIDENCE for the read-only manual replay against the REAL
trial-postgres store that this adapter's actual query was validated
against.

Run via run_pins.sh, or standalone:
  python3 test_findings_5722.py
"""
import inspect
import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path
from types import SimpleNamespace

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
# merge_corpus.py's own `import corpus as _corpus_module` needs a `corpus.py`
# on sys.path -- run_pins.sh supplies testdata_corpus/ for exactly this, but
# this file's own docstring also promises standalone execution
# (`python3 test_findings_5722.py`), so the same directory is added here too
# rather than silently depending on the runner's PYTHONPATH.
sys.path.insert(0, str(HERE / "testdata_corpus"))

import semantic_verdict_bridge as sv_bridge  # noqa: E402
import merge_corpus as merge_corpus_module  # noqa: E402  — for the CHAOS-5722 INFRA guard, see its tests below


class Findings5722Error(Exception):
    """Raised by `_require` -- never a bare `assert` (see test_corpus.py)."""


def _require(cond, msg):
    if not cond:
        raise Findings5722Error(msg)


_require(__debug__, "refusing to run under python -O / PYTHONOPTIMIZE=1: "
         "assert-stripping optimizations would silently weaken this guard")

# Exactly the four vars scripts/trial/common.sh's `trial_wire_common_env`
# actually exports (common.sh:486-489). The database name is resolved
# separately -- see `test_trial_pg_database_*` below.
_ALL_PG_ENV = {
    "ACR_TEST_TRIAL_PG_HOST": "10.0.0.1",
    "ACR_TEST_TRIAL_PG_PORT": "30500",
    "ACR_TEST_TRIAL_PG_USER": "devhealth",
    "ACR_TEST_TRIAL_PG_PASSWORD": "s3cret",
}


class _FakeSemanticVerdictModule:
    """The one thing `make_persisted_semantic_state_adapter` needs off a
    real/fake ask-dev `semantic_verdict` module: the exception class it
    raises for an unreadable row. A real module's own `PersistedSemanticStateUnreadable`
    behaves identically -- this is not a re-implementation of any
    CHAOS-5620/5722 acceptance logic, only its one exception type."""

    class PersistedSemanticStateUnreadable(Exception):
        pass


def _fake_run(stdout="", returncode=0, stderr="", raises=None):
    """A `subprocess.run`-shaped double. `raises`, if given, is raised
    instead of returning (models a `psql` invocation that never completes:
    binary missing, connection refused, timeout)."""

    def run(*args, **kwargs):
        if raises is not None:
            raise raises
        return SimpleNamespace(stdout=stdout, stderr=stderr, returncode=returncode)

    return run


def test_env_present_requires_every_one_of_the_four_variables(monkeypatch=None):
    import os
    saved = {k: os.environ.pop(k, None) for k in _ALL_PG_ENV}
    try:
        _require(sv_bridge.trial_postgres_env_present() is False, "no vars set")
        for k, v in _ALL_PG_ENV.items():
            os.environ[k] = v
        _require(sv_bridge.trial_postgres_env_present() is True, "all vars set")
        for missing in _ALL_PG_ENV:
            os.environ.pop(missing)
            _require(sv_bridge.trial_postgres_env_present() is False,
                     f"must be False with {missing!r} unset")
            os.environ[missing] = _ALL_PG_ENV[missing]
    finally:
        for k in _ALL_PG_ENV:
            os.environ.pop(k, None)
        for k, v in saved.items():
            if v is not None:
                os.environ[k] = v


def test_adapter_is_none_when_env_recipe_is_not_fully_present():
    import os
    saved = {k: os.environ.pop(k, None) for k in _ALL_PG_ENV}
    try:
        os.environ.update({k: v for i, (k, v) in enumerate(_ALL_PG_ENV.items()) if i > 0})  # missing HOST
        adapter = sv_bridge.make_persisted_semantic_state_adapter(_FakeSemanticVerdictModule)
        _require(adapter is None, "a partial env recipe must never build a partially-configured adapter")
    finally:
        for k in _ALL_PG_ENV:
            os.environ.pop(k, None)
        for k, v in saved.items():
            if v is not None:
                os.environ[k] = v


def _with_env(fn, database=None):
    """Run `fn()` with the full trial-postgres CONNECTION recipe set,
    restoring whatever was there before. `ACR_TRIAL_PG_DATABASE` is always
    explicitly cleared first (never left over from the ambient environment)
    and set only when `database` is given -- so a test that omits it
    deterministically exercises the standing default, never an accidental
    leftover value."""
    import os
    keys = (*_ALL_PG_ENV, "ACR_TRIAL_PG_DATABASE")
    saved = {k: os.environ.pop(k, None) for k in keys}
    os.environ.update(_ALL_PG_ENV)
    if database is not None:
        os.environ["ACR_TRIAL_PG_DATABASE"] = database
    try:
        return fn()
    finally:
        for k in keys:
            os.environ.pop(k, None)
        for k, v in saved.items():
            if v is not None:
                os.environ[k] = v


def test_trial_pg_database_defaults_to_the_standing_k3s_database():
    def go():
        _require(sv_bridge._trial_pg_database() == "acr_kiac_askdev", sv_bridge._trial_pg_database())
    _with_env(go)


def test_trial_pg_database_honors_an_explicit_override():
    def go():
        _require(sv_bridge._trial_pg_database() == "some_other_db", sv_bridge._trial_pg_database())
    _with_env(go, database="some_other_db")


def test_adapter_connects_to_the_resolved_database_not_a_hardcoded_one():
    captured = {}

    def spying_run(cmd, **kwargs):
        captured["cmd"] = cmd
        return SimpleNamespace(stdout="", stderr="", returncode=0)

    def go():
        adapter = sv_bridge.make_persisted_semantic_state_adapter(_FakeSemanticVerdictModule, run=spying_run)
        adapter("result_x")
        cmd = captured["cmd"]
        _require(cmd[cmd.index("-d") + 1] == "some_other_db", cmd)
    _with_env(go, database="some_other_db")


def test_adapter_returns_the_decoded_row():
    def go():
        state = {"format_version": "semantic-state.v1", "family": "explicit_comparison",
                  "validation": {"gate_outcome": "passed"}}
        adapter = sv_bridge.make_persisted_semantic_state_adapter(
            _FakeSemanticVerdictModule, run=_fake_run(stdout=json.dumps(state) + "\n"))
        _require(adapter is not None, "adapter must build when env is fully present")
        got = adapter("result_abc123")
        _require(got == state, got)
    _with_env(go)


def test_adapter_returns_none_on_empty_output_null_or_no_row():
    def go():
        adapter = sv_bridge.make_persisted_semantic_state_adapter(_FakeSemanticVerdictModule, run=_fake_run(stdout=""))
        _require(adapter("result_x") is None, "empty psql output (NULL column or no row) must be absent, not unreadable")
    _with_env(go)


def test_adapter_raises_unreadable_on_non_json_output():
    def go():
        adapter = sv_bridge.make_persisted_semantic_state_adapter(
            _FakeSemanticVerdictModule, run=_fake_run(stdout="not-json{"))
        try:
            adapter("result_x")
            _require(False, "must raise, not return, on undecodable output")
        except _FakeSemanticVerdictModule.PersistedSemanticStateUnreadable:
            pass
    _with_env(go)


def test_adapter_raises_unreadable_on_non_object_json():
    # A real SQL NULL prints as an EMPTY string under `psql -At` (covered by
    # test_adapter_returns_none_on_empty_output_null_or_no_row) -- a
    # NON-EMPTY body that decodes to something other than a JSON object
    # (an array, a bare scalar, or the literal text "null") is a different,
    # unreadable fact: something is actually stored, and it is not the
    # object shape this scorer requires.
    def go():
        for payload in ("[1, 2, 3]", "42", '"just a string"', "null"):
            adapter = sv_bridge.make_persisted_semantic_state_adapter(
                _FakeSemanticVerdictModule, run=_fake_run(stdout=payload))
            try:
                adapter("result_x")
                _require(False, f"{payload!r} must raise, not return")
            except _FakeSemanticVerdictModule.PersistedSemanticStateUnreadable:
                pass
    _with_env(go)


def test_adapter_raises_unreadable_on_oversized_row():
    def go():
        huge = json.dumps({"format_version": "semantic-state.v1", "family": "x" * 100000,
                            "validation": {"gate_outcome": "passed"}})
        _require(len(huge.encode("utf-8")) > sv_bridge._MAX_PERSISTED_STATE_BYTES, "fixture must exceed the bound")
        adapter = sv_bridge.make_persisted_semantic_state_adapter(_FakeSemanticVerdictModule, run=_fake_run(stdout=huge))
        try:
            adapter("result_x")
            _require(False, "an oversized row must never reach the scorer")
        except _FakeSemanticVerdictModule.PersistedSemanticStateUnreadable:
            pass
    _with_env(go)


def test_adapter_raises_infra_error_on_nonzero_exit():
    # CHAOS-5722: a non-zero psql exit is an INFRA-class fault -- it must
    # raise `sv_bridge.PersistedSemanticStateInfraError`, NEVER the fake
    # module's `PersistedSemanticStateUnreadable` (that class is reserved
    # for a DATA-class fault: oversized/non-JSON/non-object).
    def go():
        adapter = sv_bridge.make_persisted_semantic_state_adapter(
            _FakeSemanticVerdictModule, run=_fake_run(returncode=1, stderr="connection refused"))
        try:
            adapter("result_x")
            _require(False, "a failed psql invocation must raise, not silently return None")
        except sv_bridge.PersistedSemanticStateInfraError:
            pass
        except _FakeSemanticVerdictModule.PersistedSemanticStateUnreadable:
            _require(False, "a non-zero psql exit is INFRA, not DATA-class Unreadable")
    _with_env(go)


def test_adapter_raises_infra_error_when_the_subprocess_call_itself_fails():
    def go():
        for exc in (OSError("psql: command not found"), subprocess.TimeoutExpired(cmd="psql", timeout=15)):
            adapter = sv_bridge.make_persisted_semantic_state_adapter(
                _FakeSemanticVerdictModule, run=_fake_run(raises=exc))
            try:
                adapter("result_x")
                _require(False, f"{type(exc).__name__} from the subprocess call itself must raise InfraError")
            except sv_bridge.PersistedSemanticStateInfraError:
                pass
            except _FakeSemanticVerdictModule.PersistedSemanticStateUnreadable:
                _require(False, f"{type(exc).__name__} is INFRA, not DATA-class Unreadable")
    _with_env(go)


def test_infra_error_message_never_carries_the_password():
    # Defense-in-depth scrub: even a stderr body that happens to echo the
    # literal password string must not reach the raised message.
    def go():
        adapter = sv_bridge.make_persisted_semantic_state_adapter(
            _FakeSemanticVerdictModule,
            run=_fake_run(returncode=1, stderr=f"connection failed, tried password {_ALL_PG_ENV['ACR_TEST_TRIAL_PG_PASSWORD']}"))
        try:
            adapter("result_x")
            _require(False, "must raise")
        except sv_bridge.PersistedSemanticStateInfraError as exc:
            _require(_ALL_PG_ENV["ACR_TEST_TRIAL_PG_PASSWORD"] not in str(exc),
                     f"password leaked into infra error: {exc}")
    _with_env(go)


def test_adapter_never_queries_for_a_malformed_result_id():
    calls = []

    def counting_run(*args, **kwargs):
        calls.append(args)
        return SimpleNamespace(stdout="", stderr="", returncode=0)

    def go():
        adapter = sv_bridge.make_persisted_semantic_state_adapter(_FakeSemanticVerdictModule, run=counting_run)
        for bad in (None, "", 12345):
            _require(adapter(bad) is None, bad)
        _require(calls == [], f"a malformed result_id must never reach psql: {calls}")
    _with_env(go)


def test_adapter_passes_on_error_stop_to_psql():
    # `psql -f` exits 0 on a genuine SQL error unless `-v ON_ERROR_STOP=1`
    # is set -- without it, an error prints to stderr while stdout stays
    # empty, and empty stdout is this adapter's own signal for NULL/absent.
    # An error-with-empty-stdout must never read as NULL/absent, so this
    # pin locks the argv shape in place.
    captured = {}

    def spying_run(cmd, **kwargs):
        captured["cmd"] = cmd
        return SimpleNamespace(stdout="", stderr="", returncode=0)

    def go():
        adapter = sv_bridge.make_persisted_semantic_state_adapter(_FakeSemanticVerdictModule, run=spying_run)
        adapter("result_abc123")
        cmd = captured["cmd"]
        pairs = list(zip(cmd, cmd[1:]))
        _require(("-v", "ON_ERROR_STOP=1") in pairs, f"missing -v ON_ERROR_STOP=1: {cmd}")
    _with_env(go)


def test_result_id_is_bound_via_psql_variable_not_string_concatenation():
    captured = {}

    def spying_run(cmd, **kwargs):
        captured["cmd"] = cmd
        # Must read the -f file WHILE it exists -- the adapter unlinks its
        # scratch file in a `finally` before returning to the caller (see
        # test_adapter_cleans_up_its_scratch_sql_file), so reading it here,
        # inside the fake `run`, is the only point it is still on disk.
        captured["sql_text"] = Path(cmd[cmd.index("-f") + 1]).read_text()
        return SimpleNamespace(stdout="", stderr="", returncode=0)

    def go():
        adapter = sv_bridge.make_persisted_semantic_state_adapter(_FakeSemanticVerdictModule, run=spying_run)
        adapter("result_'; drop table x; --")
        cmd = captured["cmd"]
        # The result id must appear ONLY inside a `-v name=value` pair, never
        # substituted directly into the SQL text -- and the SQL text itself
        # must be read from a `-f` script file, never a `-c` command-line
        # string (see test_adapter_uses_dash_f_never_dash_c_for_the_query
        # below for why `-c` cannot bind `:'result_id'` at all).
        _require("drop table" not in captured["sql_text"].lower(),
                 f"result_id leaked into the SQL text: {captured['sql_text']}")
        _require(any(a.startswith("result_id=") for a in cmd), cmd)
    _with_env(go)


def test_adapter_uses_dash_f_never_dash_c_for_the_query():
    # CHAOS-5722: `psql -c "...:'name'..."` never substitutes the `-v` bind
    # variable -- only a script read via `-f` (or stdin/interactive input)
    # does. The query text must reach psql through `-f`, a readable file
    # containing `:'result_id'`, never through `-c`.
    captured = {}

    def spying_run(cmd, **kwargs):
        captured["cmd"] = cmd
        captured["sql_text"] = Path(cmd[cmd.index("-f") + 1]).read_text()
        return SimpleNamespace(stdout="", stderr="", returncode=0)

    def go():
        adapter = sv_bridge.make_persisted_semantic_state_adapter(_FakeSemanticVerdictModule, run=spying_run)
        adapter("result_abc123")
        cmd = captured["cmd"]
        _require("-c" not in cmd, f"the query must never be passed via -c (it cannot bind :'result_id'): {cmd}")
        _require("-f" in cmd, f"the query must be read via -f: {cmd}")
        _require(":'result_id'" in captured["sql_text"],
                 f"the -f script must bind result_id via :'result_id': {captured['sql_text']}")
    _with_env(go)


def test_adapter_cleans_up_its_scratch_sql_file():
    captured = {}

    def spying_run(cmd, **kwargs):
        captured["cmd"] = cmd
        captured["existed_during_call"] = Path(cmd[cmd.index("-f") + 1]).exists()
        return SimpleNamespace(stdout="", stderr="", returncode=0)

    def go():
        adapter = sv_bridge.make_persisted_semantic_state_adapter(_FakeSemanticVerdictModule, run=spying_run)
        adapter("result_abc123")
        _require(captured["existed_during_call"], "the scratch SQL file must exist while psql runs")
        sql_path = captured["cmd"][captured["cmd"].index("-f") + 1]
        _require(not Path(sql_path).exists(), "the scratch SQL file must be removed after the call")
    _with_env(go)


def test_verdict_for_row_omits_the_adapter_for_a_pre_5722_build_verdict():
    # The exact shape a pinned ask-dev checkout from before this ticket has:
    # no `persisted_semantic_state` parameter, an `**identity` catch-all
    # that would otherwise forward it straight into `legacy_score` (which
    # does not accept it either) and crash every row.
    class OldSemanticVerdict:
        @staticmethod
        def audit_window_exchange(attempts):
            return {"family_relation": "unknown", "window_binding": "unknown",
                     "family_confirmation": "unavailable", "reason": "n/a"}

        @staticmethod
        def build_verdict(row, bucket, status, final, audit, legacy_score, corpus_version,
                           legacy_scorer_version, **identity):
            verdict, reason = legacy_score(row, bucket, status, **identity)
            return {"verdict": verdict, "reason": reason, "branch_results": [],
                     "family_relation": "unknown", "window_binding": "unknown",
                     "family_confirmation": "unavailable"}

    _require("persisted_semantic_state" not in
             inspect.signature(OldSemanticVerdict.build_verdict).parameters,
             "fixture must actually lack the parameter")
    row = {"id": "x", "expect": "serve"}
    result = sv_bridge.verdict_for_row(
        row, str(HERE / "testdata_corpus"), "does-not-exist", 1, "unserved", "complete",
        OldSemanticVerdict, {"legacy_scorer_version": "fake-v1"}, "corpus-v0",
        persisted_semantic_state=lambda result_id: {"should": "never be reached"},
    )
    _require(result["verdict"] in {"agree", "disagree", "unscored"}, result)


def test_verdict_for_row_forwards_the_adapter_to_a_post_5722_build_verdict():
    received = {}

    class NewSemanticVerdict:
        @staticmethod
        def audit_window_exchange(attempts):
            return {"family_relation": "unknown", "window_binding": "unknown",
                     "family_confirmation": "unavailable", "reason": "n/a"}

        @staticmethod
        def build_verdict(row, bucket, status, final, audit, legacy_score, corpus_version,
                           legacy_scorer_version, persisted_semantic_state=None, **identity):
            received["persisted_semantic_state"] = persisted_semantic_state
            verdict, reason = legacy_score(row, bucket, status, **identity)
            return {"verdict": verdict, "reason": reason, "branch_results": [],
                     "family_relation": "unknown", "window_binding": "unknown",
                     "family_confirmation": "unavailable"}

    marker = lambda result_id: None  # noqa: E731
    row = {"id": "x", "expect": "serve"}
    sv_bridge.verdict_for_row(
        row, str(HERE / "testdata_corpus"), "does-not-exist", 1, "unserved", "complete",
        NewSemanticVerdict, {"legacy_scorer_version": "fake-v1"}, "corpus-v0",
        persisted_semantic_state=marker,
    )
    _require(received["persisted_semantic_state"] is marker,
             "a post-CHAOS-5722 build_verdict must receive the adapter unchanged")


def test_infra_guard_passes_through_a_successful_call():
    def fake_adapter(result_id):
        return {"ok": result_id}
    guard = merge_corpus_module._PersistedStateInfraGuard(fake_adapter)
    _require(guard("x") == {"ok": "x"}, "a successful call must pass through unchanged")
    _require(not guard.disabled_after_first_call, "a successful call must not disable the guard")


def test_infra_guard_catches_once_then_falls_back_to_none_for_the_rest_of_the_run():
    # CHAOS-5722: every psql call failing the SAME infrastructure way must
    # be caught ONCE (first occurrence), never re-raised per row, and the
    # guard must never call the underlying adapter again afterwards -- even
    # for a result_id that would have succeeded.
    calls = []

    def flaky_adapter(result_id):
        calls.append(result_id)
        raise sv_bridge.PersistedSemanticStateInfraError(f"{result_id}: psql exited 1: syntax error")

    guard = merge_corpus_module._PersistedStateInfraGuard(flaky_adapter)
    _require(guard("row1") is None, "an INFRA failure must degrade to None, never raise past the guard")
    _require(guard.disabled_after_first_call, "the guard must be disabled after the first INFRA failure")
    _require(guard.error and "row1" in guard.error, f"the guard must record the first error's message: {guard.error!r}")

    got = guard("row2")
    _require(got is None, "every call after the first INFRA failure must return None")
    _require(calls == ["row1"], f"the underlying adapter must never be called again once disabled: {calls}")


def test_infra_guard_does_not_catch_data_class_unreadable():
    # A DATA-class failure (oversized/non-JSON/non-object row) is scoped to
    # the ONE row and must still reach the caller -- the guard exists only
    # for the INFRA class, never as a blanket try/except around the adapter.
    class FakeUnreadable(Exception):
        pass

    def data_bad_adapter(result_id):
        raise FakeUnreadable(f"{result_id}: not an object")

    guard = merge_corpus_module._PersistedStateInfraGuard(data_bad_adapter)
    try:
        guard("row1")
        _require(False, "a DATA-class failure must propagate, not be swallowed by the guard")
    except FakeUnreadable:
        pass
    _require(not guard.disabled_after_first_call, "a DATA-class failure must never disable the guard")


def test_real_psql_round_trip_against_the_trial_store():
    """CHAOS-5722: the ONE test in this file that shells out to a REAL
    `psql` against the REAL standing k3s `acr-trial-data` trial-postgres
    instance (see the module docstring's Isolation section for why every
    other test here uses a fake `run`) -- proving `-f` actually substitutes
    `:'result_id'` against this exact binary and table. SKIPPED with an
    explicit SKIP line unless the full ACR_TEST_TRIAL_PG_* connection
    recipe is present -- the trial store is the only admissible data venue
    (cf-lane-rules.md "Suites"), never a fabricated local stand-in, so this
    pin does not run without it.
    """
    if not sv_bridge.trial_postgres_env_present():
        print("SKIP: test_real_psql_round_trip_against_the_trial_store -- "
              "ACR_TEST_TRIAL_PG_* not fully present in the environment")
        return

    class RealSemanticVerdictModule:
        class PersistedSemanticStateUnreadable(Exception):
            pass

    adapter = sv_bridge.make_persisted_semantic_state_adapter(RealSemanticVerdictModule)
    _require(adapter is not None, "adapter must build against the real env recipe")

    env = {**os.environ, "PGPASSWORD": os.environ["ACR_TEST_TRIAL_PG_PASSWORD"]}

    def q(sql):
        proc = subprocess.run(
            ["psql", "-h", os.environ["ACR_TEST_TRIAL_PG_HOST"], "-p", os.environ["ACR_TEST_TRIAL_PG_PORT"],
             "-U", os.environ["ACR_TEST_TRIAL_PG_USER"], "-d", sv_bridge._trial_pg_database(),
             "-At", "-c", sql],
            capture_output=True, text=True, timeout=15, env=env)
        _require(proc.returncode == 0, f"setup query failed rc={proc.returncode}: {proc.stderr.strip()}")
        return proc.stdout.strip()

    null_id = q("select result_id from acr.context_fabric_investigation_results "
                "where semantic_state is null limit 1;")
    nonnull_id = q("select result_id from acr.context_fabric_investigation_results "
                   "where semantic_state is not null limit 1;")
    _require(null_id, "trial store must carry at least one NULL semantic_state row for this pin")
    _require(nonnull_id, "trial store must carry at least one non-NULL semantic_state row for this pin")

    got_null = adapter(null_id)
    _require(got_null is None, f"a NULL semantic_state row must round-trip to None, got {got_null!r}")

    got_state = adapter(nonnull_id)
    _require(isinstance(got_state, dict),
             f"a non-NULL semantic_state row must round-trip to a dict, got {type(got_state).__name__}")
    _require("format_version" in got_state, f"the decoded state must carry format_version: {got_state}")

    # A genuine SQL error against the real binary and table, run twice:
    # without `-v ON_ERROR_STOP=1` psql exits 0 with the error only on
    # stderr; with it, the identical query exits non-zero, which the
    # adapter's own `proc.returncode != 0` branch turns into
    # PersistedSemanticStateInfraError (see test_adapter_raises_infra_error_
    # on_nonzero_exit for that branch, proven with a fake run).
    bad_sql_fd, bad_sql_path = tempfile.mkstemp(prefix="acr-5722-test-badquery-", suffix=".sql")
    try:
        with os.fdopen(bad_sql_fd, "w") as fh:
            fh.write("select this_column_does_not_exist from "
                     "acr.context_fabric_investigation_results where result_id = :'result_id';\n")
        proc_bug = subprocess.run(
            ["psql", "-h", os.environ["ACR_TEST_TRIAL_PG_HOST"], "-p", os.environ["ACR_TEST_TRIAL_PG_PORT"],
             "-U", os.environ["ACR_TEST_TRIAL_PG_USER"], "-d", sv_bridge._trial_pg_database(),
             "-v", f"result_id={null_id}", "-At", "-f", bad_sql_path],
            capture_output=True, text=True, timeout=15, env=env)
        _require(proc_bug.returncode == 0,
                  f"pre-fix repro: psql -f WITHOUT ON_ERROR_STOP must exit 0 even on a real SQL error "
                  f"(got rc={proc_bug.returncode}) -- this is the bug the fix closes")
        _require("ERROR" in proc_bug.stderr, f"expected a real SQL error on stderr: {proc_bug.stderr!r}")
        _require(proc_bug.stdout.strip() == "", f"expected empty stdout on the errored query: {proc_bug.stdout!r}")

        proc_fixed = subprocess.run(
            ["psql", "-h", os.environ["ACR_TEST_TRIAL_PG_HOST"], "-p", os.environ["ACR_TEST_TRIAL_PG_PORT"],
             "-U", os.environ["ACR_TEST_TRIAL_PG_USER"], "-d", sv_bridge._trial_pg_database(),
             "-v", "ON_ERROR_STOP=1", "-v", f"result_id={null_id}", "-At", "-f", bad_sql_path],
            capture_output=True, text=True, timeout=15, env=env)
        _require(proc_fixed.returncode != 0,
                  f"post-fix: psql -f WITH ON_ERROR_STOP=1 must exit non-zero on the same real SQL error "
                  f"(got rc={proc_fixed.returncode})")
    finally:
        os.unlink(bad_sql_path)

    print(f"PASS (real psql, ON_ERROR_STOP): pre-fix rc={proc_bug.returncode} (bug reproduced) -> "
          f"post-fix rc={proc_fixed.returncode} (caught)")

    print(f"PASS (real psql, {os.environ['ACR_TEST_TRIAL_PG_HOST']}:{os.environ['ACR_TEST_TRIAL_PG_PORT']}/"
          f"{sv_bridge._trial_pg_database()}): NULL row {null_id} -> None; "
          f"non-NULL row {nonnull_id} -> dict, format_version={got_state.get('format_version')!r}")


def main():
    tests = [v for k, v in sorted(globals().items()) if k.startswith("test_") and callable(v)]
    for test in tests:
        test()
        print(f"PASS: {test.__name__}")
    print(f"PASS: {len(tests)} CHAOS-5722 persisted-state-adapter controls")


if __name__ == "__main__":
    main()
