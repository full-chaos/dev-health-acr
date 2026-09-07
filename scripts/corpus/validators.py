#!/usr/bin/env python3
"""ONE ingestion boundary. Nothing downstream ever sees an unvalidated value.

Four review rounds closed one fail-open path at a time and a fifth opened each time,
because the code trusted whatever shape reached it: a declared expectation could be a
list, an anchor could be missing a key, an attempt's `result` could be an integer. The
class was never "this predicate is wrong" -- it was "there is no boundary".

Everything enters through here. A corpus row that does not satisfy the schema is
`invalid_expectation` and is never scored; an attempt artefact that does not satisfy the
schema is `unparseable` and is never read as evidence. Neither is guessed at, and neither
reaches the scorer or the identity check in a half-trusted state.
"""

EXPECT_VALUES = {"serve", "refuse", "decline", "clarify"}

_SCHEMA = None
_TYPES = {"int": int, "number": (int, float), "string": str, "bool": bool,
          "object": dict, "array": list}


def schema():
    """The artefact schema, loaded once from its data file."""
    global _SCHEMA
    if _SCHEMA is None:
        import json
        from pathlib import Path
        _SCHEMA = json.loads((Path(__file__).parent / "artefact_schema.json").read_text())
    return _SCHEMA


def _check_node(node, node_name, path="attempt"):
    """Recursive shape check against the schema. Returns a reason, or None if valid.

    r6: validation used to stop at the first level -- `result` had to be a mapping, but a
    string nested under `result.subject_resolution` validated fine and then crashed the
    consumer that read it. Every path a consumer dereferences is typed here and checked
    all the way down.
    """
    spec = schema().get(node_name)
    if spec is None or not isinstance(node, dict):
        return None
    for key, rule in spec.items():
        if key not in node:
            if rule.get("required"):
                return f"{path}.{key} is required and absent"
            continue
        val = node[key]
        if val is None:
            return f"{path}.{key} is explicitly null; omit it instead"
        want = _TYPES[rule["type"]]
        if rule["type"] == "int" and isinstance(val, bool):
            return f"{path}.{key} must be an int, got bool"
        if not isinstance(val, want):
            return (f"{path}.{key} must be {rule['type']}, got "
                    f"{type(val).__name__}")
        # r9: an array's ELEMENTS are typed too. Typing the array and trusting its members
        # was the same shallow boundary one level lower -- `committed: [1]` validated and
        # then crashed the consumer that read `committed[0].get(...)`.
        if rule["type"] == "array" and rule.get("items"):
            item_want = _TYPES[rule["items"]]
            for i, item in enumerate(val):
                if rule["items"] == "int" and isinstance(item, bool):
                    return f"{path}.{key}[{i}] must be an int, got bool"
                if not isinstance(item, item_want):
                    return (f"{path}.{key}[{i}] must be {rule['items']}, got "
                            f"{type(item).__name__}")
        if key in schema():                       # a node the schema describes: recurse
            deeper = _check_node(val, key, f"{path}.{key}")
            if deeper:
                return deeper
    return None


def validate_corpus_row(row):
    """(ok, reason). Checks the DECLARED fields only; the rest of the row is the
    corpus's business."""
    if not isinstance(row, dict):
        return False, "row is not a mapping"

    expect = row.get("expect")
    if expect is not None and (not isinstance(expect, str) or expect not in EXPECT_VALUES):
        return False, f"expect must be one of {sorted(EXPECT_VALUES)} or None, got {expect!r}"

    basis = row.get("basis")
    if basis is not None and not isinstance(basis, str):
        return False, f"basis must be a string or None, got {type(basis).__name__}"

    anchor = row.get("anchor")
    if anchor is not None:
        if not isinstance(anchor, dict):
            return False, f"anchor must be a mapping or None, got {type(anchor).__name__}"
        for key in ("kind", "label"):
            if not isinstance(anchor.get(key), str) or not anchor.get(key):
                return False, f"anchor.{key} must be a non-empty string"

    nonexistent = row.get("nonexistent", False)
    # bool only: "false" is a string and would be truthy, which is how a declaration
    # meaning the opposite of what it says gets believed.
    if not isinstance(nonexistent, bool):
        return False, f"nonexistent must be a bool, got {type(nonexistent).__name__}"

    return True, None


def validate_attempt(attempt):
    """(ok, reason). An attempt envelope -- from an artefact file OR a live response.

    DEEP, and deliberately so. A shallow check passed `{"response": {"result": [1]}}`
    because `response` was a mapping, and the list then crashed whatever dereferenced it.
    An EMPTY result or failure mapping is VALID: an empty document is still a document,
    and rejecting it conflated "nothing in it" with "not readable".
    `status` must be an int; None and bool are rejected -- bool is an int subclass in
    Python, so `True` would otherwise pass as a status code.
    """
    if not isinstance(attempt, dict):
        return False, f"attempt is not a mapping, got {type(attempt).__name__}"

    reason = _check_node(attempt, "attempt")
    if reason:
        return False, reason
    return True, None


def load_attempt(path):
    """THE loader. The ONLY place an attempt artefact's JSON is decoded.

    Returns (ok, attempt, reason). Every consumer -- identity, the failure scanner, the
    shard writer -- goes through here, so none of them can hold a different opinion about
    whether a file is readable, and none of them can dereference an unvalidated shape.
    Four separate crashes came from four separate readers each decoding and trusting the
    same file in its own way.
    """
    import json
    try:
        with open(path) as fh:
            data = json.load(fh)
    except Exception as exc:                      # unreadable bytes, bad JSON, missing file
        return False, None, f"{type(exc).__name__}: {exc}"
    ok, reason = validate_attempt(data)
    if not ok:
        return False, None, reason
    return True, data, None
