"""RED-FIRST pins for CHAOS-5430: the boundary is DERIVED, not written.

Round 10 found the same shallow-boundary defect the two rounds before it found, one level
deeper each time. These pins hold the property that ends that -- the schema is a report
about the artefacts -- and each of r10's four findings is pinned at the level where
construction closes it, not at the single example the reviewer happened to name.

Where a pin could pass for the wrong reason, it ships with a negative control that shows
the old behaviour failing it.
"""
import json
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).parent
sys.path.insert(0, str(HERE))

import expectations as E          # noqa: E402
import merge_corpus as MC         # noqa: E402
import subject_identity as SI     # noqa: E402
import validators as V            # noqa: E402

SCHEMA_DOC = json.loads((HERE / "artefact_schema.json").read_text())


def _attempt(result):
    return {"request": {}, "status": 200, "dt": 1.0, "response": {"result": result}}


def _row(committed, mechs=None):
    # receipt_id is REQUIRED now, and every real candidate carries one (observed 1597
    # times). A fixture without it is not a smaller artefact, it is an artefact the engine
    # never emits -- so the fixture changes, not the boundary.
    cands = [{"receipt_id": f"rc{i}", "state": "committed", "subject": c,
              "match_mechanisms": mechs or [], "matched_terms": []}
             for i, c in enumerate(committed)]
    return _attempt({"request_id": "r", "result_id": "res", "status": "complete",
                     "claimed_facts": [{"claim_id": "c0"}],
                     "subject_resolution": {"committed": committed,
                                            "candidates": cands}})


def _verdict(committed):
    """End-to-end: artefact on disk -> identity -> score. Not the validator alone.

    The validator returning ok is not the property that matters; what matters is the
    VERDICT the row ends up with. The first cut of the null policy passed validation and
    the row still scored agree_weak, because the same null was rejected through the
    candidate mirror. Only the end-to-end check saw it.
    """
    with tempfile.TemporaryDirectory() as tmp:
        d = Path(tmp) / "replicate"
        d.mkdir(parents=True)
        (d / "q-rep1-t1-a1.json").write_text(json.dumps(_row(committed)))
        e = E.expectation_for({"id": "q", "expect": "serve", "basis": None,
                               "anchor": {"kind": "team", "label": "Platform"},
                               "nonexistent": False})
        rec = SI.inspect(tmp, "q", e)
        return rec["state"], E.score(
            e, "served_with_data", subject_substitution=rec.get("subject_substitution"),
            identity_state=rec.get("state"), terminal_status="complete")[0]


# ==================================================== the boundary is generated
def test_the_schema_is_GENERATED_and_says_so():
    assert SCHEMA_DOC["_generated_by"] == "scripts/corpus/measure_schema.py"
    assert SCHEMA_DOC["_artefacts_measured"] >= 700, SCHEMA_DOC["_artefacts_measured"]
    assert SCHEMA_DOC["_roots"], "a generated schema must name what it was generated from"
    # depth is the point: the hand-written file had a single-digit number of nodes
    assert len(SCHEMA_DOC["nodes"]) > 100, len(SCHEMA_DOC["nodes"])


def test_regenerating_from_the_same_artefacts_is_BYTE_IDENTICAL():
    """Determinism, and the negative control for hand-editing.

    If the file can be hand-edited and still pass, "generated" is a claim rather than a
    property. Regenerating must reproduce it exactly, so an edit is visible as a diff.
    """
    roots = SCHEMA_DOC["_roots"]
    missing = [r for r in roots if not Path(r).exists()]
    if missing:
        print(f"    SKIP: measured roots absent here: {missing[:1]}")
        return
    with tempfile.TemporaryDirectory() as tmp:
        out = Path(tmp) / "regen.json"
        subprocess.run([sys.executable, str(HERE / "measure_schema.py"),
                        "--out", str(out),
                        "--null-policy", str(HERE / "schema_null_policy.json"),
                        "--declared-paths", str(HERE / "schema_declared_paths.json"),
                        "--roots", *roots],
                       check=True, capture_output=True)
        assert out.read_text() == (HERE / "artefact_schema.json").read_text(), \
            "the committed schema is not what the generator produces from these artefacts"


