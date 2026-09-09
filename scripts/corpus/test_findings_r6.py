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


# r9: the domain, written HERE and independent of the file. `_full_domain()` used to read
# `doc["_domain"]` -- the same mutable file it was checking -- so deleting the "named"
# basis AND its 336 cells left set-equality passing on a 336-cell file. The oracle could
# see a missing CELL and not a missing AXIS. This constant is the second source; the file's
# own `_domain` is cross-checked against it, so an edit to either is caught.
DOMAIN_EXPECT = ["serve", "refuse", "decline", "clarify"]
DOMAIN_BASIS = ["none", "named"]
DOMAIN_TERMINAL = ["complete", "partial", "degraded", "answered",
                   "no_match", "refused",
                   "clarification_required(max_turns_exhausted)",
                   "http_502:acr_investigation_failed", "http_400:acr_rejected_request",
                   None, "", "future_status"]
DOMAIN_BUCKET = ["served_with_data", "served_degraded", "unserved",
                 "clarification_needed", "error", None, "nonsense"]
EXPECTED_CELLS = (len(DOMAIN_EXPECT) * len(DOMAIN_BASIS)
                  * len(DOMAIN_TERMINAL) * len(DOMAIN_BUCKET))


def _full_domain(doc=None):
    """The domain from the HAND-WRITTEN constants, never from the file under test."""
    return {(e, b, t, k) for e in DOMAIN_EXPECT for b in DOMAIN_BASIS
            for t in DOMAIN_TERMINAL for k in DOMAIN_BUCKET}


def test_the_files_declared_domain_matches_the_independent_constant():
    """Cross-check both directions. Either the file or this constant drifting is caught."""
    d = _G["_domain"]
    assert d["expect"] == DOMAIN_EXPECT, d["expect"]
    assert d["basis"] == DOMAIN_BASIS, d["basis"]
    assert d["terminal"] == DOMAIN_TERMINAL, d["terminal"]
    assert d["bucket"] == DOMAIN_BUCKET, d["bucket"]
    assert len(_G["cells"]) == EXPECTED_CELLS, f'{len(_G["cells"])} != {EXPECTED_CELLS}'


def test_the_oracle_catches_a_whole_dimension_being_removed():
    """r9's repro: drop the `named` basis from `_domain` AND its 336 cells. The old check
    read the domain from the file, so it passed. It must now fail."""
    import copy
    m = copy.deepcopy(_G)
    m["_domain"]["basis"] = ["none"]
    m["cells"] = [c for c in m["cells"] if c["basis"] != "named"]
    assert _coords(m) != _full_domain(), "a removed AXIS still passes set equality"
    assert len(m["cells"]) != EXPECTED_CELLS


# ============================================================ (c) the oracle
def test_golden_coordinates_are_SET_EQUAL_to_the_full_domain():
    """Set equality, not cardinality. A file missing one cell and carrying one extra has
    the right length and the wrong contents -- which the length check could not see."""
    have, want = _coords(_G), _full_domain()
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
        if _coords(doc) != _full_domain():
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


def test_no_pin_derives_an_EXPECTED_value_from_the_code_under_test():
    """AST sweep over every pin file: a call to the scorer may produce an ACTUAL value, but
    never an EXPECTED one. The mirror trap has now appeared four times -- a pin recomputing
    the implementation's fallback, one grepping a comment, one matching its own assertion
    string, one deriving the domain from the file it checked. This looks for the shape:
    a scorer call whose result is compared against another scorer call, or assigned to a
    name that reads as an expectation.
    """
    DERIVERS = {"score", "terminal_key", "weak_kind_for", "expectation_for"}
    EXPECT_NAMES = {"want", "expected", "expect_value", "golden", "oracle"}
    offenders = []
    for f in sorted(HERE.glob("test_*.py")):
        tree = ast.parse(f.read_text())
        for node in ast.walk(tree):
            # an expectation-named variable assigned from a scorer call
            if isinstance(node, ast.Assign):
                for tgt in node.targets:
                    if isinstance(tgt, ast.Name) and tgt.id in EXPECT_NAMES:
                        for sub in ast.walk(node.value):
                            if isinstance(sub, ast.Call):
                                fn = sub.func
                                nm = fn.attr if isinstance(fn, ast.Attribute) else \
                                    getattr(fn, "id", None)
                                if nm in DERIVERS:
                                    offenders.append(f"{f.name}:{node.lineno} {tgt.id}")
            # both sides of a comparison produced by the scorer
            if isinstance(node, ast.Compare) and node.comparators:
                def _is_deriver(n):
                    for sub in ast.walk(n):
                        if isinstance(sub, ast.Call):
                            fn = sub.func
                            nm = fn.attr if isinstance(fn, ast.Attribute) else \
                                getattr(fn, "id", None)
                            if nm in DERIVERS:
                                return True
                    return False
                if _is_deriver(node.left) and any(_is_deriver(c) for c in node.comparators):
                    offenders.append(f"{f.name}:{node.lineno} both sides derived")
    assert not offenders, f"pins deriving an expectation from the code under test: {offenders}"


