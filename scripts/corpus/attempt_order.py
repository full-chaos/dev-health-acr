#!/usr/bin/env python3
"""THE ordering of attempt artefacts. One helper, used everywhere attempts are sequenced.

Attempts must be ordered by their RECORDED SEQUENCE, never by path. Two separate defects
came from path sorting: rooting replays under the shard tree put `reclassify/...` before
the original it replaced, and lexicographic order puts `t10` before `t9`, so "the last
attempt" was the wrong file in both cases. The fix lived in one module and the identical
defect stayed in the other, so it lives here now and both import it.
"""
import re
from pathlib import Path

# The directory a deadline replay is written under. A replay always sorts AFTER the
# original attempt it supersedes, whatever its indices are.
REPLAY_DIRNAME = "reclassify"

_SEQ = re.compile(r"^(?P<qid>.+)-rep(?P<rep>\d+)-t(?P<turn>\d+)-a(?P<att>\d+)\.json$")


def parse_attempt_name(path):
    """(ok, (rep, turn, att)). A name that does not parse is UNPARSEABLE, not (0,0,0).

    r5: an unparsed filename silently received sequence (0,0,0), so `q-rep1-t10-a1-extra`
    sorted ahead of a real `t9` and could be selected as the terminal attempt. A file we
    cannot sequence is not something to guess a position for. The pattern is anchored at
    both ends, so a trailing suffix does not parse.
    """
    m = _SEQ.match(Path(path).name)
    if not m:
        return False, None
    return True, (int(m.group("rep")), int(m.group("turn")), int(m.group("att")))


def is_replay(path):
    """Replay detection by PATH SEGMENT, independent of how the path is spelled.

    r5: the test was `"/reclassify/" in path`, which needs a leading separator, so a
    RELATIVE path like `reclassify/q/replicate/...` was not seen as a replay and sorted
    ahead of the original it supersedes -- the defect the helper exists to prevent,
    reintroduced by an assumption about path shape.
    """
    return REPLAY_DIRNAME in Path(path).parts


def attempt_sort_key(path):
    ok, seq = parse_attempt_name(path)
    if not ok:
        raise ValueError(f"attempt filename does not parse: {path}")
    rep, turn, att = seq
    return (1 if is_replay(path) else 0, rep, turn, att, str(path))


def order_attempts(paths, on_unparseable="raise"):
    """Sequence order, deduplicated.

    `on_unparseable="skip"` returns (ordered, skipped) for callers that must survive a
    stray file; the default raises, so a filename that cannot be sequenced is never
    silently given a position.
    """
    uniq = sorted(set(str(p) for p in paths))
    good, bad = [], []
    for p in uniq:
        (good if parse_attempt_name(p)[0] else bad).append(p)
    ordered = sorted(good, key=attempt_sort_key)
    if on_unparseable == "skip":
        return ordered, bad
    if bad:
        raise ValueError(f"{len(bad)} attempt filename(s) do not parse: {bad[:3]}")
    return ordered
