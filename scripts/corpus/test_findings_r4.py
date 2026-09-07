"""RED-FIRST pins for round 4, and the GOLDEN TABLE.

The previous round's domain pin could not detect the defect it was written for, because it
recomputed the implementation's own fallback to decide what the table "should" say. A pin
that derives its expectation from the code under test can only ever agree with that code.

GOLDEN below is written by hand from the specification, independently of `expectations.py`.
It is the authority; the code is compared to it. Nothing here calls `terminal_key`,
`VERDICTS`, or any other helper of the module under test to decide what to expect.
"""
import json
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).parent
sys.path.insert(0, str(HERE))

import expectations as E          # noqa: E402
import subject_identity as SI     # noqa: E402
import engine_failures            # noqa: E402
from attempt_order import order_attempts  # noqa: E402
from validators import validate_attempt, validate_corpus_row  # noqa: E402

# --------------------------------------------------------------------------- GOLDEN
# The golden table is a DATA FILE, authored by hand from the design and the operator's
# rulings, independent of the implementation. It used to be a 27-cell literal in this
# file, which covered only a slice of the domain: a regression in a valid terminal with an
# omitted bucket stayed green. The file carries the FULL cross product with the coherence
# relation, so the complement assertion below is meaningful.
GOLDEN_FILE = HERE / "golden_verdicts.json"
_G = json.loads(GOLDEN_FILE.read_text())
GOLDEN = {(c["expect"], c["basis"], c["terminal"], c["bucket"]): (c["verdict"], c["weak_kind"])
          for c in _G["cells"]}
UNRECOGNISED = [t for t in _G["_domain"]["terminal"]
                if not isinstance(t, str) or t == ""] + ["future_status"]


def test_every_golden_cell_matches_the_implementation():
    """All 672 authored cells, not a slice."""
    bad = []
    for (cls, basis, term, bucket), (want, _kind) in GOLDEN.items():
        e = {"expectation": cls, "expectation_basis": ("named_basis" if basis == "named" else None)}
        got = E.score(e, bucket, terminal_status=term)[0]
        if got != want:
            bad.append((cls, basis, term, bucket, want, got))
    assert not bad, f"{len(bad)} of {len(GOLDEN)} cells disagree: {bad[:6]}"


def test_the_golden_file_is_the_full_cross_product():
    d = _G["_domain"]
    assert len(GOLDEN) == len(d["expect"]) * len(d["basis"]) * len(d["terminal"]) * len(d["bucket"])
    assert _G["_coherence"], "the coherence relation is missing"
    assert set(_G["_weak_kinds"]) == {"weak_basis_unstated", "weak_never_terminated",
                                      "weak_hollow_serve"}


def test_the_scorers_coherence_table_matches_the_authored_one():
    """The file is the authority; the scorer carries its own copy so it does not depend on
    test data. This pin is what stops the two drifting apart."""
    authored = {k: set(v) for k, v in _G["_coherence"].items()}
    assert E.COHERENCE == authored, (
        f"scorer-only: {set(E.COHERENCE) - set(authored)}; "
        f"file-only: {set(authored) - set(E.COHERENCE)}")


def test_nothing_outside_the_golden_file_ever_scores():
    """The complement, over the WHOLE domain -- not just unrecognised terminals. A valid
    terminal paired with an omitted bucket must be unscored too."""
    leaked = []
    for (cls, basis, term, bucket), (want, _k) in GOLDEN.items():
        if want != "unscored":
            continue
        e = {"expectation": cls, "expectation_basis": ("named_basis" if basis == "named" else None)}
        got = E.score(e, bucket, terminal_status=term)[0]
        if got != "unscored":
            leaked.append((cls, basis, term, bucket, got))
    assert not leaked, f"{len(leaked)} cells the file marks unscored did score: {leaked[:8]}"