def test_that_no_derivation_pin_can_itself_see_the_trap():
    """Negative control: feed it the shape and check it fires."""
    probe = ast.parse("want = E.score(a, b)[0]\nassert E.score(c, d)[0] == want\n")
    hits = [n for n in ast.walk(probe) if isinstance(n, ast.Assign)
            and any(isinstance(t, ast.Name) and t.id == "want" for t in n.targets)]
    assert hits, "the detector cannot see an expectation assigned from a scorer call"


# ============================================================ (a) schema as data
# r9 #7: the sweep is a FUNCTION so a negative control can feed it a synthetic module. As
# an inline loop it could only ever be run against the real consumers, which pass -- so a
# broken sweep and a clean codebase were indistinguishable, and that is what let the
# aliased `prev_result` dereference through.
# CHAOS-5430: nodes are addressed by their full measured path now that the schema is
# generated. The seeds move with them; the sweep's property is unchanged.
_SEEDS = {"result": "attempt.response.result",
          "sr": "attempt.response.result.subject_resolution",
          "failure": "attempt.response.failure", "fail": "attempt.response.failure",
          "resp": "attempt.response", "payload": "attempt.response",
          "a": "attempt", "attempt": "attempt", "last": "attempt"}


def _unschemad_paths(src, label, schema=None):
    """Delegates to consumer_paths.sweep -- ONE implementation.

    This module used to carry its own copy of the sweep, and round 1 found the copy was
    weaker than the code it guarded: it recognised only `Name["key"]` and `Name.get("key")`.
    Two implementations of "what does the code read" will always drift, and the weaker one
    is the one that passes.
    """
    import consumer_paths
    schema = V.schema() if schema is None else schema
    reads, _unresolved = consumer_paths.sweep(src, schema, label)
    return [f"{label}: {node}.{key}" for node, key, _u in sorted(reads)
            if node in schema and key not in schema[node]]


def test_the_consumer_sweep_catches_an_ALIASED_dereference():
    """r9 #7's negative control. The old sweep tracked names, so the SAME unschema'd read
    was invisible once it went through an alias. Both spellings must be caught, and the
    alias must survive a second hop."""
    schema = V.schema()
    direct = _unschemad_paths(
        'def f(payload):\n    return payload["result"].get("invented_key")\n', "probe")
    aliased = _unschemad_paths(
        'def f(payload):\n    r = payload["result"]\n    return r.get("invented_key")\n', "probe")
    two_hop = _unschemad_paths(
        'def f(payload):\n    r = payload["result"]\n    q = r\n'
        '    return q.get("invented_key")\n', "probe")
    assert aliased, "an aliased unschema'd dereference is invisible to the sweep"
    assert two_hop, "a two-hop alias is invisible to the sweep"
    assert "attempt.response.result.invented_key" in aliased[0], aliased
    # and it must not fire on a key the schema DOES type, or it is just noise
    assert not _unschemad_paths(
        'def f(payload):\n    r = payload["result"]\n    return r.get("limitations")\n',
        "probe"), "the sweep fires on a schema'd path"


