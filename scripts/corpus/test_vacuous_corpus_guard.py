"""A corpus with no expectation-bearing rows must make the run and the merge FAIL."""
import os
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).parent
TESTDATA = HERE / "testdata_corpus"
sys.path.insert(0, str(HERE))
import expectations  # noqa: E402


def _row(i, **kw):
    return dict({"id": i, "text": "synthetic"}, **kw)


def _raises(rows):
    try:
        expectations.require_expectation_bearing(rows)
    except expectations.VacuousCorpus:
        return True
    return False


def test_zero_expect_rows_raise():
    assert _raises([_row("a"), _row("b", expect=None)])


def test_empty_corpus_raises():
    assert _raises([])


def test_only_invalid_rows_raise():
    assert _raises([_row("a", expect="bogus")])


def test_one_valid_row_among_unscored_passes():
    assert expectations.require_expectation_bearing(
        [_row("a"), _row("b", expect="serve")]) == 1


def test_example_corpus_passes():
    import corpus_example
    assert expectations.require_expectation_bearing(corpus_example.CORPUS) == 3


def _run(script, args, corpus_dir, extra_env=None):
    env = dict(os.environ)
    env["PYTHONPATH"] = f"{corpus_dir}:{HERE}"
    env["CORPUS_BASE"] = "http://127.0.0.1:9/api/investigations"
    env.update(extra_env or {})
    return subprocess.run([sys.executable, str(HERE / script), *args],
                          env=env, capture_output=True, text=True, timeout=60,
                          cwd=corpus_dir)


ENTRYPOINTS = [
    ("run_shard.py", ["0", "1", "1"]),
    ("harness.py", ["--check-only"]),
    ("merge_corpus.py", ["--shape", "sequential", "--in", "x", "--out", "y"]),
]


def test_entrypoints_refuse_vacuous_corpus():
    for script, args in ENTRYPOINTS:
        _refuses(script, args)


def _refuses(script, args):
    with tempfile.TemporaryDirectory() as d:
        (Path(d) / "corpus.py").write_text(
            "CORPUS=[{'id':'a','text':'t','family':'f','variant':'v','member_kind':None,"
            "'group_kind':None,'requested_kind':None,'anchor_kind':None,'note':''}]\n"
            "REQUESTED_KIND={'a':None}\nANCHOR_KIND={'a':None}\n")
        r = _run(script, args, d)
    assert r.returncode != 0, script
    assert "0 expectation-bearing rows" in (r.stderr + r.stdout)


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