def test_unknown_terminal_variants_are_not_scored_by_a_prefix():
    """CHAOS-5430 moved `clarification_required` OUT of this list, deliberately.

    The bare spelling is what the engine actually emits -- 54/50/89/100 occurrences across
    the four measured arms -- and it is now authored for the scorer as well as the
    bucketer, so it is SCORED. It was listed here as an unknown variant only because the
    scorer had never been taught a status the bucketer already knew, and treating a status
    the engine emits in every run as "unknown" is the bug, not the guard.

    What this pin defends is unchanged: a variant nobody authored must never reach a
    verdict through a prefix match.
    """
    for term in ("clarification_required(future)", "clarification_required()",
                 "http_599:unknown", "http_", "clarification_requiredX"):
        for cls in ("serve", "refuse", "decline", "clarify"):
            v = E.score({"expectation": cls, "expectation_basis": None},
                        "clarification_needed", terminal_status=term)[0]
            assert v == "unscored", f"{cls} + {term!r} -> {v}"
    # and the authored bare form IS scored, in both directions
    assert E.score({"expectation": "clarify", "expectation_basis": None},
                   "clarification_needed",
                   terminal_status="clarification_required")[0] == "agree"
    assert E.score({"expectation": "serve", "expectation_basis": None},
                   "clarification_needed",
                   terminal_status="clarification_required")[0] == "disagree"


def test_the_bucket_can_never_rescue_an_absent_terminal_status():
    """r4 repro: terminals {} -> agree; 'future_status' -> agree."""
    for bucket in ("served_with_data", "served_degraded", "unserved",
                   "clarification_needed", "error"):
        for term in (None, "future_status"):
            v = E.score({"expectation": "serve", "expectation_basis": None}, bucket,
                        terminal_status=term)[0]
            assert v == "unscored", f"bucket={bucket} terminal={term!r} -> {v}"


def test_table_level_absent_terminals_do_not_score():
    rows = {"q": {"id": "q", "expect": "serve", "basis": None, "anchor": None,
                  "nonexistent": False, "family": "f"}}
    for terminals in ({}, {"q": "future_status"}, {"q": ["list"]}):
        out = E.table(rows, {"q": "served_with_data"}, {"q": False}, terminals_by_id=terminals)
        assert out[0]["verdict"] == "unscored", f"{terminals} -> {out[0]['verdict']}"


# --------------------------------------------------------------------------- validation
def test_malformed_declarations_are_invalid_not_trusted():
    cases = {
        "expect-list": {"id": "x", "expect": ["serve"]},
        "expect-unknown": {"id": "x", "expect": "sorve"},
        "anchor-list": {"id": "x", "anchor": [1]},
        "anchor-partial": {"id": "x", "anchor": {"kind": "team"}},
        "anchor-empty-label": {"id": "x", "anchor": {"kind": "team", "label": ""}},
        "nonexistent-str": {"id": "x", "nonexistent": "false"},
        "nonexistent-int": {"id": "x", "nonexistent": 1},
        "basis-int": {"id": "x", "basis": 7},
        "not-a-row": "nope",
    }
    for label, row in cases.items():
        e = E.expectation_for(row)
        assert e["expectation"] == E.INVALID, f"{label} -> {e['expectation']}"
        assert E.score(e, "served_with_data", terminal_status="partial")[0] == "unscored", label


def test_a_valid_declaration_still_scores():
    row = {"id": "x", "expect": "serve", "basis": None,
           "anchor": {"kind": "team", "label": "T"}, "nonexistent": False}
    e = E.expectation_for(row)
    assert e["expectation"] == E.SERVE
    assert E.score(e, "served_with_data", terminal_status="partial")[0] == "agree"


def test_attempt_envelopes_are_validated_at_ingestion():
    bad = {"result-list": {"response": {"result": [1]}},
           "failure-str": {"response": {"failure": "boom"}},
           "response-list": {"response": [1]},
           "status-str": {"status": "422"},
           "not-a-dict": "nope"}
    for label, a in bad.items():
        assert SI.classify_attempt(a)[0] == SI.UNPARSEABLE, label
        ok, _ = validate_attempt(a)
        assert not ok, label