def test_no_consumer_dereferences_a_path_absent_from_the_schema():
    """AST sweep over the consumers. Every key read off an attempt-shaped object must be
    typed in artefact_schema.json, so validation cannot be shallower than the code."""
    missing = []
    for name in ("subject_identity.py", "engine_failures.py", "run_shard.py", "harness.py"):
        missing += _unschemad_paths((HERE / name).read_text(), name)
    assert not missing, f"paths dereferenced but not in the schema: {sorted(set(missing))}"


def test_the_schema_pin_would_notice_a_new_path():
    """Negative control: the pin must fail on an added dereference, or it proves nothing."""
    schema = V.schema()
    R = "attempt.response.result"
    assert "subject_resolution" in schema[R], "schema shape changed"
    assert "invented_key" not in schema[R]


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
# MOVED for CHAOS-5380 (was 5171dfe1c6091b7c). `classify()` now asks
# `attempt_classes.is_success_status(http)` instead of spelling `http != 200` itself, so
# the row bucketer and the attempt classifier cannot hold different opinions about one
# artefact -- a 201 classified `ok_200` at attempt level while its own row bucketed
# `error`, and a pin asserted that WAS correct "as a positive control".
#
# THE RE-CHECK THIS PIN DEMANDS WAS DONE BEFORE THE DIGEST MOVED, and the digest written
# here is the one THIS PIN PRINTED, never one re-derived with another extraction. All
# seven archived arms were re-merged under both instruments (pristine
# f4dfef13a1e86b8b759af81aa0503c6a4b931ff6 vs this branch) and compared PER ROW:
# 252 rows, 0 moved bucket, 0 pre-existing row keys differ, and the seven
# expectation_summary_split lines are identical arm for arm. The predicate is equivalent
# for every status the archives contain; it differs only over 201-399, which no archive
# holds and which the producer has never treated as served.
# MOVED AGAIN for CHAOS-5380 (was 6b5fd8caa99267d1, and 5171dfe1c6091b7c before that).
# `classify()` now asks `contract.is_success_status` rather than reaching through the
# classifier for the same function: the producer's contract lives in one module that both
# the producer and every reader import. The FUNCTION IS THE SAME OBJECT -- only the name
# the call site spells changed -- so no behaviour moved, and the re-check this pin demands
# was done anyway, before the digest: all seven archived arms re-merged under both the
# merge-base instrument and this branch and compared PER ROW -- 252 rows, 0 moved bucket,
# 0 pre-existing row keys differ, seven expectation_summary_split lines identical arm for
# arm. The digest written here is the one THIS PIN PRINTED.
ACCEPTED_CLASSIFY_SHA = "2fa5c1c6ee36ed88"


def _classify_source():
    import re
    src = (HERE / "merge_corpus.py").read_text()
    m = re.search(r"def classify\(row\):.*?(?=\nBUCKETS)", src, re.S)
    assert m, "classify() not found"
    return m.group(0)


def test_classify_no_longer_matches_by_substring():
    """classify() was byte-frozen for eight commits and the freeze was withdrawn on the
    evidence that changing it moves no bucket. The hash is pinned so a further change is
    a deliberate act with its own re-check, not a drift."""
    import hashlib
    got = hashlib.sha256(_classify_source().encode()).hexdigest()[:16]
    assert got == ACCEPTED_CLASSIFY_SHA, (
        f"classify() changed ({got}); re-verify per-row buckets across all four arms "
        "before accepting it")
    # Assert on the AST, not the text. The function's own comment explains the substring
    # test it replaced, so a text search matches its documentation -- the third time that
    # trap has appeared in this lane (a pin grepping a comment, a pin matching its own
    # assertion string, and now this).
    import ast as _ast
    fn = next(n for n in _ast.walk(_ast.parse(_classify_source()))
              if isinstance(n, _ast.FunctionDef) and n.name == "classify")
    substrings = [n for n in _ast.walk(fn)
                  if isinstance(n, _ast.Compare) and n.ops and isinstance(n.ops[0], _ast.In)
                  and isinstance(n.left, _ast.Constant)
                  and isinstance(n.left.value, str) and "clarification" in n.left.value]
    assert not substrings, "the substring test is back in the bucketer (AST)"
    names = {n.id for n in _ast.walk(fn) if isinstance(n, _ast.Name)}
    assert "CLARIFICATION_VALUES" in names, "the bucketer no longer reads the authored vocabulary"


