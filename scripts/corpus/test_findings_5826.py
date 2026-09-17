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
import ast
import fnmatch
import json
import subprocess
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
    """Identity between `payload` and `validated`, not a field on either side, decides
    what gets persisted: the SAME object back from validate_live_payload means nothing to
    save; a NEW object (the failure envelope) means the RAW BYTES that produced it are
    worth keeping -- returned as-is, never the decoded `payload` dict."""
    served = {"result": {"status": "complete"}}
    assert harness.raw_payload_to_persist_on_failure(b'{"result":{}}', served, served) is None
    failed = {"cohort": "not schema-shaped"}
    envelope = {"failure": {"code": "acr_malformed_response"}}
    raw = b'{"cohort": "not schema-shaped"}'
    assert harness.raw_payload_to_persist_on_failure(raw, failed, envelope) is raw
    assert harness.raw_payload_to_persist_on_failure(b"ignored", None, {"failure": {}}) is None


def test_persist_raw_response_writes_the_exact_bytes_given():
    """The one file-level pin: a failed turn's raw 200 body lands beside the attempt
    artefact BYTE FOR BYTE -- non-ASCII text and a `1.10` literal both survive, which a
    decode-then-`json.dump` round trip would not (escaping the former, collapsing the
    latter to `1.1`) -- a served turn leaves no such sibling file, and a stale one from an
    earlier attempt at the SAME filename is cleaned up rather than left to lie about the
    current attempt."""
    with tempfile.TemporaryDirectory() as tmp:
        fname = Path(tmp) / "row-rep1-t1-a1.json"
        sibling = Path(tmp) / ("row-rep1-t1-a1.json" + harness.RAW_RESPONSE_SUFFIX)

        raw = '{"label": "café — 分析", "ratio": 1.10}'.encode("utf-8")
        harness.persist_raw_response(fname, raw)
        assert sibling.exists()
        assert sibling.read_bytes() == raw, "the sidecar must be byte-identical to the wire body"

        harness.persist_raw_response(fname, None)
        assert not sibling.exists(), "a served turn must leave no raw-response sibling"


def test_run_replicate_persists_the_raw_body_only_for_the_malformed_turn():
    """End to end through `run_replicate`, a stubbed transport standing in for the live
    HTTP call: turn 1's body fails schema validation (a `cohort` shaped like the pre-fix
    defect) and must leave a raw-response sibling with the exact wire bytes the stub
    returned; turn 2 serves cleanly and must leave none."""
    calls = []

    def stub_post(body):
        calls.append(body)
        if len(calls) == 1:
            raw = (b'{"result": {"request_id": "r", "result_id": "res", '
                   b'"status": "complete", "cohort": "not an object, unresolvable"}}')
            bad = json.loads(raw)
            validated = harness.validate_live_payload(200, bad)
            return 200, validated, 0.1, False, \
                harness.raw_payload_to_persist_on_failure(raw, bad, validated)
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

        suffix = harness.RAW_RESPONSE_SUFFIX
        assert (Path(tmp) / ("row-rep1-t1-a1.json" + suffix)).exists(), \
            "the malformed turn's raw body must be persisted"
        assert not (Path(tmp) / ("row-rep1-t2-a1.json" + suffix)).exists(), \
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
    (label), a oneOf left unchecked (flavor), a same-file $ref reaching a scalar a further
    level down (nested.depth), and the PARENT LINKS for both container fields -- `items`
    (array-of-objects) and `nested` (a plain nested object) -- each wired to its child node
    exactly as an observed field of the same shape would be."""
    with tempfile.TemporaryDirectory() as tmp:
        entry = _write_synthetic_schema(Path(tmp))
        got = MS.canonical_field_types(str(entry), "root")

        assert got["root"]["items"] == {
            "type": "array", "items": "object", "element_node": "root.items[]"}
        assert got["root.items[]"]["ratio"] == {"type": "number", "minimum": 0, "maximum": 1}
        assert got["root.items[]"]["label"] == {"type": "string", "nullable": True}
        assert "flavor" not in got["root.items[]"], \
            "a oneOf field must stay unchecked, never guessed"
        assert got["root.items[]"]["nested"] == {
            "type": "object", "node": "root.items[].nested"}
        assert got["root.items[].nested"]["depth"] == {"type": "int"}


def _write_orphan_prone_canonical_schema(tmp, entry_path):
    """A canonical schema declaring a two-level-deep array-of-objects branch
    (`census[]` -> nested `detail`) that NO sample below will ever carry -- the exact
    shape the class fix wires: a container field whose child node has real scalar fields
    but whose PARENT key was never observed, the fix's own reason to exist."""
    entry_path.write_text(json.dumps({
        "type": "object",
        "properties": {
            "request_id": {"type": "string"},
            "census": {"type": "array", "items": {
                "type": "object",
                "properties": {
                    "axis": {"type": "string"},
                    "count": {"type": "integer"},
                    "detail": {"type": "object", "properties": {
                        "note": {"type": "string"},
                    }},
                    "authorized_count": {"type": ["integer", "null"], "minimum": 0},
                },
            }},
        },
    }))


