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
import subprocess
import sys
from pathlib import Path
from types import SimpleNamespace

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import semantic_verdict_bridge as sv_bridge  # noqa: E402


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


def test_adapter_raises_unreadable_on_nonzero_exit():
    def go():
        adapter = sv_bridge.make_persisted_semantic_state_adapter(
            _FakeSemanticVerdictModule, run=_fake_run(returncode=1, stderr="connection refused"))
        try:
            adapter("result_x")
            _require(False, "a failed psql invocation must raise, not silently return None")
        except _FakeSemanticVerdictModule.PersistedSemanticStateUnreadable:
            pass
    _with_env(go)


def test_adapter_raises_unreadable_when_the_subprocess_call_itself_fails():
    def go():
        for exc in (OSError("psql: command not found"), subprocess.TimeoutExpired(cmd="psql", timeout=15)):
            adapter = sv_bridge.make_persisted_semantic_state_adapter(
                _FakeSemanticVerdictModule, run=_fake_run(raises=exc))
            try:
                adapter("result_x")
                _require(False, f"{type(exc).__name__} from the subprocess call itself must raise Unreadable")
            except _FakeSemanticVerdictModule.PersistedSemanticStateUnreadable:
                pass
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


def test_result_id_is_bound_via_psql_variable_not_string_concatenation():
    captured = {}

    def spying_run(cmd, **kwargs):
        captured["cmd"] = cmd
        return SimpleNamespace(stdout="", stderr="", returncode=0)

    def go():
        adapter = sv_bridge.make_persisted_semantic_state_adapter(_FakeSemanticVerdictModule, run=spying_run)
        adapter("result_'; drop table x; --")
        cmd = captured["cmd"]
        # The result id must appear ONLY inside a `-v name=value` pair, never
        # substituted directly into the `-c` SQL text.
        sql_arg = cmd[cmd.index("-c") + 1]
        _require("drop table" not in sql_arg.lower(), f"result_id leaked into the SQL text: {sql_arg}")
        _require(any(a.startswith("result_id=") for a in cmd), cmd)
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


def main():
    tests = [v for k, v in sorted(globals().items()) if k.startswith("test_") and callable(v)]
    for test in tests:
        test()
        print(f"PASS: {test.__name__}")
    print(f"PASS: {len(tests)} CHAOS-5722 persisted-state-adapter controls")


if __name__ == "__main__":
    main()
