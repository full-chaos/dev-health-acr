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
    """(ok, reason). An attempt artefact's envelope, before anything reads it."""
    if not isinstance(attempt, dict):
        return False, f"attempt is not a mapping, got {type(attempt).__name__}"

    resp = attempt.get("response")
    if resp is not None and not isinstance(resp, dict):
        return False, f"response must be a mapping or absent, got {type(resp).__name__}"

    if isinstance(resp, dict):
        result = resp.get("result")
        if result is not None and not isinstance(result, dict):
            return False, f"response.result must be a mapping or absent, got {type(result).__name__}"
        failure = resp.get("failure")
        if failure is not None and not isinstance(failure, dict):
            return False, f"response.failure must be a mapping or absent, got {type(failure).__name__}"

    status = attempt.get("status")
    if status is not None and not isinstance(status, int):
        return False, f"status must be an int or absent, got {type(status).__name__}"

    return True, None