def _run_measure_schema(tmp, canonical_entry):
    """Runs measure_schema.py as its own process against one sampled artefact that never
    carries `census` at all, plus the canonical schema above, and returns the generated
    schema's `nodes` mapping -- the real generator, the real CLI, never a reimplementation
    of its merge logic."""
    root = tmp / "run1" / "replicate"
    root.mkdir(parents=True)
    (root / "row-rep1-t1-a1.json").write_text(json.dumps({
        "dt": 1.0, "request": {}, "status": 200,
        "response": {"result": {"request_id": "r"}},
    }))
    consumer = tmp / "noop_consumer.py"
    consumer.write_text("")
    out = tmp / "schema.json"
    subprocess.run([sys.executable, str(HERE / "measure_schema.py"),
                    "--roots", str(tmp / "run1"), "--out", str(out),
                    "--consumers", str(consumer),
                    "--canonical-entry", str(canonical_entry),
                    "--canonical-node-prefix", "attempt.response.result"],
                   check=True, cwd=HERE)
    return json.loads(out.read_text())["nodes"]


def _reachable_from(nodes, root="attempt"):
    """Every node name reachable from `root` by following `items`->`element_node` and
    `node` links, the SAME two link shapes validators._check_node itself recurses
    through -- a node absent from this set is a node no validator call can ever reach."""
    seen = set()
    stack = [root]
    while stack:
        n = stack.pop()
        if n in seen or n not in nodes:
            continue
        seen.add(n)
        for rule in nodes[n].values():
            for child in (rule.get("element_node"), rule.get("node")):
                if child:
                    stack.append(child)
    return seen


def test_measure_schema_wires_every_canonical_only_node_reachable_from_the_root():
    """The class fix itself, executed end to end: a canonical-only, never-sampled,
    two-level container branch must leave 0 orphaned nodes in the generated schema, and a
    malformed instance of it must be REJECTED while a well-formed one is ACCEPTED --
    failing either half is exactly the P1 the fix closes."""
    with tempfile.TemporaryDirectory() as tmp:
        tmp = Path(tmp)
        canonical_entry = tmp / "canonical_entry.schema.json"
        _write_orphan_prone_canonical_schema(tmp, canonical_entry)
        nodes = _run_measure_schema(tmp, canonical_entry)

        orphans = set(nodes) - _reachable_from(nodes)
        assert not orphans, f"nodes unreachable from 'attempt': {sorted(orphans)}"

        real_schema, V._SCHEMA = V._SCHEMA, nodes
        try:
            good = {"result": {"request_id": "r",
                               "census": [{"axis": "team", "count": 3,
                                          "detail": {"note": "n"},
                                          "authorized_count": 5}]}}
            ok, reason = V.validate_response(good)
            assert ok, reason

            bad = {"result": {"request_id": "r",
                              "census": [{"axis": 123, "count": "three",
                                         "detail": {"note": 7},
                                         "authorized_count": 5}]}}
            ok, reason = V.validate_response(bad)
            assert not ok, "a malformed canonical-only nested field must be rejected"

            # The nullable half of the SAME canonical-only field: `authorized_count`
            # admits null on the contract's own say-so even though no sample below ever
            # carried a value there at all, sample-inferred or otherwise, to admit it FROM.
            null_ok = {"result": {"request_id": "r",
                                  "census": [{"axis": "team", "count": 3,
                                             "detail": {"note": "n"},
                                             "authorized_count": None}]}}
            ok, reason = V.validate_response(null_ok)
            assert ok, reason
            wrong_type_null_admitted = {"result": {"request_id": "r",
                                        "census": [{"axis": "team", "count": 3,
                                                   "detail": {"note": "n"},
                                                   "authorized_count": "five"}]}}
            ok, reason = V.validate_response(wrong_type_null_admitted)
            assert not ok, "null admitted must not widen into any non-null type"
        finally:
            V._SCHEMA = real_schema


_SIDECAR_PROBE = {"cid": "row-x", "corpus_id": "row-x", "qid": "row-x", "rep": "1"}


class _ProbeDict(dict):
    def __missing__(self, key):
        return _SIDECAR_PROBE.get(key, "*")