def test_no_substring_matching_on_status_anywhere():
    """AST across ALL modules, WITH NO EXCEPTION. The bucketer used to be allowlisted by
    hash because it was frozen; the freeze was withdrawn once the evidence showed the
    change moves no bucket, so the allowlist is gone and every site is an offence."""
    offenders = []
    for f in sorted(HERE.glob("*.py")):
        if f.name.startswith("test_"):
            continue
        tree = ast.parse(f.read_text())
        for node in ast.walk(tree):
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




def test_an_unauthored_terminal_is_unserved_named_and_counted():
    """The bucketer is now fail-closed, so an unauthored terminal lands in `unserved` --
    quietly, unless it is named. Both halves are pinned: the bucket it falls into AND the
    reason plus count that make it visible."""
    from merge_corpus import classify
    row = lambda st: {"final_http": 200, "final_payload_status": st,
                      "chain": f"t1={st}", "claimed_facts_n": 0}
    assert classify(row("clarification_required(max_turns_exhausted)")) == "clarification_needed"
    assert classify(row("clarification_required")) == "clarification_needed"
    # fail-closed: an unauthored terminal is NOT bucketed as a clarification
    assert classify(row("clarification_required(future)")) == "unserved"
    assert classify(row("clarification_bogus")) == "unserved"

    v, why = E.score({"expectation": "clarify", "expectation_basis": None},
                     "unserved", terminal_status="clarification_required(future)")
    assert v == "unscored", v
    assert why == "unauthored_terminal:clarification_required(future)", why

    rows = {"q": {"id": "q", "expect": "clarify", "basis": None, "anchor": None,
                  "nonexistent": False, "family": "f"}}
    tb = E.table(rows, {"q": "unserved"}, {"q": False},
                 terminals_by_id={"q": "clarification_required(future)"})
    n = sum(1 for e in tb if str(e.get("why", "")).startswith("unauthored_terminal:"))
    assert n == 1, f"an unauthored terminal was not counted: {tb}"


def test_ARRAY_ELEMENTS_are_typed_not_just_the_array():
    """r9 #1, RED-FIRST against the tip. `committed: [1]` validated -- the array was a
    list, so the check stopped there -- and then crashed `committed[0].get(...)` in
    subject_identity. Typing a container and trusting its members is the same shallow
    boundary one level lower, which is the class this lane has now closed four times."""
    bad = {
        "subject_resolution.committed[0] as int":
            {"response": {"result": {"subject_resolution": {"committed": [1]}}}},
        "subject_resolution.candidates[0] as str":
            {"response": {"result": {"subject_resolution": {"candidates": ["x"]}}}},
        "result.claimed_facts[0] as int":
            {"response": {"result": {"claimed_facts": [1]}}},
        "result.limitations[0] as object":
            {"response": {"result": {"limitations": [{"a": 1}]}}},
    }
    for label, a in bad.items():
        ok, reason = V.validate_attempt(a)
        assert not ok, f"{label} validated true -- elements are not typed"
        assert "[0]" in reason, f"{label}: reason does not name the element: {reason}"
    # the measured shapes must still pass, or the boundary is rejecting real data
    ok, reason = V.validate_attempt({"response": {"result": {
        "limitations": ["a string, which is what the corpus actually carries"],
        "claimed_facts": [{"k": "v"}],
        "structure_needs": {"k": "v"},
        "subject_resolution": {"committed": [{"kind": "team", "canonical_id": "t1"}]}}}})
    assert ok, f"a MEASURED artefact shape was rejected: {reason}"


def test_the_element_pin_reflects_MEASURED_types_not_assumed_ones():
    """The first draft of this schema typed `limitations` as objects and `structure_needs`
    as an array, both by analogy with their neighbours. Both were wrong -- the corpus
    carries strings and a mapping -- and together they rejected 233 of 425 real attempts,
    which would have shipped as a scoring regression. So the types are asserted against
    what the artefacts contain, and the schema records that they were measured."""
    schema = V.schema()
    R = "attempt.response.result"
    assert schema[R]["limitations"]["items"] == "string", schema[R]["limitations"]
    assert schema[R]["structure_needs"]["type"] == "object", schema[R]["structure_needs"]
    assert schema[f"{R}.subject_resolution"]["committed"]["items"] == "object"
    # CHAOS-5430: provenance is no longer a note asserting the types were measured -- every
    # entry CARRIES its observation counts, which is the same claim backed by the data.
    assert schema[R]["limitations"]["observed"], "types no longer carry their observations"


