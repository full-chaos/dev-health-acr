"""RED-FIRST pins for round 6. The oracle checks ITSELF this time.

Every previous round the pin was weaker than the thing it guarded: it mirrored the code,
or covered a slice, or checked cardinality where set equality was meant. So these assert
the oracle's own properties -- including that a mutated authored table FAILS.
"""
import ast
import json
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).parent
sys.path.insert(0, str(HERE))

import expectations as E          # noqa: E402
import validators as V            # noqa: E402
from attempt_order import is_replay, order_attempts  # noqa: E402

GOLDEN_FILE = HERE / "golden_verdicts.json"
_G = json.loads(GOLDEN_FILE.read_text())


def _coords(doc):
    return {(c["expect"], c["basis"], c["terminal"], c["bucket"]) for c in doc["cells"]}


def _full_domain(doc):
    d = doc["_domain"]
    return {(e, b, t, k) for e in d["expect"] for b in d["basis"]
            for t in d["terminal"] for k in d["bucket"]}


# ============================================================ (c) the oracle
def test_golden_coordinates_are_SET_EQUAL_to_the_full_domain():
    """Set equality, not cardinality. A file missing one cell and carrying one extra has
    the right length and the wrong contents -- which the length check could not see."""
    have, want = _coords(_G), _full_domain(_G)
    assert have == want, (f"missing {sorted(want - have)[:4]}; extra {sorted(have - want)[:4]}")


def test_every_cell_matches_on_BOTH_verdict_and_weak_kind():
    bad = []
    for c in _G["cells"]:
        e = {"expectation": c["expect"],
             "expectation_basis": ("named_basis" if c["basis"] == "named" else None)}
        verdict, why = E.score(e, c["bucket"], terminal_status=c["terminal"])
        kind = E.weak_kind_for(verdict, why)
        if verdict != c["verdict"] or kind != c["weak_kind"]:
            bad.append((c["expect"], c["basis"], c["terminal"], c["bucket"],
                        (c["verdict"], c["weak_kind"]), (verdict, kind)))
    assert not bad, f"{len(bad)} cells disagree on verdict or weak_kind: {bad[:4]}"


def test_the_oracle_detects_its_own_mutation():
    """Drop a cell, add one, flip a weak_kind -- all three must be caught. Without this the
    pin proves the code matches the file, but nothing proves the file was not edited."""
    def check(doc):
        problems = []
        if _coords(doc) != _full_domain(doc):
            problems.append("coordinates")
        for c in doc["cells"]:
            e = {"expectation": c["expect"],
                 "expectation_basis": ("named_basis" if c["basis"] == "named" else None)}
            v, why = E.score(e, c["bucket"], terminal_status=c["terminal"])
            if v != c["verdict"] or E.weak_kind_for(v, why) != c["weak_kind"]:
                problems.append("cell")
                break
        return problems

    assert not check(_G), "the unmutated file must pass"

    dropped = json.loads(GOLDEN_FILE.read_text())
    dropped["cells"] = dropped["cells"][1:]
    assert check(dropped), "dropping a cell went undetected"

    added = json.loads(GOLDEN_FILE.read_text())
    extra = dict(added["cells"][0]); extra["terminal"] = "invented_status"
    added["cells"] = added["cells"] + [extra]
    assert check(added), "an extra coordinate went undetected"

    flipped = json.loads(GOLDEN_FILE.read_text())
    for c in flipped["cells"]:
        if c["weak_kind"] == "weak_hollow_serve":
            c["weak_kind"] = "weak_never_terminated"
            break
    assert check(flipped), "a flipped weak_kind went undetected"


def test_the_rulings_text_matches_the_cells():
    txt = _G["_rulings"]["3"]
    assert "not authored" in txt or "unscored" in txt, \
        f"_rulings.3 still claims a blanket http_* rule: {txt!r}"