def test_a_hand_added_key_does_NOT_survive_regeneration():
    """Negative control for the control above: prove the comparison can fail."""
    doc = json.loads((HERE / "artefact_schema.json").read_text())
    doc["nodes"]["attempt"]["invented_by_hand"] = {"type": "string"}
    assert json.dumps(doc) != (HERE / "artefact_schema.json").read_text(), \
        "the byte comparison cannot detect an added key"


# ==================================================== r10 #1: nested live shapes
def test_r10_1_nested_live_shapes_are_typed_by_CONSTRUCTION():
    import harness
    cases = {
        "structure_needs.kind_options[0] as int": {"structure_needs": {"kind_options": [1]}},
        "window_clarification as string": {"window_clarification": "x"},
        "structure_needs as string": {"structure_needs": "x"},
    }
    for label, result in cases.items():
        ok, reason = V.validate_attempt(_attempt(result))
        assert not ok, f"{label} validated true"
        out = harness.validate_live_payload(200, {"result": result})
        assert "failure" in out, f"{label} passed the LIVE boundary"
        assert out["failure"]["code"] == "acr_malformed_response", out


def test_the_nested_paths_exist_in_the_schema_because_they_were_MEASURED():
    """Not because someone remembered them. Both r10 paths carry observation counts."""
    n = SCHEMA_DOC["nodes"]
    sn = n["attempt.response.result"]["structure_needs"]
    assert sn["type"] == "object" and sn["observed"], sn
    assert "attempt.response.result.structure_needs" in n
    assert n["attempt.response.result"]["window_clarification"]["type"] == "object"
    ko = n["attempt.response.result.structure_needs"]["kind_options"]
    assert ko["items"] == "object" and ko.get("element_node"), ko


# ==================================================== r10 #2: committed fields
def test_r10_2_committed_fields_are_typed_but_an_explicit_null_still_DISAGREES():
    """Both halves, because either alone is the bug.

    Typing the fields without admitting null makes a null-kind commit an unreadable
    artefact, and the unreadable cap turns the `disagree` it earns into `agree_weak` --
    doubt making a failed row look better. Admitting null without typing leaves the
    original crash. The pin holds the VERDICT, not the validator.
    """
    assert _verdict([{"kind": None, "canonical_id": None, "label": "Platform"}]) == \
        ("read", "disagree")
    assert _verdict([{"kind": "", "canonical_id": "x", "label": "Platform"}]) == \
        ("read", "disagree")
    assert _verdict([{"kind": "team", "canonical_id": "team:P", "label": "Platform"}]) == \
        ("read", "agree")
    for bad in ({"kind": [], "canonical_id": "x", "label": "P"},
                {"kind": "team", "canonical_id": "x", "label": 1}):
        ok, reason = V.validate_attempt(_row([bad]))
        assert not ok, f"{bad} validated true"


def test_the_null_policy_is_DECLARED_data_and_every_entry_is_used():
    """A policy path that matches nothing is a typo that silently weakens the boundary."""
    doc = json.loads((HERE / "schema_null_policy.json").read_text())
    assert doc["admit_null"], "the policy is empty"
    for e in doc["admit_null"]:
        assert e["consumer"], f"{e['path']} is admitted with no stated consumer"
    assert SCHEMA_DOC["_null_policy_unused"] == [], SCHEMA_DOC["_null_policy_unused"]
    applied = {a["path"] for a in SCHEMA_DOC["_null_policy_applied"]}
    assert applied == {e["path"] for e in doc["admit_null"]}
    # the candidate MIRROR must be covered, or the committed policy is defeated by it
    assert any("candidates[].subject.kind" in p for p in applied), sorted(applied)