def test_the_LIVE_body_goes_through_the_same_boundary_as_a_file():
    """r9 #2. There are two ingestion points and only the file one was deep, so a server
    returning `result.structure_needs: "x"` passed and crashed run_replicate three frames
    later. A malformed live body must come back as a failure ENVELOPE -- naming the reason,
    so the row records what happened -- and never as a payload a consumer will dereference."""
    import harness
    for label, body in (
            ("structure_needs as str", {"result": {"structure_needs": "x"}}),
            ("committed[0] as int",
             {"result": {"subject_resolution": {"committed": [1]}}}),
            ("result as list", {"result": [1]}),
    ):
        out = harness.validate_live_payload(200, body)
        assert "failure" in out, f"{label}: a malformed live body was accepted"
        assert out["failure"]["code"] == "acr_malformed_response", out
        assert out["failure"]["message"], f"{label}: rejected without naming the reason"
    # a non-mapping body is rejected too, and says so
    out = harness.validate_live_payload(200, "not a mapping")
    assert out["failure"]["code"] == "acr_malformed_response", out
    # a well-formed body passes through UNCHANGED -- the boundary must not rewrite evidence
    good = {"result": {"status": "complete", "limitations": ["s"]}}
    assert harness.validate_live_payload(200, good) is good


def test_an_unauthored_terminal_is_named_even_when_the_row_declares_NOTHING():
    """r9 #9. The naming sat AFTER the expectation early-returns, so the 16 rows of 36 that
    declare no expectation reported `no_expectation` and their unauthored terminal was
    never counted. A visibility feature silent on the majority of rows is the silence it
    exists to remove. Both the no-expectation and the invalid-declaration paths are pinned,
    because both returned early."""
    v, why = E.score({"expectation": None}, "unserved", terminal_status="future_status")
    assert v == "unscored", v
    assert why.startswith("unauthored_terminal:future_status"), why
    assert "no_expectation" in why, "the row's own reason was dropped"

    v, why = E.score({"expectation": E.INVALID, "invalid_reason": "expect must be one of"},
                     "unserved", terminal_status="future_status")
    assert v == "unscored", v
    assert why.startswith("unauthored_terminal:future_status"), why
    assert "invalid_expectation" in why, "the row's own reason was dropped"

    # an AUTHORED terminal on the same paths must NOT be named, or the counter is noise
    _, why = E.score({"expectation": None}, "unserved", terminal_status="no_match")
    assert not why.startswith("unauthored_terminal:"), why
    for empty in (None, ""):
        _, why = E.score({"expectation": None}, "unserved", terminal_status=empty)
        assert not why.startswith("unauthored_terminal:"), f"{empty!r}: {why}"


def test_reading_the_domain_from_the_file_under_test_would_MISS_an_axis_removal():
    """r9 #10's negative control, and the reason the constant exists. Reconstruct the old
    oracle -- domain read from `doc["_domain"]` -- and show it passes on a file with a
    whole basis and its 336 cells removed, while the hand-written domain fails. Without
    this, the constant is an unproven claim that it is stronger."""
    import copy
    m = copy.deepcopy(_G)
    m["_domain"]["basis"] = ["none"]
    m["cells"] = [c for c in m["cells"] if c["basis"] != "named"]
    assert len(m["cells"]) < len(_G["cells"]), "the mutation removed nothing"

    def old_domain(doc):
        d = doc["_domain"]
        return {(e, b, t, k) for e in d["expect"] for b in d["basis"]
                for t in d["terminal"] for k in d["bucket"]}

    assert _coords(m) == old_domain(m), \
        "the file-derived oracle should be blind here -- the control no longer reproduces r9"
    assert _coords(m) != _full_domain(), "the hand-written domain missed a removed axis"


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