# ============================================================ (a) schema as data
def test_no_consumer_dereferences_a_path_absent_from_the_schema():
    """AST sweep over the consumers. Every key read off an attempt-shaped object must be
    typed in artefact_schema.json, so validation cannot be shallower than the code."""
    schema = V.schema()
    ROOTS = {"result": "result", "sr": "subject_resolution", "failure": "failure",
             "fail": "failure", "resp": "response", "payload": "response",
             "a": "attempt", "attempt": "attempt", "last": "attempt"}
    missing = []
    for name in ("subject_identity.py", "engine_failures.py", "run_shard.py", "harness.py"):
        tree = ast.parse((HERE / name).read_text())
        for node in ast.walk(tree):
            key = base = None
            if isinstance(node, ast.Call) and isinstance(node.func, ast.Attribute) \
                    and node.func.attr == "get" and node.args \
                    and isinstance(node.func.value, ast.Name) \
                    and isinstance(node.args[0], ast.Constant) \
                    and isinstance(node.args[0].value, str):
                base, key = node.func.value.id, node.args[0].value
            if base in ROOTS and key:
                node_name = ROOTS[base]
                if node_name in schema and key not in schema[node_name]:
                    missing.append(f"{name}: {node_name}.{key}")
    assert not missing, f"paths dereferenced but not in the schema: {sorted(set(missing))}"


def test_the_schema_pin_would_notice_a_new_path():
    """Negative control: the pin must fail on an added dereference, or it proves nothing."""
    schema = V.schema()
    assert "subject_resolution" in schema["result"], "schema shape changed"
    assert "invented_key" not in schema["result"]


def test_validation_is_recursive_not_shallow():
    cases = {
        "result.subject_resolution as str": {"response": {"result": {"subject_resolution": "x"}}},
        "failure.narrowerContinuation as str": {"response": {"failure": {"narrowerContinuation": "x"}}},
        "result.claimed_facts as str": {"response": {"result": {"claimed_facts": "x"}}},
        "subject_resolution.committed as str":
            {"response": {"result": {"subject_resolution": {"committed": "x"}}}},
    }
    for label, a in cases.items():
        ok, reason = V.validate_attempt(a)
        assert not ok, f"{label} validated true"
        assert reason, label
    ok, _ = V.validate_attempt(
        {"response": {"result": {"subject_resolution": {"committed": []}, "claimed_facts": []}}})
    assert ok, "a valid deep artefact was rejected"


# ============================================================ (b) no substrings
FROZEN_CLASSIFY_SHA = "af37f7364fa5b71c"


def _classify_source():
    import re
    src = (HERE / "merge_corpus.py").read_text()
    m = re.search(r"def classify\(row\):.*?(?=\nBUCKETS)", src, re.S)
    assert m, "classify() not found"
    return m.group(0)


def test_classify_is_still_byte_frozen():
    """The allowlist below is keyed to this hash. If classify() changes, the allowlist is
    void and the substring pin must fail rather than quietly keep excusing it."""
    import hashlib
    got = hashlib.sha256(_classify_source().encode()).hexdigest()[:16]
    assert got == FROZEN_CLASSIFY_SHA, (
        f"classify() changed ({got}); the substring allowlist no longer applies and the "
        "positive-control invariant needs re-checking")


def test_no_substring_matching_on_status_anywhere_except_frozen_classify():
    """AST across ALL modules. classify() is allowlisted BY HASH, not by name: it is
    byte-frozen so a prior run re-merges identically, and that invariant outranks internal
    consistency here. The exception is recorded in golden_verdicts.json
    `_known_inconsistencies` and surfaced at runtime as `unauthored_terminal:<status>`.
    Every OTHER site is an offence."""
    classify_lines = set()
    src = (HERE / "merge_corpus.py").read_text()
    start = src[:src.index("def classify(row):")].count("\n") + 1
    classify_lines = set(range(start, start + _classify_source().count("\n") + 1))

    offenders = []
    for f in sorted(HERE.glob("*.py")):
        if f.name.startswith("test_"):
            continue
        tree = ast.parse(f.read_text())
        for node in ast.walk(tree):
            if f.name == "merge_corpus.py" and getattr(node, "lineno", None) in classify_lines:
                continue
            if isinstance(node, ast.Compare) and node.ops and isinstance(node.ops[0], ast.In):
                left = node.left
                if isinstance(left, ast.Constant) and isinstance(left.value, str) \
                        and left.value in ("clarification", "clarification_required",
                                           "http_", "servable", "expect"):
                    offenders.append(f"{f.name}:{node.lineno}")
            if isinstance(node, ast.Call) and isinstance(node.func, ast.Attribute) \
                    and node.func.attr in ("startswith", "endswith") and node.args:
                arg = node.args[0]
                if isinstance(arg, ast.Constant) and isinstance(arg.value, str) \
                        and any(k in arg.value for k in ("clarification", "http_")):
                    offenders.append(f"{f.name}:{node.lineno} ({node.func.attr})")
    assert not offenders, f"substring/prefix matching on a status: {offenders}"


