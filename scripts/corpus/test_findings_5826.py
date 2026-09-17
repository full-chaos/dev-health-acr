"""CHAOS-5826 pins: the measured artefact boundary must not narrow a field the canonical
contracts/jsonschema/v1 contract already declares wider than what the samples measure_schema.py
was run against happened to contain.

`cohort.members[].drivers[].value` is a genuine 0..1 RATIO by design -- the canonical schema
declares it `number`, and acr's own write-path validator enforces the same 0..1 bound. Every
drivers[].value sample in the artefacts the committed schema was generated from happened to be
a whole number, so pure observation locked the field to `int`, and a later contractually-valid
fraction then read as a boundary violation (`acr_malformed_response`) instead of the served
answer it was.

Fixtures here are built from the WIRE SHAPE (CohortMemberDriver's own required fields), never
from a captured proof artefact and never from corpus question text.
"""
import json
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).parent
sys.path.insert(0, str(HERE))

import measure_schema as MS  # noqa: E402
import validators as V  # noqa: E402
import harness  # noqa: E402


def _cohort_driver_response(value):
    """The SHAPE `validate_response` actually checks -- the "attempt.response" envelope
    (`{"result": {...}}`), not the bare result object -- with one ranked cohort member
    carrying one driver, built from CohortMemberDriver's own required fields."""
    return {
        "result": {
            "request_id": "r", "result_id": "res", "status": "complete",
            "cohort": {
                "kind": "team", "rationale": "r", "complete": True, "truncated": False,
                "members": [{
                    "subject": {"kind": "team", "canonical_id": "team-1", "label": "Team 1"},
                    "rank": 1,
                    "inclusion_reasons": ["ranked"],
                    "drivers": [{
                        "signal": "readiness.coverage_gap",
                        "value": value,
                        "weight": 15,
                        "weight_contributed": 15,
                        "window": "current",
                    }],
                }],
            },
        },
    }


def test_a_genuine_ratio_driver_value_is_accepted():
    """A fraction (the common case for this signal) must validate, not read as malformed."""
    ok, reason = V.validate_response(_cohort_driver_response(0.67))
    assert ok, reason


def test_a_whole_number_driver_value_still_validates():
    """The complement: a ratio that happens to land on a whole number (0.0/1.0) must keep
    validating too -- the fix widens the declared type, it never narrows what was accepted."""
    ok, reason = V.validate_response(_cohort_driver_response(1.0))
    assert ok, reason


def test_measured_schema_declares_drivers_value_as_the_canonical_number_type():
    """The type itself, not just one instance of it: `value`/`weight` on a cohort member
    driver are governed by the canonical contract (`number`), never by what the generating
    samples happened to contain, and the generated schema records that it did so."""
    schema = V.schema()
    driver_node = schema["attempt.response.result.cohort.members[].drivers[]"]
    assert driver_node["value"]["type"] == "number", driver_node["value"]
    assert driver_node["value"].get("canonical_type") is True, driver_node["value"]
    assert driver_node["weight"]["type"] == "number", driver_node["weight"]


def test_a_bool_driver_value_is_still_rejected_under_the_widened_type():
    """The negative control this file's own guard exists for: Python's bool is an int
    subclass, so widening `value` from `int` to `number` must not let a `true`/`false`
    through as if it were a ratio -- `_NUMERIC`/`_type_ok` already guard both, this pins
    the interaction stays intact under the wider type."""
    ok, reason = V.validate_response(_cohort_driver_response(True))
    assert not ok
    assert "got bool" in reason, reason


def test_raw_payload_to_persist_on_failure():
    """Identity, not a field on either side, decides what gets persisted: the SAME
    object back from validate_live_payload means nothing to save; a NEW object (the
    failure envelope) means the raw body that produced it is worth keeping."""
    served = {"result": {"status": "complete"}}
    assert harness.raw_payload_to_persist_on_failure(served, served) is None
    failed = {"cohort": "not schema-shaped"}
    envelope = {"failure": {"code": "acr_malformed_response"}}
    assert harness.raw_payload_to_persist_on_failure(failed, envelope) is failed
    assert harness.raw_payload_to_persist_on_failure(None, {"failure": {}}) is None