def test_a_truthy_non_dict_result_cannot_crash_identity():
    with tempfile.TemporaryDirectory() as tmp:
        d = Path(tmp) / "replicate"; d.mkdir(parents=True)
        (d / "q-rep1-t1-a1.json").write_text(json.dumps({"response": {"result": [1]}}))
        rec = SI.inspect(tmp, "q", E.expectation_for(
            {"id": "q", "expect": "serve", "basis": None, "anchor": None, "nonexistent": False}))
        assert rec["state"] == "unreadable_artefact", rec["state"]


def test_engine_failures_reports_an_invalid_envelope_instead_of_crashing():
    with tempfile.TemporaryDirectory() as tmp:
        d = Path(tmp) / "replicate"; d.mkdir(parents=True)
        (d / "q-rep1-t1-a1.json").write_text(json.dumps({"response": {"failure": "boom"}}))
        recs = engine_failures.scan(tmp)
        assert any(r["kind"] == "UNREADABLE_ARTEFACT" for r in recs), recs


def test_load_attempt_is_the_only_decoder_of_attempt_artefacts():
    """AST sweep: no module may decode an attempt artefact itself.

    Four readers each decoded and trusted the same files in their own way, which is how
    four separate crashes came from one malformed shape. Documents that are NOT attempt
    artefacts (shard summaries, provenance, the baseline verdict, live HTTP responses) are
    allowlisted by file and line with a reason, so the exclusion is explicit rather than
    a loose pattern.
    """
    import ast
    ALLOWED = {
        "validators.py": "the loader itself",
        "merge_corpus.py": "shard summaries, provenance and the baseline verdict",
        "reclassify_deadlines.py": "shard summaries",
        "harness.py": "live HTTP responses, validated by validate_attempt at the call site",
        # CHAOS-5430. The generator reads raw artefacts because that is its whole job: it
        # DERIVES the boundary from them, so it cannot be gated by the boundary it
        # produces. It is also off the measurement path entirely -- it emits a schema and
        # never a verdict -- so a malformed artefact here corrupts a type, not a score,
        # and the regeneration pin would catch that as a diff.
        "measure_schema.py": "derives the schema FROM the artefacts; cannot use the "
                             "boundary it generates, and produces no verdict",
        "schema_report.py": "reads the GENERATED SCHEMA, never an attempt artefact; a "
                            "reporting tool off the measurement path entirely",
    }
    # LINE-scoped, not file-scoped. Allowlisting subject_identity.py wholesale would hide
    # the next raw-attempt decode added to it, which is the exact defect this pin exists to
    # catch -- the guard must not be widened to fit the one call that is legitimate.
    ALLOWED_CALLS = {
        ("subject_identity.py", "rep_from_summaries"):
            "shard summaries only, to read the run's replicate tag; attempt artefacts "
            "still go through load_attempt",
    }
    offenders = []
    for f in sorted(HERE.glob("*.py")):
        if f.name.startswith("test_") or f.name in ALLOWED:
            continue
        tree = ast.parse(f.read_text())
        enclosing = {}
        for fn in ast.walk(tree):
            if isinstance(fn, (ast.FunctionDef, ast.AsyncFunctionDef)):
                for sub in ast.walk(fn):
                    enclosing[id(sub)] = fn.name
        for node in ast.walk(tree):
            if isinstance(node, ast.Call) and isinstance(node.func, ast.Attribute) \
                    and node.func.attr in ("load", "loads") \
                    and isinstance(node.func.value, ast.Name) and node.func.value.id == "json":
                if (f.name, enclosing.get(id(node))) in ALLOWED_CALLS:
                    continue
                offenders.append(f"{f.name}:{node.lineno}")
    assert not offenders, f"artefact JSON decoded outside load_attempt: {offenders}"