def test_the_substring_pin_would_catch_a_new_offence():
    """Negative control: the allowlist must not blind the pin elsewhere. A substring test
    injected into another module has to be seen."""
    import ast as _ast
    probe = _ast.parse('def f(status):\n    return "clarification" in status\n')
    found = [n for n in _ast.walk(probe)
             if isinstance(n, _ast.Compare) and n.ops and isinstance(n.ops[0], _ast.In)
             and isinstance(n.left, _ast.Constant) and n.left.value == "clarification"]
    assert found, "the detector cannot see a substring test at all"


def test_an_unauthored_terminal_is_named_and_counted():
    """The inconsistency is VISIBLE, not silent: the scorer names the status and the merge
    counts it with an explicit zero, so a future clarification_required(x) appears on the
    line instead of being absorbed into clarification_needed by the frozen bucketer."""
    v, why = E.score({"expectation": "clarify", "expectation_basis": None},
                     "clarification_needed", terminal_status="clarification_required(future)")
    assert v == "unscored" and why == "unauthored_terminal:clarification_required(future)", (v, why)
    assert "_known_inconsistencies" in _G, "the golden file does not record the exception"
    rec = _G["_known_inconsistencies"][0]
    assert rec["frozen_function_sha256_prefix"] == FROZEN_CLASSIFY_SHA
    assert rec["observed_in_the_four_measured_runs"] == 0


def test_the_known_divergence_between_bucketer_and_scorer_is_pinned():
    """Pin the divergence the ruling ACCEPTS, not its absence.

    classify() is byte-frozen, so it still buckets by substring: an unauthored
    `clarification_required(future)` lands in clarification_needed there. The scorer does
    NOT score it and names it. Asserting that classify() rejects it would pin behaviour the
    ruling deliberately did not adopt, and would go red the moment someone read the code
    and believed the pin. This states what is true, so the divergence cannot be forgotten.
    """
    from merge_corpus import classify
    row = lambda st: {"final_http": 200, "final_payload_status": st,
                      "chain": f"t1={st}", "claimed_facts_n": 0}
    # the frozen bucketer, substring behaviour intact
    assert classify(row("clarification_required(max_turns_exhausted)")) == "clarification_needed"
    assert classify(row("clarification_required(future)")) == "clarification_needed", \
        "classify() no longer buckets by substring -- the recorded inconsistency is stale"
    # the scorer, authored instances only
    known = E.score({"expectation": "clarify", "expectation_basis": None},
                    "clarification_needed",
                    terminal_status="clarification_required(max_turns_exhausted)")[0]
    unknown = E.score({"expectation": "clarify", "expectation_basis": None},
                      "clarification_needed", terminal_status="clarification_required(future)")
    assert known == "agree"
    assert unknown[0] == "unscored" and unknown[1].startswith("unauthored_terminal:")
    # and neither occurs in the measured runs
    assert _G["_known_inconsistencies"][0]["observed_in_the_four_measured_runs"] == 0


# ============================================================ (d) unsequenced + separators
def test_unsequenced_files_are_surfaced_never_discarded():
    import subject_identity as SI
    with tempfile.TemporaryDirectory() as tmp:
        d = Path(tmp) / "replicate"; d.mkdir(parents=True)
        (d / "q-rep1-t1-a1-extra.json").write_text("{}")
        rec = SI.inspect(tmp, "q", E.expectation_for(
            {"id": "q", "expect": "serve", "basis": None, "anchor": None, "nonexistent": False}))
        assert rec["state"].startswith("unsequenced:"), rec["state"]
        assert rec.get("unsequenced_files"), rec
    src = (HERE / "run_shard.py").read_text()
    assert "UNSEQUENCED" in src and "unsequenced_files" in src, \
        "run_shard still discards what the ordering helper reports"


def test_replay_detection_handles_both_separators():
    assert is_replay("s/reclassify/q/replicate/q-rep1-t1-a1.json")
    assert is_replay("s\\reclassify\\q\\replicate\\q-rep1-t1-a1.json")
    assert not is_replay("s/shard-00/replicate/q-rep1-t1-a1.json")


def test_ordering_still_numeric_and_replay_last():
    got = [Path(p).name for p in order_attempts(
        [f"d/q-rep1-t{n}-a1.json" for n in (1, 9, 10, 11)])]
    assert got == [f"q-rep1-t{n}-a1.json" for n in (1, 9, 10, 11)], got


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