# ==================================================== r10 #3: the bare spelling
def test_r10_3_the_bare_clarification_is_authored_for_BOTH_vocabularies():
    row = lambda st: {"final_http": 200, "final_payload_status": st,
                      "chain": f"t1={st}", "claimed_facts_n": 0}
    decl = {"expectation": "clarify", "expectation_basis": None}
    for st in ("clarification_required", "clarification_required(max_turns_exhausted)"):
        assert MC.classify(row(st)) == "clarification_needed", st
        v, why = E.score(decl, "clarification_needed", terminal_status=st)
        assert v == "agree", f"{st}: bucketer and scorer disagree -- {v} {why}"
    # fail-closed is preserved: an unauthored variant is still not scored
    assert MC.classify(row("clarification_required(future)")) == "unserved"
    v, why = E.score(decl, "unserved", terminal_status="clarification_required(future)")
    assert v == "unscored" and why.startswith("unauthored_terminal:"), (v, why)


# ==================================================== r10 #4: bool vs numeric
def test_r10_4_EVERY_numeric_field_rejects_a_bool_not_just_the_one_reported():
    """Swept across the whole measured schema. The guard existed for `int` and the hole was
    in `number`; fixing the reported field would leave the next numeric type open."""
    numeric = [(node, key) for node, keys in SCHEMA_DOC["nodes"].items()
               for key, rule in keys.items() if rule.get("type") in ("int", "number")]
    assert numeric, "no numeric fields measured -- the sweep would be vacuous"
    # Checked at the NODE, so every numeric field is covered wherever it sits, rather than
    # only the handful reachable by a hand-built nesting. A sweep that silently checks
    # nothing is the failure mode this pin is guarding against, so it asserts its own count.
    offenders = []
    for node, key in numeric:
        reason = V._check_node({key: True}, node, node)
        if not reason or "bool" not in reason:
            offenders.append(f"{node}.{key} -> {reason!r}")
    assert not offenders, f"numeric fields that accept a bool: {offenders[:5]}"
    assert len(numeric) >= 20, f"only {len(numeric)} numeric fields swept"
    # and a real numeric value still passes, or the guard is just rejecting everything
    for node, key in numeric[:20]:
        assert V._check_node({key: 1}, node, node) is None, f"{node}.{key} rejected an int"
    ok, _ = V.validate_attempt(_row([{"kind": "t", "canonical_id": "c", "label": "l"}]))
    assert ok


# ==================================================== unreadable attribution
def test_an_unreadable_artefact_on_a_FAILED_row_is_one_defect_not_two():
    recs = [{"corpus_id": "q", "kind": "OTHER_400"},
            {"corpus_id": "q", "kind": "UNREADABLE_ARTEFACT"},
            {"corpus_id": "z", "kind": "UNREADABLE_ARTEFACT"}]
    by = MC.classification_by_row(recs)
    assert by == {"q": ["OTHER_400"]}, by
    assert "z" not in by, "UNREADABLE_ARTEFACT classified a row all by itself"


def test_the_attribution_is_carried_into_the_verdict_artefact():
    src = (HERE / "merge_corpus.py").read_text()
    for key in ("failure_classification_by_row", "unreadable_artefact_detail",
                "unreadable_artefact_rows_standalone"):
        assert f'"{key}"' in src, f"{key} is not emitted"
    assert '"unreadable_artefact_rows"' in src, "the original list was dropped"


# ==================================================== rep tag + loud failure
def test_scan_FAILS_LOUDLY_rather_than_reporting_a_silent_whole_arm_miss():
    from corpus import CORPUS
    with tempfile.TemporaryDirectory() as tmp:
        try:
            SI.scan(tmp, CORPUS, E.expectation_for)
        except SI.NoArtefactsFound as exc:
            assert "no_artefact" in str(exc) and "wrong root" in str(exc), str(exc)
        else:
            raise AssertionError("an empty root returned 36 clean rows")


def test_scan_does_NOT_raise_when_artefacts_are_really_there():
    """Negative control: a pin that only proves it raises would be satisfied by a scan that
    always raises."""
    from corpus import CORPUS
    with tempfile.TemporaryDirectory() as tmp:
        d = Path(tmp) / "replicate"
        d.mkdir(parents=True)
        for r in CORPUS:
            (d / f"{r['id']}-rep1-t1-a1.json").write_text(json.dumps(
                _row([{"kind": "team", "canonical_id": "t", "label": "Platform"}])))
        out = SI.scan(tmp, CORPUS, E.expectation_for)
        assert len(out) == len(CORPUS)
        assert all(v["state"] == "read" for v in out.values())