def test_the_live_payload_is_validated_at_ingestion_too():
    """harness has no FILE to load, so it cannot use load_attempt -- but it must apply the
    same shape check before dereferencing a live response, or a server returning
    `{"result": [1]}` crashes three frames later."""
    import importlib, os
    os.environ.setdefault("CORPUS_BASE", "http://127.0.0.1:1/api/investigations")
    h = importlib.import_module("harness")
    bad = h.validate_live_payload(200, {"result": [1]})
    assert bad.get("failure", {}).get("code") == "acr_malformed_response", bad
    assert h.validate_live_payload(200, "a string").get("failure")
    good = {"result": {"status": "partial"}}
    assert h.validate_live_payload(200, good) is good


def test_the_allowlisted_decoders_do_not_read_attempt_artefacts():
    """Negative control for the pin above: the allowlist must not be a loophole. Each
    allowed module is checked to decode only the document kinds its reason names."""
    import re as _re
    harness = (HERE / "harness.py").read_text()
    assert "resp.read()" in harness, "harness.py no longer decodes a live response"
    for name in ("merge_corpus.py", "reclassify_deadlines.py"):
        src = (HERE / name).read_text()
        for m in _re.finditer(r"json\.loads?\(([^)]*)\)", src):
            arg = m.group(1)
            assert "replicate" not in arg, f"{name} decodes an attempt artefact: {arg}"


# --------------------------------------------------------------------------- ordering
def test_attempt_order_is_numeric_not_lexicographic():
    got = [Path(p).name for p in order_attempts(
        [f"d/q-rep1-t{n}-a1.json" for n in (1, 2, 9, 10, 11)])]
    assert got == [f"q-rep1-t{n}-a1.json" for n in (1, 2, 9, 10, 11)], got


def test_run_shard_uses_the_shared_ordering_helper():
    t = (HERE / "run_shard.py").read_text()
    assert "order_attempts" in t, "run_shard.py does not use the shared ordering helper"
    assert "sorted(glob.glob" not in t, "run_shard.py still path-sorts attempts"


def test_run_shard_last_attempt_picks_t10_over_t9():
    sys.path.insert(0, str(HERE))
    import importlib
    rs = importlib.import_module("run_shard")
    with tempfile.TemporaryDirectory() as tmp:
        d = Path(tmp)
        for n in (9, 10):
            (d / f"q-rep1-t{n}-a1.json").write_text("{}")
        assert Path(rs.last_attempt_file(d, "q", 1)).name == "q-rep1-t10-a1.json"


# --------------------------------------------------------------------------- isolation
def test_isolation_is_by_process_not_by_sys_modules():
    assert not (HERE / "corpus_stub.py").exists(), "corpus_stub.py still exists"
    for name in ("test_shard_plan.py", "test_findings_r1.py", "test_findings_r2.py",
                 "test_findings_r3.py", "test_findings_r4.py", "test_instrument.py"):
        f = HERE / name
        if f.exists():
            t = f.read_text()
            # Look for an ASSIGNMENT or a deletion, not a mention -- this file names the
            # pattern in its own assertion, and a substring check would flag itself.
            import re as _re
            bad = _re.search(r'sys\.modules\[[\'"]corpus[\'"]\]\s*=', t) or \
                  _re.search(r'sys\.modules\.pop\(\s*[\'"]corpus', t)
            assert not bad, f"{name} still mutates sys.modules: {bad.group(0)!r}"


def test_a_fresh_interpreter_without_the_testdata_path_has_no_corpus():
    code = (f"import sys; sys.path.insert(0, {str(HERE)!r});"
            "import expectations, subject_identity, validators, attempt_order;"
            "print('CORPUS' if 'corpus' in sys.modules else 'NOCORPUS')")
    out = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True,
                         env={"PATH": "/usr/bin:/bin"})
    assert out.stdout.strip().endswith("NOCORPUS"), out.stdout.strip()


if __name__ == "__main__":
    fails = 0
    for name, fn in sorted(globals().items()):
        if name.startswith("test_") and callable(fn):
            try:
                fn()
                print(f"PASS  {name}")
            except Exception as exc:
                fails += 1
                print(f"FAIL  {name}: {type(exc).__name__}: {str(exc)[:170]}")
    print(f"\n{fails} failing")
    raise SystemExit(1 if fails else 0)