def test_persist_raw_response_writes_on_failure_and_is_absent_on_success():
    """The one file-level pin: a failed turn's raw 200 body lands beside the attempt
    artefact, unmutated; a served turn leaves no such sibling file, and a stale one
    from an earlier attempt at the SAME filename is cleaned up rather than left to lie
    about the current attempt."""
    with tempfile.TemporaryDirectory() as tmp:
        fname = Path(tmp) / "row-rep1-t1-a1.json"
        sibling = Path(tmp) / "row-rep1-t1-a1.json.raw-response.json"

        raw = {"cohort": {"members": [{"drivers": [{"value": 0.67}]}]}}
        harness.persist_raw_response(fname, raw)
        assert sibling.exists()
        assert json.loads(sibling.read_text()) == raw

        harness.persist_raw_response(fname, None)
        assert not sibling.exists(), "a served turn must leave no raw-response sibling"


def test_run_replicate_persists_the_raw_body_only_for_the_malformed_turn():
    """End to end through `run_replicate`, a stubbed transport standing in for the live
    HTTP call: turn 1's body fails schema validation (a `drivers[].value` shaped like the
    pre-fix defect, on the UNFIXED base schema this stub simulates by returning a body
    `validate_response` never accepts) and must leave a `.raw-response.json` sibling with
    the exact body the stub returned; turn 2 serves cleanly and must leave none."""
    calls = []

    def stub_post(body):
        calls.append(body)
        if len(calls) == 1:
            bad = {"result": {"request_id": "r", "result_id": "res", "status": "complete",
                              "cohort": "not an object, unresolvable by the schema"}}
            return 200, harness.validate_live_payload(200, bad), 0.1, False, \
                harness.raw_payload_to_persist_on_failure(
                    bad, harness.validate_live_payload(200, bad))
        good = {"result": {"request_id": "r", "result_id": "res", "status": "complete"}}
        return 200, good, 0.1, False, None

    real_post, real_outdir = harness.post, harness.OUTDIR
    real_requested = dict(harness.REQUESTED_KIND)
    with tempfile.TemporaryDirectory() as tmp:
        harness.post = stub_post
        harness.OUTDIR = Path(tmp)
        harness.REQUESTED_KIND = {"row": ""}
        try:
            harness.run_replicate("row", "fixture question text", 1, warn=lambda *_a, **_k: None)
        finally:
            harness.post = real_post
            harness.OUTDIR = real_outdir
            harness.REQUESTED_KIND = real_requested

        assert (Path(tmp) / "row-rep1-t1-a1.json.raw-response.json").exists(), \
            "the malformed turn's raw body must be persisted"
        assert not (Path(tmp) / "row-rep1-t2-a1.json.raw-response.json").exists(), \
            "a served turn must leave no raw-response sibling"


def _write_synthetic_schema(tmp):
    """A minimal two-file canonical schema pair covering canonical_field_types' own input
    domain: same-file $ref, cross-file $ref, a nullable ["string","null"] type, a oneOf
    field with no single resolvable type, a bounded number, and a scalar nested three
    levels down through an array-of-$ref'd-object -- never the real, much larger
    contracts/jsonschema/v1 tree, so each cell is exercised in isolation."""
    (tmp / "common.schema.json").write_text(json.dumps({
        "$defs": {
            "Item": {
                "type": "object",
                "properties": {
                    "ratio": {"type": "number", "minimum": 0, "maximum": 1},
                    "label": {"type": ["string", "null"]},
                    "flavor": {"oneOf": [{"const": "a"}, {"const": "b"}]},
                    "nested": {"$ref": "#/$defs/Nested"},
                },
            },
            "Nested": {
                "type": "object",
                "properties": {"depth": {"type": "integer"}},
            },
        },
    }))
    (tmp / "entry.schema.json").write_text(json.dumps({
        "type": "object",
        "properties": {
            "items": {"type": "array",
                       "items": {"$ref": "common.schema.json#/$defs/Item"}},
        },
    }))
    return tmp / "entry.schema.json"


def test_canonical_field_types_domain():
    """Each cell in canonical_field_types' own input domain, executed in one pass: a
    cross-file $ref (items[] -> Item), a bounded number (ratio), a nullable type list
    (label), a oneOf left unchecked (flavor), and a same-file $ref reaching a scalar a
    further level down (nested.depth)."""
    with tempfile.TemporaryDirectory() as tmp:
        entry = _write_synthetic_schema(Path(tmp))
        got = MS.canonical_field_types(str(entry), "root")

        assert got["root.items[]"]["ratio"] == {"type": "number", "minimum": 0, "maximum": 1}
        assert got["root.items[]"]["label"] == {"type": "string", "nullable": True}
        assert "flavor" not in got["root.items[]"], \
            "a oneOf field must stay unchecked, never guessed"
        assert got["root.items[].nested"]["depth"] == {"type": "int"}


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