def test_the_rep_tag_is_read_from_the_run_not_defaulted():
    with tempfile.TemporaryDirectory() as tmp:
        d = Path(tmp) / "replicate"
        d.mkdir(parents=True)
        (d / "q-rep2-t1-a1.json").write_text(json.dumps(_row([])))
        assert SI.rep_from_summaries(tmp) == [2], SI.rep_from_summaries(tmp)
    import inspect as _i
    assert _i.signature(SI.scan).parameters["rep"].default is None, \
        "scan still defaults the rep tag instead of reading it"


# ==================================================== vector wording
def test_the_vector_column_does_not_claim_causality():
    doc = MC.VECTOR_COLUMNS_DOC
    assert "vector participation and no exact mechanism" in doc, doc
    assert "CommitBasisSet" in doc and "non-reconstructible" in doc, doc
    assert "rests on the vector hit" not in doc, "the causal claim is back"


# ==================================================== the boundary is TOTAL
CONSUMERS = ("subject_identity.py", "engine_failures.py", "run_shard.py", "harness.py",
             "merge_corpus.py", "reclassify_deadlines.py")


def test_every_path_a_consumer_READS_is_in_the_boundary():
    """Round 1's P1. The old sweep recognised only `Name["key"]` and `Name.get("key")`, so
    `match[0]["receipt_id"]` and `(failure.get("x") or {}).get("y")` were invisible to it --
    the pin passed while a missing receipt_id crashed the consumer. It now walks the
    EXPRESSION, and it FAILS on a read it cannot resolve instead of skipping one.
    """
    import consumer_paths
    schema = V.schema()
    missing, unresolved = [], []
    for name in CONSUMERS:
        reads, unres = consumer_paths.sweep((HERE / name).read_text(), schema, name)
        unresolved += unres
        for node, key, _uncond in reads:
            if node in schema and key not in schema[node]:
                missing.append(f"{name}: {node}.{key}")
    assert not unresolved, f"consumer reads the sweep cannot resolve: {unresolved}"
    assert not missing, f"consumer paths outside the boundary: {sorted(set(missing))}"


def test_the_sweep_resolves_EVERY_shape_round_1_found_it_missing():
    """One negative control per pattern shape. A sweep is only as good as the expressions
    it can see, and every shape here is one it was blind to when round 1 ran."""
    import consumer_paths
    schema = V.schema()
    R = "attempt.response.result"
    SR = f"{R}.subject_resolution"
    CANDS = f"{SR}.candidates[]"
    shapes = {
        "plain get":        ('def f(result):\n    return result.get("invented")\n', R),
        "plain subscript":  ('def f(result):\n    return result["invented"]\n', R),
        "index result":     ('def f(sr):\n    c = sr["candidates"]\n'
                             '    return c[0]["invented"]\n', CANDS),
        "or-fallback chain": ('def f(result):\n'
                              '    return (result.get("subject_resolution") or {})'
                              '.get("invented")\n', SR),
        "for-loop binding": ('def f(sr):\n    for c in sr["candidates"]:\n'
                             '        c["invented"]\n', CANDS),
        "comprehension":    ('def f(sr):\n'
                             '    return [c["invented"] for c in sr["candidates"]]\n',
                             CANDS),
        "two-hop alias":    ('def f(payload):\n    r = payload["result"]\n'
                             '    q = r\n    return q.get("invented")\n', R),
        "list() wrapper":   ('def f(sr):\n'
                             '    return list(sr["candidates"])[0]["invented"]\n', CANDS),
    }
    for label, (src, want_node) in shapes.items():
        reads, _ = consumer_paths.sweep(src, schema, "probe")
        hit = [(n, k) for n, k, _u in reads if k == "invented"]
        assert hit, f"{label}: the sweep cannot see this shape at all"
        assert hit[0][0] == want_node, f"{label}: resolved to {hit[0][0]}, want {want_node}"