def _literal_text(node):
    """Renders a Constant-str or JoinedStr AST node to text, standing in for whatever
    variable an f-string formats with a probe value keyed by the variable/attribute name
    -- concrete enough to fnmatch, without hand-copying any call site's own argument
    names."""
    if isinstance(node, ast.Constant) and isinstance(node.value, str):
        return node.value
    if isinstance(node, ast.JoinedStr):
        parts = []
        for piece in node.values:
            if isinstance(piece, ast.Constant):
                parts.append(piece.value)
            elif isinstance(piece, ast.FormattedValue):
                v = piece.value
                name = v.id if isinstance(v, ast.Name) else getattr(v, "attr", None)
                parts.append(str(_SIDECAR_PROBE.get(name, "*")))
        return "".join(parts)
    return None


def _resolve_string_like(node):
    """The last Constant-str/JoinedStr literal in node's own subtree -- covers a bare
    literal, an f-string, and either wrapped in a call (`str(...)`) or built with `/`
    (`Path(x) / f"..."`)."""
    found = None
    for child in ast.walk(node):
        if isinstance(child, ast.JoinedStr):
            found = child
        elif isinstance(child, ast.Constant) and isinstance(child.value, str) and found is None:
            found = child
    return found


def _iter_glob_call_patterns(path):
    """Every `.glob(...)`/`.rglob(...)`/module-level `glob.glob(...)` call in `path` whose
    first argument resolves to literal text -- this file's OWN call sites, read from its
    own source, not a hand-kept list of where attempt discovery happens."""
    tree = ast.parse(path.read_text(), filename=str(path))
    for node in ast.walk(tree):
        if not isinstance(node, ast.Call) or not node.args:
            continue
        func = node.func
        is_glob = (isinstance(func, ast.Attribute) and func.attr in ("glob", "rglob")) or \
                  (isinstance(func, ast.Name) and func.id == "glob")
        if not is_glob:
            continue
        lit = _resolve_string_like(node.args[0])
        text = _literal_text(lit) if lit is not None else None
        if text:
            yield text.format_map(_ProbeDict())


def _iter_glob_const_patterns(path):
    """Module-level `*_GLOBS` tuple/list constants of literal patterns -- covers a call
    site that globs a PATTERN VARIABLE (`Path(root).glob(g.format(...))` for `g` drawn
    from one of these) rather than a literal, including one built via `A + B` of two such
    constants."""
    tree = ast.parse(path.read_text(), filename=str(path))
    resolved = {}

    def elts_of(expr):
        if isinstance(expr, (ast.Tuple, ast.List)):
            out = []
            for e in expr.elts:
                t = _literal_text(e)
                if t is not None:
                    out.append(t)
            return out
        if isinstance(expr, ast.BinOp) and isinstance(expr.op, ast.Add):
            return elts_of(expr.left) + elts_of(expr.right)
        if isinstance(expr, ast.Name):
            return resolved.get(expr.id, [])
        return []

    out = []
    for node in tree.body:
        if not isinstance(node, ast.Assign):
            continue
        names = [t.id for t in node.targets if isinstance(t, ast.Name)]
        if not any(n.endswith("_GLOBS") for n in names):
            continue
        texts = [t.format_map(_ProbeDict()) for t in elts_of(node.value)]
        for n in names:
            resolved[n] = texts
        out.extend(texts)
    return out


def test_no_artefact_discovery_glob_in_scripts_corpus_matches_the_raw_response_sidecar():
    """Every attempt-discovery glob this package's OWN source defines, evaluated against a
    real attempt filename and its raw-response sidecar. A pattern that matches the attempt
    file must never also match the sidecar -- the P1 this class fix closes: a sidecar
    sharing the attempt file's own discovery pattern gets sampled as a bogus attempt by
    every one of these walks."""
    attempt_name = "row-x-rep1-t1-a1.json"
    sidecar_name = attempt_name + harness.RAW_RESPONSE_SUFFIX

    checked = 0
    for path in sorted(HERE.glob("*.py")):
        if path.name.startswith("test_") or path.name == "conftest.py":
            continue
        patterns = list(_iter_glob_call_patterns(path)) + list(_iter_glob_const_patterns(path))
        for pattern in patterns:
            base = pattern.rsplit("/", 1)[-1]
            if not fnmatch.fnmatch(attempt_name, base):
                continue  # not an attempt-discovery pattern -- irrelevant to this class
            checked += 1
            assert not fnmatch.fnmatch(sidecar_name, base), (
                f"{path.name}: pattern {pattern!r} (basename {base!r}) matches both the "
                f"attempt file {attempt_name!r} and its raw-response sidecar {sidecar_name!r}")
    assert checked >= 1, "no attempt-discovery glob pattern found in scripts/corpus -- vacuous"


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
