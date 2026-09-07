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

    if "response" in attempt:
        resp = attempt["response"]
        if resp is not None and not isinstance(resp, dict):
            return False, f"response must be a mapping or absent, got {type(resp).__name__}"
        if isinstance(resp, dict):
            for key in ("result", "failure"):
                if key in resp:
                    val = resp[key]
                    if val is None:
                        return False, f"response.{key} is explicitly null; omit it instead"
                    if not isinstance(val, dict):
                        return False, (f"response.{key} must be a mapping, got "
                                       f"{type(val).__name__}")

    if "status" in attempt:
        status = attempt["status"]
        if isinstance(status, bool):
            return False, "status must be an int, got bool"
        if status is not None and not isinstance(status, int):
            return False, f"status must be an int or absent, got {type(status).__name__}"
        if status is None:
            return False, "status is explicitly null; omit it instead"

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