def test_the_sweep_FAILS_on_a_read_it_cannot_resolve():
    """The vacuous pass, made impossible. `if node in schema` silently skipped anything
    unresolved, which is why the pin passed against the merge base where NO generated node
    name exists. An unresolvable read is reported, and the pin above asserts on it."""
    import consumer_paths
    _, unresolved = consumer_paths.sweep(
        'def f(result):\n    x = result.get("nowhere")\n    return x["deep"]\n',
        {"attempt.response.result": {}}, "probe")
    reads, _ = consumer_paths.sweep(
        'def f(result):\n    return result["k"]\n',
        {"attempt.response.result": {"k": {"type": "string"}}}, "probe")
    assert reads, "the sweep sees nothing at all"
    # THE VACUOUS PASS, made visible. Against a schema whose node names do not match, the
    # sweep resolves nothing -- which is exactly what happened at the merge base. The pin
    # must be able to tell "nothing to check" from "nothing wrong", so it asserts the sweep
    # finds a non-trivial number of reads against the REAL schema before trusting a clean
    # result.
    real = V.schema()
    total = 0
    for name in CONSUMERS:
        r, _u = consumer_paths.sweep((HERE / name).read_text(), real, name)
        total += len(r)
    assert total > 25, (f"the sweep resolved only {total} reads against the real schema; a "
                        "clean totality result would be vacuous")
    stale = sum(len(consumer_paths.sweep((HERE / n).read_text(),
                                         {"result": {}}, n)[0]) for n in CONSUMERS)
    assert stale < total, ("against stale node names the sweep must resolve FEWER reads; "
                           "if it resolves as many, node naming is not being checked")


def test_UNCONDITIONAL_reads_are_distinguished_from_tolerant_ones():
    """`required` rests on this distinction: x["k"] raises when absent, x.get("k") does
    not. Getting it backwards would mark half the schema required and reject real data."""
    import consumer_paths
    schema = V.schema()
    reads, _ = consumer_paths.sweep(
        'def f(result):\n    a = result["status"]\n    b = result.get("status")\n',
        schema, "probe")
    flags = {u for n, k, u in reads if k == "status"}
    assert flags == {True, False}, flags


def test_a_required_key_is_rejected_when_ABSENT_by_both_boundaries():
    """Round 1's repro, both ingestion points."""
    import copy
    import harness
    good = {"result": {"status": "complete", "subject_resolution": {"committed": [], "candidates": [
        {"receipt_id": "rc1", "state": "proposed",
         "subject": {"kind": "team", "canonical_id": "t", "label": "P"},
         "match_mechanisms": ["exact"], "matched_terms": [], "match_reasons": [],
         "evidence_ref_ids": [], "confidence": 0.9}]}}}
    bad = copy.deepcopy(good)
    del bad["result"]["subject_resolution"]["candidates"][0]["receipt_id"]

    assert "failure" not in harness.validate_live_payload(200, good)
    out = harness.validate_live_payload(200, bad)
    assert "failure" in out, "the live boundary accepted a missing required key"
    assert "receipt_id is required and absent" in out["failure"]["message"], out

    ok, _ = V.validate_attempt({"status": 200, "dt": 1.0, "request": {}, "response": good})
    assert ok
    ok, reason = V.validate_attempt(
        {"status": 200, "dt": 1.0, "request": {}, "response": bad})
    assert not ok and "receipt_id is required and absent" in reason, reason
    # the KeyError path cannot be reached: nothing that validates is missing the key
    for c in good["result"]["subject_resolution"]["candidates"]:
        assert c["receipt_id"]


def test_a_key_observed_LESS_than_always_is_NOT_required():
    """Negative control for presence. Requiring everything always-present rejects a
    legitimate response that merely omits a field these runs all happened to carry."""
    schema = V.schema()
    R = "attempt.response.result"
    optional = [k for k, r in schema[R].items()
                if not r.get("required") and r.get("observed")
                and sum(r["observed"].values()) < (r.get("node_visits") or 0)]
    assert optional, "no partially-observed key found -- the control is vacuous"
    k = optional[0]
    ok, reason = V.validate_attempt(
        {"status": 200, "dt": 1.0, "request": {}, "response": {"result": {}}})
    assert ok or k not in (reason or ""), f"{k} was demanded though not always observed"


def test_required_is_measured_or_DECLARED_never_assumed():
    doc = SCHEMA_DOC
    assert doc["_required_rule"], "the required rule is not recorded"
    for full in doc["_declared_required"]:
        node, key = full.rsplit(".", 1)
        rule = V.schema()[node][key]
        assert rule.get("required_declared") is True, full
        assert rule.get("required_consumer"), f"{full} declared with no consumer named"


def test_declared_paths_are_marked_UNMEASURED_and_name_their_consumer():
    """They are the only types here not derived from data, so they are labelled as such."""
    schema = V.schema()
    declared = SCHEMA_DOC["_declared_unmeasured"]
    assert declared, "no declared paths -- the 413 fields should still need declaring"
    for full in declared:
        node, key = full.rsplit(".", 1)
        rule = schema[node][key]
        assert rule.get("unmeasured") is True, full
        assert rule.get("consumer"), f"{full} is declared with no stated consumer"
        assert rule.get("observed") == {}, f"{full} claims observations it does not have"


# ==================================================== unreadable identity
def _score_with(committed, anchor=True, nonexistent=False):
    with tempfile.TemporaryDirectory() as tmp:
        d = Path(tmp) / "replicate"
        d.mkdir(parents=True)
        (d / "q-rep1-t1-a1.json").write_text(json.dumps(_row(committed)))
        row = {"id": "q", "expect": "serve", "basis": None, "nonexistent": nonexistent,
               "anchor": {"kind": "team", "label": "Platform"} if anchor else None}
        e = E.expectation_for(row)
        rec = SI.inspect(tmp, "q", e)
        return (rec["state"],) + E.score(
            e, "served_with_data", subject_substitution=rec.get("subject_substitution"),
            identity_state=rec.get("state"), terminal_status="complete")


def test_an_unreadable_identity_on_an_IDENTITY_DEPENDENT_row_is_unscored():
    """The residual upgrade path, closed. `kind: []` is malformed rather than null, so the
    artefact is genuinely unreadable -- and capping that at agree_weak returned a WEAK
    AGREEMENT for a row that would otherwise have been a substitution disagreement. Our own
    doubt improved a failed row, which is the class this instrument has now hit twice.
    `unscored` is neither an agreement nor a judgement the evidence cannot support.
    """
    for nonexistent, anchor in ((False, True), (True, False)):
        state, verdict, why = _score_with(
            [{"kind": [], "canonical_id": "x", "label": "P"}],
            anchor=anchor, nonexistent=nonexistent)
        assert state == "unreadable_artefact", state
        assert verdict == "unscored", f"anchor={anchor} nonexistent={nonexistent}: {verdict}"
        assert why.startswith("unreadable_identity:"), why
        assert "agree" not in verdict


def test_a_row_that_does_NOT_depend_on_identity_still_only_CAPS():
    """Negative control, and the reason this is a rule rather than a blanket.

    A row naming no anchor and declaring nothing nonexistent can be scored from the bucket
    and the terminal. Voiding it too would throw away verdicts the evidence DOES support,
    and would quietly move rows in every arm -- so the pin holds the boundary of the rule,
    not just the rule.
    """
    state, verdict, why = _score_with([{"kind": [], "canonical_id": "x", "label": "P"}],
                                      anchor=False, nonexistent=False)
    assert state == "unreadable_artefact", state
    assert verdict == "agree_weak", verdict
    assert "identity could not be checked" in why, why


def test_a_READABLE_identity_is_untouched_by_the_new_rule():
    """The rule must fire on unreadable identity ONLY. If it fired on readable rows it
    would zero the table, and every re-score line would move."""
    assert _score_with([{"kind": "team", "canonical_id": "team:P",
                         "label": "Platform"}])[1] == "agree"
    assert _score_with([{"kind": None, "canonical_id": None,
                         "label": "Platform"}])[1] == "disagree"
    assert E.expectation_depends_on_identity(
        {"declared_anchor_name": "Platform"}) is True
    assert E.expectation_depends_on_identity({"declares_nonexistent": True}) is True
    assert E.expectation_depends_on_identity({}) is False


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
